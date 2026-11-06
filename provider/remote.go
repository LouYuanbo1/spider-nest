package provider

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/LouYuanbo1/spider-nest/cloud/client"
)

var _ WebProvider = (*RemoteProvider)(nil)

// RemoteProvider 是 WebProvider 契约的"云端编排后端"实体实现（Route B，动作级 RPC）。
//
// 与传统本地 Nest/Spider provider 不同，本实现不做任何浏览器进程，只作为"会话侧调度口"：
//   - 它攥着一组云端节点（cloud/agent）的 RPC 连接；
//   - AcquireWeb 时按各节点 Stats() 的余量挑一台有余量的节点，把它的活页借来，
//     包装成一纸"令牌 + 远端 Page 代理"的 WebHandle 交还会话层；
//   - 会话层之后的每个动作经由 WebHandle.Web（web.Page 的 RPC 代理）拍到节点本地就地执行。
//
// 编排粒度：放置决策收敛在本实现内部，与工具/会话层解耦——需要抽离成独立控制面进程时，
// 把 pickNode 换成向注册中心查询即可，其余零改动。
// （有意取舍：本项目刻意不内置 gRPC / 独立控制面；上生产时可把本端的静态节点地址列表
//
//	换成注册中心动态发现、并按需把 cloud/proto 的 net/rpc+gob 换为 gRPC，见其边界声明。）
//
// 生命周期保障：为所有在途令牌维护一条后台心跳线，定期续期远端租约，抵消防
// 服务侧与云端节点两侧 reaper 的失活回收；Release/Close 幂等归还。
type RemoteProvider struct {
	conns []*client.Conn // 所有云端节点连接
	// 心跳节奏
	hbInterval time.Duration

	mu       sync.Mutex
	outl     map[string]*client.Conn // token -> 持有它的节点连接（在途租约）
	draining bool

	done chan struct{}
}

// NewRemoteProvider 用一组节点地址构造远程 provider。拨号失败不致命：该节点本轮跳过
// （Acquire 时仍会按名单重拨，见连接就绪列表）。hbInterval<=0 时默认 30s。
func NewRemoteProvider(addrs []string, hbInterval time.Duration) *RemoteProvider {
	if hbInterval <= 0 {
		hbInterval = 30 * time.Second
	}
	p := &RemoteProvider{
		hbInterval: hbInterval,
		outl:       make(map[string]*client.Conn),
		done:       make(chan struct{}),
	}
	for _, addr := range addrs {
		if c, err := client.Dial(addr); err != nil {
			log.Printf("[provider] 连接节点 %s 失败(该节点本轮不参与调度): %v", addr, err)
		} else {
			p.conns = append(p.conns, c)
		}
	}
	go p.heartbeatLoop()
	return p
}

// connectionsReady 返回当前参与调度的节点连接列表（不含拨号失败的地址）。
// 节点中途掉线时，其 Stat 调用会失败从而在放置时被跳过；重连同态交由后续迭代做。
func (p *RemoteProvider) connectionsReady() []*client.Conn {
	return p.conns
}

// pickNode 挑一台"有余量且可服务"的节点，以 Available 最多为优（负载均衡倾向）。
//   - 有余量节点 → 返回该连接；
//   - 节点全部失联/不可服务 → ErrUnavailable；
//   - 有可服务节点但均已满 → ErrPoolExhausted。
//
// Sticky（强身份粘性）是放置路由键；本层暂不细粒度路由到具体节点，按余量全局择优。
func (p *RemoteProvider) pickNode() (*client.Conn, error) {
	var anyReady bool
	var best *client.Conn
	bestAvail := -1
	for _, c := range p.connectionsReady() {
		st, err := c.Stat()
		if err != nil {
			continue // 节点失联:本轮跳过
		}
		if !st.Ready {
			continue // 不可服务(排空/关停中):跳过
		}
		anyReady = true
		if st.Available <= 0 {
			continue // 已满,不选但保留"池满"判定
		}
		if st.Available > bestAvail {
			best, bestAvail = c, st.Available
		}
	}
	if best != nil {
		return best, nil
	}
	if anyReady {
		return nil, ErrPoolExhausted // 有可服务节点但余量耗尽
	}
	return nil, ErrUnavailable
}

