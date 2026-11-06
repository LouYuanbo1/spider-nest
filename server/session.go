package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LouYuanbo1/spider-nest/config"
	"github.com/LouYuanbo1/spider-nest/nest"
	"github.com/LouYuanbo1/spider-nest/web"
)

var (
	// ErrWebNotFound 会话句柄不存在：要么没先 Open，要么它已被 Close 归还。
	ErrWebNotFound = errors.New("web 会话不存在:请先 Open 借一张网,或它已被 Close 归还")
	// errSessionClosed 会话管理器已关闭，拒绝再接管租约。
	errSessionClosed = errors.New("web 会话管理器已关闭")
)

// 冷热二态：web_id 的底层资源要么是一片"热持有的活页"，要么只是一份"冷检查点"。
const (
	stateCold int32 = iota // 仅持有检查点：不占浏览器，可随时重水化(重开活页)
	stateHot               // 持有活页：占着一个浏览器，操作零重建成本
)

// OpenOptions 决定一次 Open 的租约语义。
//
//	Pinned 是否钉死（保活）：pinned 的会话不参与"空闲自动冷提交"，会持续持有活页；
//	        适合登录搭建、超长爬取等"状态必须留在页上"的 state-heavy 流程。
//	Sticky 强身份粘性选择：透传给底层 provider 做放置路由（固定 IP / 登录态 / 代理池）。
type OpenOptions struct {
	Pinned bool
	Sticky map[string]string
}

// webSession 是"一个会话"：web_id 是稳定身份，其底层资源可热可冷、可替换。
//
// 关键革新：web_id 不再绑定某个进程内活页（旧实现的 live[id]→*web.Web），而只
// 绑定一个"会话"。活页只是会话当前借用的底层资源——按需租、按需还。这与"会话是
// 形、浏览器是租"的云端编排模型一致：进程重启/滚动发布后，会话仍能凭检查点重水化。
//
// 并发：mu 串行化本会话的"热↔冷"切换以及冷→热的网络重水化，确保同一 web_id 的
// 操作有序、且重水化不会叠叠发动多片页。
type webSession struct {
	id    string
	state atomic.Int32 // sessionState

	mu     sync.Mutex      // 序列化热冷切换 + 重水化
	handle *WebHandle      // 非 nil = 正在热持有（含 Release 归还闭包）
	cp     *web.Checkpoint // 非 nil = 冷却后待重放
	pinned atomic.Bool     // 保活：不参与自动冷提交

	lastUsed atomic.Int64 // unix 纳秒，最近一次被使用
	openedAt time.Time
	sticky   map[string]string
}

func (s *webSession) markUse() { s.lastUsed.Store(time.Now().UnixNano()) }

// WebSessionManager 以"会话(web)"为基本单位，实现"无状态核心 + 显式状态句柄"的
// 冷热两态会话管理器。
//
// 设计要点（承接 webProvider.go 的接缝）：
//   - 一个用户（对话/会话）通过 Open 借一个"会话"，拿到唯一 web_id 作为稳定身份；
//     之后所有浏览器动作携带该 web_id，经 Resolve 解析到同一会话——状态由句柄显式传递；
//   - 底层以"两态出租"驱动资源复用：活跃操作用热页（零重建、状态真），空闲超 hotTTL
//     自动冷提交（捕获检查点→归还浏览器→只占身份），再次 Resolve 时按检查点重水化。
//     这让"LLM 思考/idle 期间"不再白白占着一个 Chromium 进程，把节点的并发密度
//     提高一个数量级（冷态只为并发让路，热态只为连续性保真）；
//   - Pinned 会话例外：明示要持续持有活页/长任务保活，不参与自动冷提交；
//   - 会话结束显式 Close 归还；冷态会话超过 idleTTL 仍无使用则被彻底回收（连检查点
//     一并丢弃），防 web_id 无限累积。
//
// 传输层适配（MCP/HTTP）不在此处，见 mcp.go / http.go。
type WebSessionManager struct {
	provider WebProvider
	hotTTL   time.Duration // 热态宽限：空闲超过则冷提交
	idleTTL  time.Duration // 冷态会话最长存活：超过则彻底回收
	// callTimeout 每一次工具调用的总超时（传输层 wrapTool 兜底用）。
	callTimeout time.Duration

	mu     sync.Mutex
	live   map[string]*webSession
	closed atomic.Bool
	done   chan struct{}
}

