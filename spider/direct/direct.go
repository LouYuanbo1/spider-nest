package direct

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/LouYuanbo1/spider-nest/spider/hijack"
)

// Target 一次直连目标：默认方法 GET，Body 非空时才携带请求体（仅字符串形式，与
// hijack.RequestCapture.Body 对齐）。
type Target struct {
	Method string
	URL    string
	Body   string
}

func (t Target) method() string {
	if t.Method == "" {
		return "GET"
	}
	return t.Method
}

// Acquirer 负责"从浏览器这头弄到一套新鲜请求头"。每次调用都应触发一次真实请求，
// 以便拿到此刻仍有效的 cookie / 反爬签名等，再交给直连复用。
// 实现通常借助 rod 劫持（hijack）监听并捕获匹配请求的请求头快照。
type Acquirer interface {
	// Acquire 触发一次真实请求并返回匹配请求的请求头快照。
	Acquire(ctx context.Context, t Target) (*hijack.RequestCapture, error)
}

// Client 带失效回退的直连蜘蛛（核心状态机）：
//
//  1. 优先缓存身上"最后一次有效"的请求头，走纯 HTTP 直连 —— 快、不占用浏览器；
//  2. 直连失败，或状态码命中刷新策略（默认 403/429，即疑似被反爬拦），
//     回退借用 Acquirer（一般基于 rod）重新偷一套新头，再直连重试；
//  3. 超过 MaxRetries 仍失败则返回最后一次错误。
//
// 本组件与浏览器世界彻底解耦：只会依赖注入进来的 Acquirer 与 Fetcher，
// 不含任何 rod 依赖，方便用假 Acquirer 独立测试整段回退逻辑。
type Client struct {
	fetcher *Fetcher
	acquire Acquirer

	mu     sync.Mutex
	cached map[string]*hijack.RequestCapture // 按 URL 记忆"最后有效"的请求头快照

	maxRetries int              // 一次 Fetch 内最多重偷头+直连的刷新轮数
	refreshSet map[int]struct{} // 命中这些状态码视为需要刷新（反爬信号）
	logger     *slog.Logger
}

// Option 直连蜘蛛的构造函数选项，沿用 Functional Option 风格。
type Option func(*Client)

// WithMaxRetries 设置一次 Fetch 内"重偷头并直连"的最多重试轮数（默认 0，即只试一次）。
func WithMaxRetries(n int) Option {
	return func(c *Client) { c.maxRetries = n }
}

// WithRefreshStatuses 覆盖"触发刷新"的状态码集合。传空则关闭基于状态码的刷新
// （仅网络错误才触发回退）。默认集合为 {403, 429}。
func WithRefreshStatuses(codes ...int) Option {
	return func(c *Client) {
		c.refreshSet = make(map[int]struct{}, len(codes))
		for _, code := range codes {
			c.refreshSet[code] = struct{}{}
		}
	}
}

// WithLogger 注入日志器，未注入时使用 slog.Default()。
func WithLogger(l *slog.Logger) Option {
	return func(c *Client) {
		if l != nil {
			c.logger = l
		}
	}
}

// New 创建带失效回退的直连蜘蛛。fetcher 为纯 HTTP 直连引擎；acquire 为"偷新头"来源
// （nil 则只允许使用 Seed 预先注入的头，失去回退能力）。
func New(fetcher *Fetcher, acquire Acquirer, opts ...Option) *Client {
	c := &Client{
		fetcher:    fetcher,
		acquire:    acquire,
		cached:     make(map[string]*hijack.RequestCapture),
		maxRetries: 0,
		refreshSet: map[int]struct{}{403: {}, 429: {}},
		logger:     slog.Default(),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

func (c *Client) log() *slog.Logger {
	if c.logger != nil {
		return c.logger
	}
	return slog.Default()
}

// Seed 预先注入一条请求头快照（例如外部已经"监听一次"捕获到的头），按方法+URL 记忆，
// 供后续 Fetch 直连复用。可多次调用覆盖。
func (c *Client) Seed(t Target, cap *hijack.RequestCapture) {
	if cap == nil {
		return
	}
	if cap.Method == "" {
		cap.Method = t.method()
	}
	if cap.URL == "" {
		cap.URL = t.URL
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cached[t.URL] = cap
}

// Clear 丢弃某 URL 的缓存请求头，下次 Fetch 该 URL 会重新偷头。
func (c *Client) Clear(urlStr string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.cached, urlStr)
}

func (c *Client) current(t Target) *hijack.RequestCapture {
	c.mu.Lock()
	defer c.mu.Unlock()
	cap := c.cached[t.URL]
	if cap == nil || cap.Method != t.method() {
		return nil
	}
	return cap
}

func (c *Client) invalidate(t Target) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.cached, t.URL)
}

// Fetch 返回一次成功的 *http.Response（调用方负责关闭 Body）。
// 命中缓存头则直连；失败时进入 fallback，借 Acquirer 偷新头重试，直到 MaxRetries 用尽。
func (c *Client) Fetch(ctx context.Context, t Target) (*http.Response, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		cap := c.current(t)
		if cap == nil {
			got, err := c.safeAcquire(ctx, t)
			if err != nil {
				lastErr = err
			} else {
				cap = got
				c.Seed(t, cap)
			}
		}

		if cap != nil {
			resp, err := c.fetcher.Fetch(cap)
			if err == nil && !c.isRefreshStatus(resp.StatusCode) {
				return resp, nil
			}
			if resp != nil {
				resp.Body.Close()
			}
			if err != nil {
				lastErr = fmt.Errorf("直连失败: %w", err)
			} else if resp != nil {
				lastErr = fmt.Errorf("直连命中需刷新状态码 %d", resp.StatusCode)
			}
			c.log().Warn("直连未达预期，进入回退刷新请求头",
				slog.String("url", t.URL),
				slog.String("method", t.method()),
				slog.Any("err", lastErr))
			c.invalidate(t)
		}

		if attempt >= c.maxRetries {
			if lastErr == nil {
				lastErr = errors.New("直连未返回结果")
			}
			return nil, lastErr
		}
	}
}

// Fresh 强制从 Acquirer 重新偷一套头后再直连（无视缓存）。用于调用方确认当前头已过期、
// 想直接走一遍完整回退的场景。
func (c *Client) Fresh(ctx context.Context, t Target) (*http.Response, error) {
	c.Clear(t.URL)
	return c.Fetch(ctx, t)
}

func (c *Client) safeAcquire(ctx context.Context, t Target) (*hijack.RequestCapture, error) {
	if c.acquire == nil {
		return nil, errors.New("未配置 Acquirer 且缓存中无可用请求头")
	}
	return c.acquire.Acquire(ctx, t)
}

func (c *Client) isRefreshStatus(code int) bool {
	if len(c.refreshSet) == 0 {
		return false
	}
	_, ok := c.refreshSet[code]
	return ok
}
