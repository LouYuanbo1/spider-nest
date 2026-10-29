package spider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/LouYuanbo1/spider-nest/action"
	"github.com/LouYuanbo1/spider-nest/spider/hijack"
	"github.com/LouYuanbo1/spider-nest/web"
)

var _ Spider = (*Portia)(nil)

// defaultCaptureTimeout 偷头时等待"真实请求被写进劫持回调"的兜底超时。
const defaultCaptureTimeout = 30 * time.Second

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

// Portia 聪明直连蜘蛛：在保留基础蜘蛛（Spider）全部能力的底座上，叠加"偷请求头 ->
// 走纯 HTTP 直连"的加速路径。因此它既是一只合格的蜘蛛（可被 Nest 托管复用），
// 又额外拥有 Fetch/Fresh 等直连抓取的扩展能力——由于返回的是具体类型 *Portia，
// Nest 的泛型约束（nestSpider）能直接把 *Portia 递出来，调用方无需类型断言即可
// 调用扩展方法。
//
// 直连状态机（重构自原先的 direct 包）：
//  1. 优先用缓存身上"最后一次有效"的请求头走纯 HTTP 直连 —— 快、不占用浏览器；
//  2. 直连失败，或状态码命中刷新策略（默认 403/429，即疑似被反爬拦），
//     回退借用内部的基础蜘蛛（Spider）重新偷一套新头，再直连重试；
//  3. 超过 MaxRetries 仍失败则返回最后一次错误。
type Portia struct {
	Spider // 基础蜘蛛即"头制造机"：借它的浏览器级劫持监听/触发真实请求偷头

	client  *http.Client // 纯 HTTP 直连客户端；其内部 Transport 由 PortiaOption 装配
	retries int          // 直连出错后的重试次数（传输层层面）

	mu     sync.Mutex
	cached map[string]*hijack.RequestCapture // 按 URL 记忆"最后有效"的请求头快照

	maxRetries int              // 一次 Fetch 内最多重偷头+直连的刷新轮数
	refreshSet map[int]struct{} // 命中这些状态码视为需要刷新（反爬信号）
	logger     *slog.Logger

	preheatMu sync.Mutex  // 保护预热只执行一次
	preheated bool        // 是否已完成预热
	preheat   PreheatFunc // 偷头前需先建立站点上下文的可选策略（如种指纹 Cookie）；nil 则跳过
}

// PortiaOption 构造函数选项，沿用 Functional Option 风格。
type PortiaOption func(*Portia)

// WithMaxRetries 设置一次 Fetch 内"重偷头并直连"的刷新轮数（默认 0，即只试一次）。
// 注意：它控制"偷头回退"的次数，与传输层重试 WithRetries 语义不同，勿混淆。
func WithMaxRetries(n int) PortiaOption {
	return func(c *Portia) { c.maxRetries = n }
}

// WithRefreshStatuses 覆盖"触发刷新"的状态码集合。传空则关闭基于状态码的刷新
// （仅网络错误才触发回退）。默认集合为 {403, 429}。
func WithRefreshStatuses(codes ...int) PortiaOption {
	return func(c *Portia) {
		c.refreshSet = make(map[int]struct{}, len(codes))
		for _, code := range codes {
			c.refreshSet[code] = struct{}{}
		}
	}
}

// PreheatFunc 预热策略：在偷头直连前，让基础蜘蛛先访问一次站点以建立所需上下文。
// base 是 Portia 内嵌的基础蜘蛛，策略自借网导航种下指纹 Cookie（bilibili 的 buvid 等）。
// 预热与偷头是先后两次独立的 BorrowWeb（回显使用不同页面，页面池需 >= 2 才能各借一张），
// 各自独立的超时预算互不拖累；Portia 只调度一次预热，不持有页面细节。
type PreheatFunc func(ctx context.Context, base Spider) error

// WithWarmup 注入偷头前的预热策略。仅执行一次（幂等）；策略为 nil 时跳过预热。
func WithWarmup(fn PreheatFunc) PortiaOption {
	return func(c *Portia) { c.preheat = fn }
}

