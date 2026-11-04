package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LouYuanbo1/spider-nest/spider"
	"github.com/LouYuanbo1/spider-nest/web"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// webSource 抽象"如何按 web_id 解析出一张网"，让工具注册层不关心句柄背后的具体来源。
// 它在单网模式（旧 API / http.go）与多网 broker 模式之间提供了统一入口。
type webSource interface {
	// Resolve 按 web_id 取回对应的网；webID 不存在（未借/已还）时返回可读错误。
	Resolve(ctx context.Context, webID string) (*web.Web, error)
	// OpTimeout 返回该来源统一的单次动作超时。
	OpTimeout() time.Duration
}

// singleWebSource 单网模式：固定一张网，忽略 web_id（供旧 NewMCPServer 兼容 http.go 使用）。
// 在这种模式下不要求调用方提供 web_id，任何动作都作用于这张网。
type singleWebSource struct{ w *web.Web }

func (s singleWebSource) Resolve(_ context.Context, _ string) (*web.Web, error) { return s.w, nil }
func (s singleWebSource) OpTimeout() time.Duration                              { return s.w.OpTimeout() }

var (
	// ErrWebNotFound 句柄不存在：要么没先调用 open_web_tool，要么已归还。
	ErrWebNotFound  = errors.New("web 句柄不存在:请先调用 open_web_tool 借一张网,或它已被 close_web_tool 归还")
	errBrokerClosed = errors.New("web broker 已关闭")
)

// brokeredWeb 是 broker 名下"仍被占用"的一张网，附带最近使用时间以支持空闲兜底回收。
// lastUsed 用 atomic 存取，保证在取消租约（回收）时不加锁也能安全读取。
type brokeredWeb struct {
	w        *web.Web
	lastUsed atomic.Int64 // unix 纳秒
}

// WebBroker 以"网(web)"为基本单位、实现"无状态核心 + 显式状态句柄"的注册表。
//
// 设计要点：
//   - 一个用户（对话/会话）通过 open_web_tool 借一张网，拿到唯一 web_id 作为句柄；
//   - 之后所有浏览器动作携带该 web_id，经 Resolve 定位到同一张网——状态由句柄显式传递；
//   - 会话结束由调用方显式 close_web_tool 归还；另以空闲 TTL 兜底回收，防句柄泄漏
//     （对应"状态是缓存，而非真相来源"：丢失不过多一次往返，可由句柄重建）。
//   - 并发上限由底层 spider 池（webs channel）天然约束，池满时 AcquireCtx 阻塞。
type WebBroker struct {
	sp        spider.Spider
	opTimeout time.Duration
	idleTTL   time.Duration

	mu     sync.Mutex
	live   map[string]*brokeredWeb // web_id -> 网
	closed atomic.Bool
	done   chan struct{}
}

// NewWebBroker 创建以 web 为单位的句柄 broker。
//   - sp：底层蜘蛛/网池（负责借出与归还/销毁重建）；
//   - opTimeout：每次浏览器动作的通用超时；传 0 时默认 30s；
//   - idleTTL：句柄空闲多久自动回收；传 0 时默认 10 分钟。
func NewWebBroker(sp spider.Spider, opTimeout, idleTTL time.Duration) *WebBroker {
	if opTimeout <= 0 {
		opTimeout = 30 * time.Second
	}
	if idleTTL <= 0 {
		idleTTL = 10 * time.Minute
	}
	b := &WebBroker{
		sp:        sp,
		opTimeout: opTimeout,
		idleTTL:   idleTTL,
		live:      make(map[string]*brokeredWeb),
		done:      make(chan struct{}),
	}
	go b.sweep()
	return b
}

func newWebID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

// Open 从空闲池借一张网并分配 web_id 句柄。池满时按 ctx 阻塞/取消。
func (b *WebBroker) Open(ctx context.Context) (string, error) {
	w, err := b.sp.AcquireCtx(ctx)
	if err != nil {
		return "", err
	}
	id, err := newWebID()
	if err != nil {
		_ = b.sp.Release(w)
		return "", err
	}
	bw := &brokeredWeb{w: w}
	bw.lastUsed.Store(time.Now().UnixNano())

	b.mu.Lock()
	if b.closed.Load() {
		b.mu.Unlock()
		_ = b.sp.Release(w)
		return "", errBrokerClosed
	}
	b.live[id] = bw
	b.mu.Unlock()

	log.Printf("[web] 借网成功 web_id=%s inUse=%d", id, b.len())
	return id, nil
}