// NewWebSessionManager 创建以"会话"为单位的冷热两态管理器。
//   - cfg：Nest 池容量（MaxSpiders=并发上限，见 NestConfig 注释）；
//   - create：惰性创建一只蜘蛛（每浏览器实例）的工厂；
//   - callTimeout：单次工具调用总超时；<=0 默认 30s；
//   - hotTTL：热态空闲多少后冷提交；<=0 默认 60s；
//   - idleTTL：冷态会话无使用多久彻底回收；<=0 默认 10 分钟。
func NewWebSessionManager[T any, S nest.NestSpider[T]](cfg config.NestConfig, create func() (S, error), callTimeout, hotTTL, idleTTL time.Duration) (*WebSessionManager, error) {
	if callTimeout <= 0 {
		callTimeout = 30 * time.Second
	}
	if hotTTL <= 0 {
		hotTTL = 60 * time.Second
	}
	if idleTTL <= 0 {
		idleTTL = 10 * time.Minute
	}
	n, err := nest.NewOrchardFromConfig(cfg, create)
	if err != nil {
		return nil, err
	}
	m := &WebSessionManager{
		provider:    NewNestProvider(n),
		callTimeout: callTimeout,
		hotTTL:      hotTTL,
		idleTTL:     idleTTL,
		live:        make(map[string]*webSession),
		done:        make(chan struct{}),
	}
	go m.sweep()
	return m, nil
}

func newWebID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

// Open 借一个会话并分配 web_id（默认租约：非 pinned、无粘性）。
func (m *WebSessionManager) Open(ctx context.Context) (string, error) {
	return m.OpenWithOptions(ctx, OpenOptions{})
}

// OpenWithOptions 按选项借一个会话：拿令牌 → 分配 web_id → 建热态租约。
// 池满时按 ctx 阻塞/取消；排空/关闭时返回相应错误。
func (m *WebSessionManager) OpenWithOptions(ctx context.Context, opts OpenOptions) (string, error) {
	handle, err := m.provider.AcquireWeb(ctx, AcquireOptions{Sticky: opts.Sticky, KeepHot: opts.Pinned})
	if err != nil {
		return "", err
	}
	id, err := newWebID()
	if err != nil {
		_ = handle.Release()
		return "", err
	}
	s := &webSession{
		id:     id,
		handle: handle,
		sticky: opts.Sticky,
	}
	s.state.Store(stateHot)
	s.markUse()
	if opts.Pinned {
		s.pinned.Store(true)
	}

	m.mu.Lock()
	if m.closed.Load() {
		m.mu.Unlock()
		_ = handle.Release()
		return "", errSessionClosed
	}
	m.live[id] = s
	m.mu.Unlock()

	log.Printf("[session] 借网成功 web_id=%s inUse=%d", id, m.inUse())
	return id, nil
}