// PreheatNavigate 返回一个内置预热策略：让基础蜘蛛借一张网导航到 u，等待加载完成，
// 让该站点反爬 JS 种下指纹/会话 Cookie。可用作 WithWarmup 的参数。
func PreheatNavigate(u string) PreheatFunc {
	return func(ctx context.Context, base Spider) error {
		return base.BorrowWeb(func(w *web.Web) error {
			return w.Navigate(ctx, &action.Navigate{URL: u})
		})
	}
}

// WithLogger 注入日志器，未注入时使用 slog.Default()。
func WithLogger(l *slog.Logger) PortiaOption {
	return func(c *Portia) {
		if l != nil {
			c.logger = l
		}
	}
}

// WithTimeout 设置单次直连的超时；<=0 表示不主动加超时（依赖上游上下文）。
// 超时落在内部 http.Client.Timeout 上，覆盖"连接 + 读响应体"全程。
func WithTimeout(d time.Duration) PortiaOption {
	return func(c *Portia) {
		if d > 0 {
			c.client.Timeout = d
		}
	}
}

// WithRetries 设置直连出错后的重试次数（默认 0，即不重试）。注意：它控制"传输层重试"，
// 与偷头回退轮数 WithMaxRetries 语义不同，勿混淆。
func WithRetries(n int) PortiaOption {
	return func(c *Portia) { c.retries = n }
}

// WithProxy 给内部 http.Client 的 Transport 装配出向代理（可用于出口切换/IP 轮换）。
func WithProxy(proxy func(*http.Request) (*url.URL, error)) PortiaOption {
	return func(c *Portia) {
		if c.client != nil {
			if t, ok := c.client.Transport.(*http.Transport); ok && t != nil {
				t.Proxy = proxy
			}
		}
	}
}

// WithTransport 给内部 http.Client 注入自定义的 http.Transport；nil 时沿用默认传输。
func WithTransport(t *http.Transport) PortiaOption {
	return func(c *Portia) {
		if c.client != nil {
			c.client.Transport = t
		}
	}
}

// NewPortia 创建一只聪明直连蜘蛛。base 为底座的"头制造机"（一般是一只 Weaver），
// Portia 借它的浏览器偷头、走纯 HTTP 直连，同时保留其全部蜘蛛能力。
// 返回具体类型 *Portia（与 error 成对）：既实现了 Spider（可入巢），也让 Nest 能直接把
// 具体实例交给调用方；其签名天然契合 Nest 的 create 工厂 func() (S, error)。
func NewPortia(base Spider, opts ...PortiaOption) (*Portia, error) {
	if base == nil {
		return nil, errors.New("portia: 底层 Spider 不能为空")
	}
	p := &Portia{
		Spider:     base,
		client:     &http.Client{Transport: &http.Transport{}},
		cached:     make(map[string]*hijack.RequestCapture),
		maxRetries: 0,
		refreshSet: map[int]struct{}{403: {}, 429: {}},
		logger:     slog.Default(),
	}
	for _, o := range opts {
		if o != nil {
			o(p)
		}
	}
	return p, nil
}

func (p *Portia) log() *slog.Logger {
	if p.logger != nil {
		return p.logger
	}
	return slog.Default()
}

// Seed 预先注入一条请求头快照（例如外部已经"监听一次"捕获到的头），按方法+URL 记忆，
// 供后续 Fetch 直连复用。可多次调用覆盖。注：不会改写传入的 cap，缺方法/URL 时只在缓存内补全。
func (p *Portia) Seed(t Target, cap *hijack.RequestCapture) {
	if cap == nil {
		return
	}
	entry := cap
	if entry.Method == "" || entry.URL == "" {
		cp := *cap
		if cp.Method == "" {
			cp.Method = t.method()
		}
		if cp.URL == "" {
			cp.URL = t.URL
		}
		entry = &cp
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cached[t.URL] = entry
}

// Clear 丢弃某 URL 的缓存请求头，下次 Fetch 该 URL 会重新偷头。
func (p *Portia) Clear(urlStr string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.cached, urlStr)
}

func (p *Portia) current(t Target) *hijack.RequestCapture {
	p.mu.Lock()
	defer p.mu.Unlock()
	cap := p.cached[t.URL]
	if cap == nil || cap.Method != t.method() {
		return nil
	}
	return cap
}

func (p *Portia) invalidate(t Target) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.cached, t.URL)
}