// Resolve 实现 webSource：按 web_id 取回网并刷新最近使用时间。
func (b *WebBroker) Resolve(_ context.Context, webID string) (*web.Web, error) {
	b.mu.Lock()
	bw, ok := b.live[webID]
	if ok {
		bw.lastUsed.Store(time.Now().UnixNano())
	}
	b.mu.Unlock()
	if !ok {
		return nil, ErrWebNotFound
	}
	return bw.w, nil
}

// Close 把 web_id 对应的网归还空闲池（幂等：已归还/不存在则返回 ErrWebNotFound）。
// 归还后 spider.Release 会自动销毁重建 Broken 的网。
func (b *WebBroker) Close(webID string) error {
	needRelease := false
	var w *web.Web

	b.mu.Lock()
	if bw, ok := b.live[webID]; ok {
		w = bw.w
		delete(b.live, webID)
		needRelease = true
	}
	b.mu.Unlock()

	if !needRelease {
		return ErrWebNotFound
	}
	if err := b.sp.Release(w); err != nil {
		return err
	}
	log.Printf("[web] 已归还 web_id=%s inUse=%d", webID, b.len())
	return nil
}

// List 返回当前仍被占用（未归还）的 web_id 列表，方便模型管理自己打开的资源。
func (b *WebBroker) List() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	ids := make([]string, 0, len(b.live))
	for id := range b.live {
		ids = append(ids, id)
	}
	return ids
}

// OpTimeout 实现 webSource。
func (b *WebBroker) OpTimeout() time.Duration { return b.opTimeout }

// len 返回当前在途（占用）张数。
func (b *WebBroker) len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.live)
}

// sweep 定期回收超过 idleTTL 未使用的句柄（兜底：调用方没显式 close 也不泄漏）。
func (b *WebBroker) sweep() {
	ticker := time.NewTicker(b.idleTTL)
	defer ticker.Stop()
	for {
		select {
		case <-b.done:
			return
		case <-ticker.C:
			now := time.Now().UnixNano()
			b.mu.Lock()
			var expired []*brokeredWeb
			for id, bw := range b.live {
				if now-bw.lastUsed.Load() >= int64(b.idleTTL) {
					expired = append(expired, bw)
					delete(b.live, id)
				}
			}
			b.mu.Unlock()
			for _, bw := range expired {
				if err := b.sp.Release(bw.w); err != nil {
					log.Printf("[web] 空闲回收归还失败: %v", err)
				}
			}
			if len(expired) > 0 {
				log.Printf("[web] 空闲回收 %d 张网, inUse=%d", len(expired), b.len())
			}
		}
	}
}

// Shutdown 停止兜底回收并归还所有在途网。流程结束后 broker 不可再用。
func (b *WebBroker) Shutdown() {
	if !b.closed.CompareAndSwap(false, true) {
		return
	}
	close(b.done)

	b.mu.Lock()
	for id, bw := range b.live {
		if err := b.sp.Release(bw.w); err != nil {
			log.Printf("[web] 关停归还 %s 失败: %v", id, err)
		}
		delete(b.live, id)
	}
	b.mu.Unlock()
	log.Printf("[web] broker 已关停, 全部网已归还")
}

// NewBrokerHTTPHandler 以"显式状态句柄"模式把 MCP 服务器经 HTTP（Streamable 传输）暴露为
// 标准 http.Handler：模型端先调 open_web_tool 借一张网拿 web_id，再用同一 web_id 做后续操作，
// 结束调 close_web_tool 归还。传输层保持无状态（Stateless），状态只收敛在 broker 的句柄表里，
// 因而天然可水平扩展——请求落到任意实例，只要 web_id 解析得到。
func NewBrokerHTTPHandler(b *WebBroker, implementation *mcp.Implementation, opts *mcp.ServerOptions) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return NewBrokerMCPServer(b, implementation, opts)
	}, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
	})
}

// RunBrokerHTTPServer 在 addr 上以显式状态句柄模式提供 MCP 工具服务。
// 与 RunHTTPServer 的"一网多用"不同，本入口是"一个用户一张网 + web_id 句柄"。
func RunBrokerHTTPServer(b *WebBroker, addr string, implementation *mcp.Implementation, opts *mcp.ServerOptions) error {
	srv := &http.Server{
		Addr:    addr,
		Handler: NewBrokerHTTPHandler(b, implementation, opts),
	}
	log.Printf("[MCP HTTP] 显式句柄服务已启动: http://127.0.0.1%s （先 open_web_tool 借网，再携带 web_id 操作，close_web_tool 归还）", addr)
	return srv.ListenAndServe()
}