// Resolve 按 web_id 取回"能执行操作的活页"。
// 热态直接返回并刷新使用时间；冷态则按检查点重水化（重开一片活页）后再返回。
// 两个并行 Resolve 会在会话级互斥锁上排队，避免同一会话叠开多片页。
func (m *WebSessionManager) Resolve(ctx context.Context, webID string) (*web.Web, error) {
	m.mu.Lock()
	s, ok := m.live[webID]
	m.mu.Unlock()
	if !ok {
		return nil, ErrWebNotFound
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.handle != nil {
		s.markUse()
		return s.handle.Web, nil
	}
	return m.hydrate(ctx, s)
}

// hydrate 冷→热：从 provider 重借一片页，重放检查点，切回热态。
// 调用方必须已持有 s.mu。
func (m *WebSessionManager) hydrate(ctx context.Context, s *webSession) (*web.Web, error) {
	if s.cp == nil {
		return nil, ErrWebNotFound
	}
	handle, err := m.provider.AcquireWeb(ctx, AcquireOptions{Sticky: s.sticky, KeepHot: true})
	if err != nil {
		return nil, err
	}
	if err := handle.Web.RestoreCheckpoint(ctx, s.cp); err != nil {
		_ = handle.Release()
		log.Printf("[session] 重水化失败 web_id=%s: %v", s.id, err)
		return nil, err
	}
	s.handle = handle
	s.cp = nil
	s.state.Store(stateHot)
	s.markUse()
	log.Printf("[session] 重水化恢复 web_id=%s inUse=%d", s.id, m.inUse())
	return handle.Web, nil
}

// Pin 钉住/取消钉住一个会话。钉住后不参与自动冷提交；钉住一个冷态会话会立即重水化，
// 使错误日志、后续操作立即可用。取消钉住不强制提交，由下一轮 hotTTL 决定。
func (m *WebSessionManager) Pin(webID string, v bool) error {
	m.mu.Lock()
	s, ok := m.live[webID]
	m.mu.Unlock()
	if !ok {
		return ErrWebNotFound
	}
	if v {
		s.pinned.Store(true)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.handle == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_, err := m.hydrate(ctx, s)
			return err
		}
		return nil
	}
	s.pinned.Store(false)
	return nil
}

// Close 把 web_id 对应的会话归还（幂等；已归还/不存在返回 ErrWebNotFound）。
// 热态直接归还活页；冷态释放检查点。底层若发现页失效会自动销毁重建。
func (m *WebSessionManager) Close(webID string) error {
	m.mu.Lock()
	s, ok := m.live[webID]
	if ok {
		delete(m.live, webID)
	}
	m.mu.Unlock()
	if !ok {
		return ErrWebNotFound
	}

	s.mu.Lock()
	handle := s.handle
	s.handle = nil
	s.cp = nil
	s.mu.Unlock()

	if handle != nil {
		if err := handle.Release(); err != nil {
			return err
		}
	}
	log.Printf("[session] 已归还 web_id=%s inUse=%d", webID, m.inUse())
	return nil
}

// List 返回当前仍占用（未归还）的 web_id 列表。
func (m *WebSessionManager) List() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.live))
	for id := range m.live {
		ids = append(ids, id)
	}
	return ids
}

// Stats 委托给底层 provider，供外部/调度读取池容量与在途量。
func (m *WebSessionManager) Stats() PoolState { return m.provider.Stats() }

// CallTimeout 返回每一次工具调用的总超时（传输层 wrapTool 用）。
func (m *WebSessionManager) CallTimeout() time.Duration { return m.callTimeout }

// inUse 返回当前在途（热持有活页）的会话张数。
func (m *WebSessionManager) inUse() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, s := range m.live {
		if s.handle != nil {
			n++
		}
	}
	return n
}

// sweep 两档异步回收：
//   - hotTick：扫描热态空闲超 hotTTL 的会话→冷提交（捕获检查点 + 归还浏览器）；
//   - coldTick：扫描冷态会话无使用超 idleTTL 的→彻底删除（连检查点一起），防 web_id 累积。
func (m *WebSessionManager) sweep() {
	// 热档轮询粒度取小值以便及时回收闲置浏览器（不因 hotTTL 偏大而推迟太多）。
	hotTick := m.hotTTL / 2
	if hotTick < time.Second {
		hotTick = time.Second
	}
	hotTicker := time.NewTicker(hotTick)
	coldTicker := time.NewTicker(m.idleTTL)
	defer hotTicker.Stop()
	defer coldTicker.Stop()

	for {
		select {
		case <-m.done:
			return
		case <-hotTicker.C:
			m.commitIdleHot()
		case <-coldTicker.C:
			m.expireCold()
		}
	}
}