// Fetch 返回一次成功的 *http.Response（调用方负责关闭 Body）。
// 命中缓存头则直连；失败时进入回退：借内部基础蜘蛛偷新头重试，直到 MaxRetries 用尽。
func (p *Portia) Fetch(ctx context.Context, t Target) (*http.Response, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		p.log().Info("Fetch 开始一轮尝试", "attempt", attempt, "url", t.URL, "method", t.method())
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		cap := p.current(t)
		if cap == nil {
			p.log().Info("无缓存请求头，走偷头", "url", t.URL)
			got, err := p.capture(ctx, t)
			if err != nil {
				lastErr = err
				p.log().Warn("偷头失败", "err", err, "url", t.URL)
			} else {
				cap = got
				p.Seed(t, cap)
				p.log().Info("偷头入缓存", "head数", len(cap.Headers), "url", t.URL)
			}
		} else {
			p.log().Info("命中缓存请求头，直接直连", "head数", len(cap.Headers), "url", t.URL)
		}

		if cap != nil {
			resp, err := p.send(ctx, cap)
			if err == nil && !p.isRefreshStatus(resp.StatusCode) {
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
			p.log().Warn("直连未达预期，进入回退刷新请求头",
				slog.String("url", t.URL),
				slog.String("method", t.method()),
				slog.Any("err", lastErr))
			p.invalidate(t)
		}

		if attempt >= p.maxRetries {
			if lastErr == nil {
				lastErr = errors.New("直连未返回结果")
			}
			return nil, lastErr
		}
	}
}

// doPreheat 调度一次可选预热策略，在进入"偷头"（WithHijack+BorrowWeb）之前独立执行：
// 策略自借一张网、自还，与后续偷头各占独立页面与独立超时预算。
func (p *Portia) doPreheat(ctx context.Context) error {
	if p.preheat == nil {
		return nil
	}
	p.preheatMu.Lock()
	defer p.preheatMu.Unlock()
	if p.preheated {
		return nil
	}
	p.log().Info("站点预热：先建立目标站点上下文（如种下指纹 Cookie）")
	if err := p.preheat(ctx, p.Spider); err != nil {
		return fmt.Errorf("站点预热失败: %w", err)
	}
	p.preheated = true
	return nil
}

// Fresh 强制借基础蜘蛛重新偷一套头后再直连（无视缓存）。用于调用方确认当前头已过期、
// 想直接走一遍完整回退的场景。
func (p *Portia) Fresh(ctx context.Context, t Target) (*http.Response, error) {
	p.Clear(t.URL)
	return p.Fetch(ctx, t)
}