// AcquireWeb 按余量挑一台节点,在其上借一张活页并返回"令牌+远端Page代理"的 WebHandle。
func (p *RemoteProvider) AcquireWeb(ctx context.Context, opts AcquireOptions) (*WebHandle, error) {
	p.mu.Lock()
	if p.draining {
		p.mu.Unlock()
		return nil, ErrUnavailable
	}
	p.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c, err := p.pickNode()
	if err != nil {
		return nil, err
	}
	token, err := c.Open(ctx, opts.Sticky, opts.KeepHot)
	if err != nil {
		return nil, err
	}
	// 登记在途,交由心跳线养护; Release 时注销。
	p.mu.Lock()
	p.outl[token] = c
	p.mu.Unlock()

	var once sync.Once
	var releaseErr error
	release := func() error {
		once.Do(func() {
			p.mu.Lock()
			delete(p.outl, token)
			p.mu.Unlock()
			releaseErr = c.Release(token)
		})
		return releaseErr
	}
	return &WebHandle{Token: token, Web: c.RemotePage(token), Release: release}, nil
}

// Heartbeat 给指定在途令牌续期（远端后端必需实现；本实现直接向持有节点续期）。
func (p *RemoteProvider) Heartbeat(ctx context.Context, token string) error {
	p.mu.Lock()
	c, ok := p.outl[token]
	p.mu.Unlock()
	if !ok {
		return ErrTokenUnknown
	}
	return c.Heartbeat(token)
}

// Stats 汇总所有可服务节点的池容量:Capacity=Σ容量, InUse=Σ在途, Available=Σ余量,
// Ready=尚有可服务节点。
func (p *RemoteProvider) Stats() PoolState {
	st := PoolState{}
	seenReady := false
	for _, c := range p.connectionsReady() {
		s, err := c.Stat()
		if err != nil {
			continue
		}
		st.Capacity += s.Capacity
		st.InUse += s.InUse
		st.Available += s.Available
		if s.Ready {
			seenReady = true
		}
	}
	st.Ready = seenReady
	return st
}

// Drain 置排空位拒绝新租约,并等在途租约全部归还/被节点回收,直到 ctx 到期。
func (p *RemoteProvider) Drain(ctx context.Context) error {
	p.mu.Lock()
	p.draining = true
	p.mu.Unlock()
	for {
		p.mu.Lock()
		n := len(p.outl)
		p.mu.Unlock()
		if n == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("resource: drain 超时,仍有在途租约")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Close 停止心跳线,归还全部在途租约并关闭节点连接;幂等。
func (p *RemoteProvider) Close() error {
	p.mu.Lock()
	if p.draining {
		p.mu.Unlock()
		return nil
	}
	p.draining = true
	tokens := make(map[*client.Conn][]string, len(p.outl))
	for tok, c := range p.outl {
		tokens[c] = append(tokens[c], tok)
	}
	p.outl = map[string]*client.Conn{}
	conns := append([]*client.Conn(nil), p.conns...)
	p.conns = nil
	p.mu.Unlock()

	select {
	case <-p.done:
	default:
		close(p.done)
	}
	for c, toks := range tokens {
		for _, tok := range toks {
			_ = c.Release(tok)
		}
		_ = c.Close()
	}
	for _, c := range conns {
		_ = c.Close()
	}
	return nil
}

// heartbeatLoop 定期给所有在途 token 续期,防节点 reaper 误回收。
func (p *RemoteProvider) heartbeatLoop() {
	t := time.NewTicker(p.hbInterval)
	defer t.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-t.C:
			p.mu.Lock()
			snap := make(map[string]*client.Conn, len(p.outl))
			for tok, c := range p.outl {
				snap[tok] = c
			}
			p.mu.Unlock()
			for tok, c := range snap {
				if err := c.Heartbeat(tok); err != nil {
					log.Printf("[provider] 心跳失败 token=%s: %v", tok, err)
				}
			}
		}
	}
}
