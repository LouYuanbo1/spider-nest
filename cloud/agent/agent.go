// Package agent 是"云端节点"侧的 RPC 执行器（Route B 的服务器端）。
//
// 每个 agent 进程就是一台云节点：它内部攥着一个本地的 provider.WebProvider
// （常为 Nest/Spider 池），对外仅暴露一层很薄的 RPC——借/跑/抓放检查点/还/续期。
// 会话侧（provider.remote）把动作/检查点"拍"过来，本节点的本地活页就地执行，
// 这正是"节点极薄、会话密度最高"的落点：节点不感知会话状态机，只管一片片活页。
//
// 生命周期保障：
//   - 每张租约凭 token 寻址（Open 发放，Run/Capture/Release 携带）；
//   - 后台 reaper 兜底回收"长期未续期"的租约，防止客户端崩溃导致的占位泄漏；
//     会话侧的 RemoteProvider 会周期心跳（Heartbeat）把在途 token 养着，见 provider/remote.go。
package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/rpc"
	"os"
	"sync"
	"time"

	"github.com/LouYuanbo1/spider-nest/cloud/proto"
	"github.com/LouYuanbo1/spider-nest/provider"
	"github.com/LouYuanbo1/spider-nest/web"
)

// 默认时序（均可通过 Option 调）。
const (
	defAcquireTimeout = 15 * time.Second // 每次借活的响应上限,防耗尽时永久阻塞
	defLeaseTTL       = 2 * time.Minute  // 租约未被续期则视为失活、回收
	defReaperTick     = 30 * time.Second // 回收器扫描周期
)

// Config 节点（agent）运行参数。
type Config struct {
	AcquireTimeout time.Duration // 借活超时
	LeaseTTL       time.Duration // 租约失活阈值
	ReaperTick     time.Duration // 兜底回收扫描周期
}

// lease 是节点上的一张小租约：一张可执行动作的活页 + 归还闭包 + 最近活跃时间。
type lease struct {
	page     web.Web
	release  func() error
	lastSeen time.Time
}

// Node 是云端节点的 RPC 面。方法签名遵循 net/rpc 约定（导出、arg 与 reply 均可序列化）。
type Node struct {
	pvd  provider.WebProvider // 节点内部资源来源（本地 Nest/Spider 池）
	conf Config

	mu     sync.Mutex
	leases map[string]*lease // token -> 持租约

	done chan struct{}
}

// New 构造一台持有一个本地 provider 的云端节点，并启动租约兜底回收器。
func New(pvd provider.WebProvider, conf Config) *Node {
	// 补默认值
	if conf.AcquireTimeout <= 0 {
		conf.AcquireTimeout = defAcquireTimeout
	}
	if conf.LeaseTTL <= 0 {
		conf.LeaseTTL = defLeaseTTL
	}
	if conf.ReaperTick <= 0 {
		conf.ReaperTick = defReaperTick
	}
	n := &Node{pvd: pvd, conf: conf, leases: make(map[string]*lease), done: make(chan struct{})}
	go n.reap()
	return n
}

// Close 停止回收器并关闭底层 provider，释放节点全部资源。
func (n *Node) Close() error {
	close(n.done)
	return n.pvd.Close()
}

// Serve 在给定监听器上提供本节点的 RPC 服务；阻塞直到监听器关闭。
func (n *Node) Serve(lis net.Listener) error {
	srv := rpc.NewServer()
	if err := srv.RegisterName("Node", n); err != nil {
		return err
	}
	log.Printf("[agent] 云端节点 RPC 就绪 %s", lis.Addr())
	for {
		conn, err := lis.Accept()
		if err != nil {
			return err
		}
		go srv.ServeConn(conn)
	}
}

