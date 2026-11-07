// Package session 承载"会话核心"：把资源池上的一纸租约（provider.WebHandle）提升为
// 一个以 web_id 为稳定身份的会话簿记。它只依赖 provider（资源契约），不关心
// 后端是 Nest 多蜘蛛池还是单个蜘蛛。
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LouYuanbo1/spider-nest/provider"
	"github.com/LouYuanbo1/spider-nest/web"
)

var (
	// ErrWebNotFound 会话句柄不存在：要么没先 Open，要么它已被 Close 归还。
	ErrWebNotFound = errors.New("web 会话不存在:请先 Open 借一张网,或它已被 Close 归还")
	// errSessionClosed 会话管理器已关闭，拒绝再接管租约。
	errSessionClosed = errors.New("web 会话管理器已关闭")
)

// entry 是"一个会话"：web_id 是稳定身份，其底层资源是一片持有的活页。
//
// 个人使用的 MCP 服务器随 Open/Close 借还。mu 串行化本会话的归还与并发操作。
type entry struct {
	id     string
	mu     sync.Mutex          // 串行化归还与操作
	handle *provider.WebHandle // 非 nil = 正持有（含 Release 归还闭包）
}

// Manager 以"会话(web)"为基本单位，实现"无状态核心 + 显式状态句柄"的会话管理器。
//
// 设计要点：
//   - 一个用户（对话/会话）通过 Open 借一个"会话"，拿到唯一 web_id 作为稳定身份；
//     之后所有浏览器动作携带该 web_id，经 Resolve 解析到同一会话——状态由句柄显式传递；
//   - 会话保持持有活页（个人 MCP 场景浏览器常驻）；
//   - 会话结束显式 Close 归还；进程退出经 Shutdown 兜底归还全部并关停 provider。
//
// 底层资源来源以 provider.WebProvider 注入，可热替换：本地 Nest 或单个 Spider
// 实现同一契约即可透明切换，本机核心逻辑零改动。
type Manager struct {
	pvd provider.WebProvider
	// callTimeout 每一次工具调用的总超时（传输层 wrapTool 兜底用）。
	callTimeout time.Duration

	mu       sync.Mutex
	sessions map[string]*entry
	closed   atomic.Bool
}

// New 用给定的 provider 构造会话管理器。
//   - provider：会话底层资源来源（Nest / 单蛛）；
//   - callTimeout：单次工具调用总超时；<=0 默认 30s。
func New(pvd provider.WebProvider, callTimeout time.Duration) (*Manager, error) {
	if pvd == nil {
		return nil, errors.New("session: provider 不能为空")
	}
	if callTimeout <= 0 {
		callTimeout = 30 * time.Second
	}
	m := &Manager{
		pvd:         pvd,
		callTimeout: callTimeout,
		sessions:    make(map[string]*entry),
	}
	return m, nil
}

func newWebID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

// Open 借一个会话并分配 web_id，建立租约。
// 池满时按 ctx 阻塞/取消；排空/关闭时返回相应错误。
func (m *Manager) Open(ctx context.Context) (string, error) {
	handle, err := m.pvd.AcquireWeb(ctx)
	if err != nil {
		return "", err
	}
	id, err := newWebID()
	if err != nil {
		_ = handle.Release()
		return "", err
	}
	s := &entry{id: id, handle: handle}

	m.mu.Lock()
	if m.closed.Load() {
		m.mu.Unlock()
		_ = handle.Release()
		return "", errSessionClosed
	}
	m.sessions[id] = s
	m.mu.Unlock()

	log.Printf("[session] 借网成功 web_id=%s inUse=%d", id, m.inUse())
	return id, nil
}

// Resolve 按 web_id 取回"能执行操作的活页"。
// 直接返回活页；两个并行 Resolve 会在会话级互斥锁上排队。
func (m *Manager) Resolve(ctx context.Context, webID string) (web.Web, error) {
	m.mu.Lock()
	s, ok := m.sessions[webID]
	m.mu.Unlock()
	if !ok {
		return nil, ErrWebNotFound
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.handle != nil {
		return s.handle.Web, nil
	}
	return nil, ErrWebNotFound
}

// Close 把 web_id 对应的会话归还（幂等；已归还/不存在返回 ErrWebNotFound）。
// 直接归还活页。底层若发现页失效会自动销毁重建。
func (m *Manager) Close(webID string) error {
	m.mu.Lock()
	s, ok := m.sessions[webID]
	if ok {
		delete(m.sessions, webID)
	}
	m.mu.Unlock()
	if !ok {
		return ErrWebNotFound
	}

	s.mu.Lock()
	handle := s.handle
	s.handle = nil
	s.mu.Unlock()

	if handle != nil {
		if err := handle.Release(); err != nil {
			return err
		}
	}
	log.Printf("[session] 已归还 web_id=%s inUse=%d", webID, m.inUse())
	return nil
}

// CallTimeout 返回每一次工具调用的总超时（传输层 wrapTool 用）。
func (m *Manager) CallTimeout() time.Duration { return m.callTimeout }

// inUse 返回当前在途（已借出持有活页）的会话张数。
func (m *Manager) inUse() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

// Shutdown 归还全部在途热租约 → 排空 → 关闭整个 provider。
// 流程结束后管理器不可再用。
func (m *Manager) Shutdown() {
	if !m.closed.CompareAndSwap(false, true) {
		return
	}

	m.mu.Lock()
	sessions := make([]*entry, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.sessions = make(map[string]*entry)
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
	if err := m.pvd.Drain(ctx); err != nil {
		log.Printf("[session] 关停 drain 失败: %v", err)
	}
	if err := m.pvd.Close(); err != nil {
		log.Printf("[session] 关停浏览器池失败: %v", err)
	}
	log.Printf("[session] manager 已关停, 全部会话已归还")
}