// capture 借内部基础蜘蛛（Spider 的浏览器级劫持）触发一次真实请求，偷回一套请求头快照。
// 事件循环里的请求头与浏览器真实发出的完全一致（含 cookie/反爬签名等），可直接用于直连。
func (p *Portia) capture(ctx context.Context, t Target) (*hijack.RequestCapture, error) {
	if t.URL == "" {
		return nil, errors.New("Capture 目标 URL 不能为空")
	}

	ch := make(chan *hijack.RequestCapture, 1)

	p.log().Info("开始偷头（浏览器页面保持空白属正常，仅在后台 fetch 真实请求）",
		"url", t.URL, "method", t.method())

	// 预热先独立执行：它自借自还一张网导航种 Cookie，与偷头用不同页面、不同超时预算。
	// 之后挂监听 + 借网偷头时，不会被预热的重首页(如 bilibili)拖累成超时。
	if err := p.doPreheat(ctx); err != nil {
		p.log().Warn("预热失败，中止本轮偷头", "err", err)
		return nil, err
	}
	p.log().Info("预热完成（或无需预热），开始挂监听偷头")

	// 以"借-用-还"为一组挂起一次性劫持监听：命中目标 URL 且方法一致时捕获请求头回传。
	// WithHijack 保证监听 goroutine 只在本次窗口存活、用完即撤。
	err := p.WithHijack(func(sp Spider) error {
		// 借一张网，用"真实导航"到目标 URL 来偷头：导航不受 CORS/JS Promise 限制，
		// 是必然发出的网络请求，hijack 一定能在请求 pause 时捕获请求头——把原先"跨域
		// fetch + 未 await Promise"导致的超时（返回 undefined）整个绕开。
		return sp.BorrowWeb(func(w *web.Web) error {
			p.log().Info("借网成功，在页面上发起真实导航以偷头", "url", t.URL)
			if err := w.Navigate(ctx, &action.Navigate{URL: t.URL}); err != nil {
				p.log().Warn("偷头导航失败", "err", err, "url", t.URL)
				return err
			}
			return nil
		})
	}, hijack.HijackRule{
		Pattern: t.URL,
		Method:  t.method(),
		Handler: hijack.RequestHandler(func(c *hijack.RequestCapture) {
			select {
			case ch <- c:
			default: // 已满足请求，后续多余命中直接丢弃
			}
		}),
	})
	if err != nil {
		return nil, err
	}
	p.log().Info("监听窗口已结束（WaitHijack 返回）")

	// 等待劫持回调把请求头送回。
	timeout := time.After(defaultCaptureTimeout)
	select {
	case cap := <-ch:
		if cap == nil {
			return nil, errors.New("捕获到空的请求头快照")
		}
		p.log().Info("偷头成功", "method", cap.Method, "url", cap.URL, "头数量", len(cap.Headers))
		return cap, nil
	case <-ctx.Done():
		p.log().Info("偷头被调用方上下文取消", "err", ctx.Err())
		return nil, ctx.Err()
	case <-timeout:
		p.log().Warn("偷头超时：浏览器确实发起了请求但仍未捕获到目标请求头。",
			"超时", defaultCaptureTimeout, "url", t.URL,
			"可能原因", "目标请求被服务Worker/风控吞掉、被重定向到别的URL，或监听未能在请求 pause 时命中")
		return nil, fmt.Errorf("在 %v 内未捕获到目标请求 %s", defaultCaptureTimeout, t.URL)
	}
}

// send 把捕获的请求快照翻译并发出，等价于 request + do 的便捷组合。
func (p *Portia) send(ctx context.Context, cap *hijack.RequestCapture) (*http.Response, error) {
	req, err := p.request(ctx, cap)
	if err != nil {
		return nil, err
	}
	return p.do(req)
}

// request 把浏览器捕获的请求快照翻译成一个标准的 *http.Request。
// 方法 / URL / 请求头 / 请求体（字符串形式）原样搬移；body 为空则无请求体。
// ctx 会透传到请求级 Context，供上游取消/超时贯穿直连全程。
func (p *Portia) request(ctx context.Context, cap *hijack.RequestCapture) (*http.Request, error) {
	if cap == nil {
		return nil, errors.New("RequestCapture 不能为空")
	}
	var body io.Reader
	if cap.Body != "" {
		body = strings.NewReader(cap.Body)
	}
	req, err := http.NewRequestWithContext(ctx, cap.Method, cap.URL, body)
	if err != nil {
		return nil, err
	}
	// hijack.RequestCapture.Headers 是 map[string]gson.JSON，值多为单条字符串，逐键 Set 即可。
	for k, v := range cap.Headers {
		req.Header.Set(k, v.Str())
	}
	return req, nil
}

// do 通过内部 *http.Client 的高级 Do 发出请求。出错时按 retries 重试（重试复用
// 原始请求头，重读一次 body）。请求级 Context 沿用调用方（即上游 ctx），用于取消；
// 客户端配置的 timeout 落在 http.Client.Timeout 上，覆盖"连接 + 读响应体"全程，
// 因此 do 返回后不再手动 cancel——否则调用方读 resp.Body 时会撞上 context canceled。
func (p *Portia) do(req *http.Request) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt <= p.retries; attempt++ {
		clone := req.Clone(req.Context())
		// http.NewRequest 对字符串 Reader 已设置 GetBody，重试时据此重建一次 body。
		if attempt > 0 && req.GetBody != nil {
			if body, err := req.GetBody(); err == nil {
				clone.Body = body
			}
		}
		resp, err := p.client.Do(clone)
		if err != nil {
			lastErr = err
			if resp != nil {
				_ = resp.Body.Close()
			}
			continue
		}
		return resp, nil
	}
	return nil, lastErr
}

func (p *Portia) isRefreshStatus(code int) bool {
	if len(p.refreshSet) == 0 {
		return false
	}
	_, ok := p.refreshSet[code]
	return ok
}