// token 生成一个随机令牌作为租约的跨 RPC 寻址身份。
func newToken() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("tok-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}

func (n *Node) touch(token string) {
	n.mu.Lock()
	if l, ok := n.leases[token]; ok {
		l.lastSeen = time.Now()
	}
	n.mu.Unlock()
}

// reap 周期性回收长期未续期的租约，兜底客户端崩溃导致的占位泄漏。
func (n *Node) reap() {
	t := time.NewTicker(n.conf.ReaperTick)
	defer t.Stop()
	for {
		select {
		case <-n.done:
			return
		case <-t.C:
			cutoff := time.Now().Add(-n.conf.LeaseTTL)
			n.mu.Lock()
			var stale []string
			for tok, l := range n.leases {
				if l.lastSeen.Before(cutoff) {
					stale = append(stale, tok)
				}
			}
			n.mu.Unlock()
			for _, tok := range stale {
				log.Printf("[agent] 回收失活租约 token=%s", tok)
				_ = n.release(tok)
			}
		}
	}
}

// ---- RPC 方法 ----

// Open 借一张活页并发放令牌。Sticky/KeepHot 透传到底层 provider 的放置路由。
func (n *Node) Open(req proto.OpenReq, reply *proto.OpenReply) error {
	ctx, cancel := context.WithTimeout(context.Background(), n.conf.AcquireTimeout)
	defer cancel()
	handle, err := n.pvd.AcquireWeb(ctx, provider.AcquireOptions{Sticky: req.Sticky, KeepHot: req.KeepHot})
	if err != nil {
		return fmt.Errorf("agent: 借活失败: %w", err)
	}
	token := newToken()
	n.mu.Lock()
	n.leases[token] = &lease{page: handle.Web, release: handle.Release, lastSeen: time.Now()}
	n.mu.Unlock()
	reply.Token = token
	log.Printf("[agent] 已发放租约 token=%s", token)
	return nil
}

// Run 在指定令牌的活页上依次执行一组动作。
func (n *Node) Run(req proto.RunReq, reply *proto.RunReply) error {
	n.mu.Lock()
	l, ok := n.leases[req.Token]
	n.mu.Unlock()
	if !ok {
		return errors.New("agent: 令牌未知或已过期")
	}
	l.lastSeen = time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	results, err := l.page.RunActions(ctx, req.Actions)
	reply.Results = results
	return err
}

// Capture 捕获活页检查点（冷提交用），返回可序列化的会话状态。
func (n *Node) Capture(req proto.CapReq, reply *proto.CapReply) error {
	n.mu.Lock()
	l, ok := n.leases[req.Token]
	n.mu.Unlock()
	if !ok {
		return errors.New("agent: 令牌未知或已过期")
	}
	l.lastSeen = time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cp, err := l.page.CaptureCheckpoint(ctx)
	reply.Checkpoint = cp
	return err
}

// Restore 在活页上重放检查点（重水化用）。
func (n *Node) Restore(req proto.RestoreReq, reply *proto.EmptyReply) error {
	n.mu.Lock()
	l, ok := n.leases[req.Token]
	n.mu.Unlock()
	if !ok {
		return errors.New("agent: 令牌未知或已过期")
	}
	l.lastSeen = time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return l.page.RestoreCheckpoint(ctx, req.Checkpoint)
}

// Release 归还令牌对应的活页（幂等）。
func (n *Node) Release(req proto.HeartbeatReq, reply *proto.EmptyReply) error {
	return n.release(req.Token)
}

func (n *Node) release(token string) error {
	n.mu.Lock()
	l, ok := n.leases[token]
	if ok {
		delete(n.leases, token)
	}
	n.mu.Unlock()
	if !ok {
		return nil // 幂等:已归还则无事可做
	}
	if err := l.release(); err != nil {
		log.Printf("[agent] 归还租约失败 token=%s: %v", token, err)
		return err
	}
	log.Printf("[agent] 已归还租约 token=%s", token)
	return nil
}

// Heartbeat 刷新令牌的活跃时间,防被回收器误判失活。
func (n *Node) Heartbeat(req proto.HeartbeatReq, reply *proto.EmptyReply) error {
	n.mu.Lock()
	if l, ok := n.leases[req.Token]; ok {
		l.lastSeen = time.Now()
		n.mu.Unlock()
		return nil
	}
	n.mu.Unlock()
	return errors.New("agent: 令牌未知或已过期")
}

// Stat 返回节点池容量快照（协议版 PoolStat），供远端调度做放置决策。
func (n *Node) Stat(req proto.StatReq, reply *proto.StatReply) error {
	st := n.pvd.Stats()
	reply.State = proto.PoolStat{
		Capacity:  st.Capacity,
		InUse:     st.InUse,
		Available: st.Available,
		Ready:     st.Ready,
	}
	return nil
}

// FetchArtifact 把节点侧落盘的产物字节取回（如 PDF），供会话侧"回迁"到本地。
// 安全假设：节点与客户端属于同一信任域；生产环境应改为对象存储/临时签名地址。
func (n *Node) FetchArtifact(req proto.FetchReq, reply *proto.FetchReply) error {
	data, err := os.ReadFile(req.Path)
	if err != nil {
		return fmt.Errorf("agent: 读取产物 %q 失败: %w", req.Path, err)
	}
	reply.Data = data
	return nil
}
