// Package client 是"会话侧 → 云端节点"的 RPC 客户端。
//
// 它把对一个节点（cloud/agent）的远程调用包装成一个 Conn，并把"该节点上的一张活页"
// 适配成 web.Web（RemotePage）——因此上层 session/server 只需用 web.Web 契约即可，
// 与本进程内跑动作零差异。这是 Route B"动作级 RPC"在会话侧的全部落点：
//   - RemotePage.RunAction / CaptureCheckpoint / RestoreCheckpoint：把动作/检查点"拍"
//   - RPC 到持有该活页的节点执行；
//   - 产物回传：动作若返回 ArtifactPath（节点本地路径），Conn 会经 FetchArtifact 把字节
//     拉到会话侧临时文件并把 ArtifactPath 改写为本地路径，让 MCP 层拿到的仍是可读文件。
//
// 传输为 net/rpc+gob（见 cloud/proto），Conn 线程安全（rpc.Client 并发安全）。
package client

import (
	"context"
	"fmt"
	"net/rpc"
	"os"
	"path/filepath"
	"strings"

	"github.com/LouYuanbo1/spider-nest/action"
	"github.com/LouYuanbo1/spider-nest/cloud/proto"
	"github.com/LouYuanbo1/spider-nest/web"
)

// Conn 是到单个云端节点的 RPC 连接。rpc.Client 并发安全，可被多个 goroutine 共享。
type Conn struct {
	addr string
	raw  *rpc.Client
}

// Dial 拨号到节点地址（host:port），建立 gob RPC 连接。
func Dial(addr string) (*Conn, error) {
	raw, err := rpc.Dial("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("cloud/client: 拨号节点 %s 失败: %w", addr, err)
	}
	return &Conn{addr: addr, raw: raw}, nil
}

// Addr 返回节点地址。
func (c *Conn) Addr() string { return c.addr }

// Close 关闭底层连接。
func (c *Conn) Close() error { return c.raw.Close() }

// call 统一走 rpc 调用并把错误包装上节点地址，便于定位。
func (c *Conn) call(serviceMethod string, req, reply any) error {
	if err := c.raw.Call(serviceMethod, req, reply); err != nil {
		return fmt.Errorf("cloud/client: 节点 %s 调用 %s 失败: %w", c.addr, serviceMethod, err)
	}
	return nil
}

// Open 在节点上借一张活页，返回其令牌（后续 Run/Capture/Release 均凭令牌寻址）。
func (c *Conn) Open(ctx context.Context, sticky map[string]string, keepHot bool) (string, error) {
	req := proto.OpenReq{Sticky: sticky, KeepHot: keepHot}
	var repl proto.OpenReply
	if err := c.call("Node.Open", req, &repl); err != nil {
		return "", err
	}
	return repl.Token, nil
}

// Release 归还令牌对应的活页（幂等）。
func (c *Conn) Release(token string) error {
	return c.call("Node.Release", proto.HeartbeatReq{Token: token}, &proto.EmptyReply{})
}

// Heartbeat 给令牌续期，防止其被节点当作失活而回收。
func (c *Conn) Heartbeat(token string) error {
	return c.call("Node.Heartbeat", proto.HeartbeatReq{Token: token}, &proto.EmptyReply{})
}

// Stat 取节点池容量快照，供远端调度做放置决策。
func (c *Conn) Stat() (proto.PoolStat, error) {
	var repl proto.StatReply
	if err := c.call("Node.Stat", proto.StatReq{}, &repl); err != nil {
		return proto.PoolStat{}, err
	}
	return repl.State, nil
}

// RemotePage 把一个已持有的令牌包装成一张可执行动作的远端活页（web.Web）。
func (c *Conn) RemotePage(token string) web.Web {
	return &remotePage{conn: c, token: token}
}

// remotePage 是 web.Web 的 RPC 代理：每个动作/检查点操作都转发到持有该活页的节点。
type remotePage struct {
	conn  *Conn
	token string
}

func (p *remotePage) RunActions(ctx context.Context, actions []action.Action) ([]action.ActionResult, error) {
	req := proto.RunReq{Token: p.token, Actions: actions}
	var repl proto.RunReply
	if err := p.conn.call("Node.Run", req, &repl); err != nil {
		return nil, err
	}
	p.rehomeArtifacts(repl.Results)
	return repl.Results, nil
}

// RunAction 在远端活页上执行单个动作，并把节点侧产物的路径回迁到本地临时文件。
func (p *remotePage) RunAction(ctx context.Context, a action.Action) (action.ActionResult, error) {
	req := proto.RunReq{Token: p.token, Actions: []action.Action{a}}
	var repl proto.RunReply
	if err := p.conn.call("Node.Run", req, &repl); err != nil {
		return action.ActionResult{}, err
	}
	if len(repl.Results) == 0 {
		return action.ActionResult{}, nil
	}
	p.rehomeArtifacts(repl.Results)
	return repl.Results[0], nil
}

// rehomeArtifacts 把结果里的产物路径由"节点侧路径"回迁成"会话侧本地路径"：
// 经 FetchArtifact 取回字节、落到本进程临时文件，再把 ArtifactPath 改写为新路径。
// 这样上层（MCP 层）拿到的仍是可读文件，而非指向远端的无效路径。
func (p *remotePage) rehomeArtifacts(results []action.ActionResult) {
	for i := range results {
		if results[i].ArtifactPath == "" {
			continue
		}
		remote := results[i].ArtifactPath
		var repl proto.FetchReply
		if err := p.conn.call("Node.FetchArtifact", proto.FetchReq{Path: remote}, &repl); err != nil {
			results[i].ArtifactPath = "" // 拉取失败则降级为空,不阻塞主流程
			continue
		}
		local, err := writeTemp(remote, repl.Data)
		if err != nil {
			results[i].ArtifactPath = ""
			continue
		}
		results[i].ArtifactPath = local
	}
}

func (p *remotePage) CaptureCheckpoint(ctx context.Context) (*web.Checkpoint, error) {
	var repl proto.CapReply
	if err := p.conn.call("Node.Capture", proto.CapReq{Token: p.token}, &repl); err != nil {
		return nil, err
	}
	return repl.Checkpoint, nil
}

func (p *remotePage) RestoreCheckpoint(ctx context.Context, cp *web.Checkpoint) error {
	return p.conn.call("Node.Restore", proto.RestoreReq{Token: p.token, Checkpoint: cp}, &proto.EmptyReply{})
}

// writeTemp 把远端产物字节落到会话侧一个临时文件，并尽量沿用远端的扩展名，
// 以便上层按类型识别（如 .pdf）。返回本地路径。
func writeTemp(remotePath string, data []byte) (string, error) {
	ext := strings.ToLower(filepath.Ext(remotePath))
	f, err := os.CreateTemp("", "spider-nest-artifact-*"+ext)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return filepath.Clean(f.Name()), nil
}
