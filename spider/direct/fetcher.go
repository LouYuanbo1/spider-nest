package direct

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/LouYuanbo1/spider-nest/spider/hijack"
)

// Fetcher 纯 HTTP 直连引擎：把浏览器捕获的请求快照（hijack.RequestCapture）
// 翻译成一个标准的 *http.Request，并交给内部持有的 *http.Client 发出——
// http.Client 自身携带 Transport，其高级 Do 提供了重定向、cookie、超时、连接池等
// 开箱即用的语义，无需裸调 RoundTrip。它是 direct.Client（聪明蜘蛛）的直连执行器。
//
// 设计上与浏览器世界完全解耦：
//   - 生命周期独立无状态，可跨 Spider / Nest 复用；
//   - 无状态、可用 httptest 独立测试；
//   - 语义上天然承接"捕获 → 翻译 → 直连重放"的两段式流程。
type Fetcher struct {
	client  *http.Client  // 高级直连客户端；其内部 Transport 由 Option 装配
	timeout time.Duration // 单次直连超时；<=0 表示不主动加超时
	retries int           // 出错重试次数；出错后整体请求可重发
}

// FetcherOption 直连引擎的构造函数选项，沿用 Functional Option 风格。
// 与 direct.Client 的 Option 并存，前缀 Fetcher 以区分两者。
type FetcherOption func(*Fetcher)

// WithTransport 给内部 http.Client 注入自定义的 http.Transport；nil 时沿用 http.Client 默认传输。
func WithTransport(t *http.Transport) FetcherOption {
	return func(c *Fetcher) {
		if c.client != nil {
			c.client.Transport = t
		}
	}
}

// WithTimeout 设置单次直连的超时；<=0 表示不主动加超时（依赖上游上下文）。
// 该超时落在内部 http.Client.Timeout 上，覆盖"连接 + 读响应体"全程（见 Do）。
func WithTimeout(d time.Duration) FetcherOption {
	return func(c *Fetcher) {
		c.timeout = d
		if d > 0 {
			c.client.Timeout = d
		}
	}
}

// WithRetries 设置出错后的重试次数（默认 0，即不重试）。
func WithRetries(n int) FetcherOption {
	return func(c *Fetcher) { c.retries = n }
}

// WithProxy 给内部 http.Client 的 Transport 装配出向代理（可用于出口切换/IP 轮换）。
func WithProxy(proxy func(*http.Request) (*url.URL, error)) FetcherOption {
	return func(c *Fetcher) {
		if c.client != nil {
			if t, ok := c.client.Transport.(*http.Transport); ok && t != nil {
				t.Proxy = proxy
			}
		}
	}
}

// NewFetcher 创建纯 HTTP 直连引擎。默认持有新分配的 http.Client（默认 Transport），
// 可通过 FetcherOption 覆盖或追加配置。
func NewFetcher(opts ...FetcherOption) *Fetcher {
	c := &Fetcher{client: &http.Client{}}
	for _, o := range opts {
		o(c)
	}
	return c
}

// HTTPClient 暴露内部持有的 *http.Client，便于层面上的直接使用。
func (c *Fetcher) HTTPClient() *http.Client { return c.client }

// Request 把浏览器捕获的请求快照翻译成一个标准的 *http.Request。
// 方法 / URL / 请求头 / 请求体（字符串形式）原样搬移；body 为空则无请求体。
// ctx 会透传到请求级 Context，供上游取消/超时贯穿直连全程。
func (c *Fetcher) Request(ctx context.Context, cap *hijack.RequestCapture) (*http.Request, error) {
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
	// proto.NetworkHeaders 是 map[string]gson.JSON，值多为单条字符串，逐键 Set 即可。
	for k, v := range cap.Headers {
		req.Header.Set(k, v.Str())
	}
	return req, nil
}

// Send 直接把捕获的请求快照翻译并发出，等价于 Request + Do 的便捷组合。
// ctx 透传到请求级 Context：上游取消/超时对直连同样生效。
func (c *Fetcher) Send(ctx context.Context, cap *hijack.RequestCapture) (*http.Response, error) {
	req, err := c.Request(ctx, cap)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}

// Do 通过内部 *http.Client 的高级 Do 发出请求。出错时按 retries 重试（重试复用
// 原始请求头，重读一次 body）。请求级 Context 沿用调用方（即上游 ctx），用于取消；
// 客户端配置的 timeout 落在 http.Client.Timeout 上，覆盖"连接 + 读响应体"全程，
// 因此 Do 返回后不再手动 cancel——若像旧实现那样在 client.Do 一返回就 cancel，
// 调用方读 resp.Body 时会撞上 context canceled。
func (c *Fetcher) Do(req *http.Request) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt <= c.retries; attempt++ {
		clone := req.Clone(req.Context())
		// http.NewRequest 对字符串 Reader 已设置 GetBody，重试时据此重建一次 body。
		if attempt > 0 && req.GetBody != nil {
			if body, err := req.GetBody(); err == nil {
				clone.Body = body
			}
		}
		resp, err := c.client.Do(clone)
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