// commitIdleHot 冷提交所有"未钉住 + 热态 + 空闲超 hotTTL"的会话。
func (m *WebSessionManager) commitIdleHot() {
	now := time.Now().UnixNano()
	m.mu.Lock()
	var cands []*webSession
	for _, s := range m.live {
		if s.state.Load() == stateHot && !s.pinned.Load() &&
			now-s.lastUsed.Load() >= int64(m.hotTTL) {
			cands = append(cands, s)
		}
	}
	m.mu.Unlock()

	for _, s := range cands {
		m.commitOne(s)
	}
}

// commitOne 对一个热会话做冷提交：捕获检查点→释放浏览器→切冷态。
// 捕获失败时降级：直接把页归还、仍切冷态（仅保留身份，放弃检查点）。
func (m *WebSessionManager) commitOne(s *webSession) {
	s.mu.Lock()
	if s.handle == nil {
		s.mu.Unlock() // 已被并发处理（如 Close）
		return
	}
	handle := s.handle

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	cp, err := handle.Web.CaptureCheckpoint(ctx)
	cancel()

	if err != nil {
		log.Printf("[session] 冷提交捕获失败 web_id=%s, 直接归还: %v", s.id, err)
		s.handle = nil
		s.cp = nil // 捕获失败：不保留旧检查点，冷态仅占身份
		s.state.Store(stateCold)
		s.mu.Unlock()
		if relErr := handle.Release(); relErr != nil {
			log.Printf("[session] 冷提交归还失败 web_id=%s: %v", s.id, relErr)
		}
		return
	}

	s.cp = cp
	s.handle = nil
	s.state.Store(stateCold)
	s.mu.Unlock()

	if relErr := handle.Release(); relErr != nil {
		log.Printf("[session] 冷提交归还失败 web_id=%s: %v", s.id, relErr)
	}
	log.Printf("[session] 冷提交 web_id=%s -> cold inUse=%d", s.id, m.inUse())
}

// expireCold 回收"冷态 + 无使用超 idleTTL"的会话（连检查点一起丢弃）。
// 冷态不占浏览器，理论上可长期存续；此处仅兜底防 web_id/检查点无限累积。
func (m *WebSessionManager) expireCold() {
	now := time.Now().UnixNano()
	m.mu.Lock()
	var ids []string
	for id, s := range m.live {
		if s.state.Load() == stateCold && now-s.lastUsed.Load() >= int64(m.idleTTL) {
			ids = append(ids, id)
			delete(m.live, id)
		}
	}
	m.mu.Unlock()
	if len(ids) > 0 {
		log.Printf("[session] 冷回收 %d 个已过期会话", len(ids))
	}
}

// Shutdown 停回收 → 归还全部在途热租约 → 排空 → 关闭整个 provider。
// 流程结束后管理器不可再用。
func (m *WebSessionManager) Shutdown() {
	if !m.closed.CompareAndSwap(false, true) {
		return
	}
	close(m.done)

	m.mu.Lock()
	sessions := make([]*webSession, 0, len(m.live))
	for _, s := range m.live {
		sessions = append(sessions, s)
	}
	m.live = make(map[string]*webSession)
	m.mu.Unlock()

	for _, s := range sessions {
		s.mu.Lock()
		if s.handle != nil {
			_ = s.handle.Release()
			s.handle = nil
		}
		s.mu.Unlock()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := m.provider.Drain(ctx); err != nil {
		log.Printf("[session] 关停 drain 失败: %v", err)
	}
	if err := m.provider.Close(); err != nil {
		log.Printf("[session] 关停浏览器池失败: %v", err)
	}
	log.Printf("[session] manager 已关停, 全部会话已归还")
}
