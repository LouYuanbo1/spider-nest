// Package proto 定义"会话侧 ↔ 云端节点"之间 RPC 的线槽协议（Route B 的传输契约）。
//
// 设计边界：
//   - 本包是被 provider（远程端）与 cloud/agent（节点端）共同引用的中立协议层，
//     **不得反向依赖 provider**，否则 provider→cloud→proto→provider 形成依赖环。
//   - 池容量快照故不直接复用 provider.PoolState，而在本层用等值的 PoolStat 表达，
//     由两端各自做一次平凡转换（provider.PoolState ↔ proto.PoolStat）。
//
// 传输选择：Go 标准库 net/rpc + gob。动作（[]action.Action）与检查点（*web.Checkpoint）
// 本就是可序列化值；gob 对"接口槽位"（如 []action.Action）要求把具体类型注册进来，
// 故在 init() 里对全部密封动作逐一 gob.Register。
//
// 边界声明：本项目刻意不引入 gRPC / 独立控制面（避免重依赖负担）。若上生产需要
// 跨语言 / 流式 / TLS / 鉴权等传输能力，请在本层契约基础上做 gRPC 版本，改口仅限
// cloud/agent（服务端）与 cloud/client（客户端）两个包，上层的编排逻辑不受影响。
package proto

import (
	"encoding/gob"

	"github.com/LouYuanbo1/spider-nest/action"
	"github.com/LouYuanbo1/spider-nest/web"
)

// 注册所有具体动作，使它们能装进 []action.Action 接口槽跨 gob 编解码。
// 新增动作类型（实现 action.Action 的 isAction）时，务必同步在此登记，否则 RPC 会报
// "no concrete type registered for interface"。
func init() {
	var actions = []action.Action{
		&action.Navigate{},
		&action.Click{},
		&action.ClickByAXTree{},
		&action.Input{},
		&action.InputByAXTree{},
		&action.Scroll{},
		&action.JavaScript{},
		&action.GetHTML{},
		&action.GetAXContent{},
		&action.GetStructured{},
		&action.GetContent{},
		&action.GetInteractive{},
		&action.GetPDF{},
	}
	for _, a := range actions {
		gob.Register(a)
	}
	gob.Register(&web.Checkpoint{})
}

// PoolStat 是节点池占用的一次只读快照（与 provider.PoolState 等值的协议版），
// 供远端调度侧"只看容量"地做放置决策，避免协议层依赖 provider。
type PoolStat struct {
	Capacity  int
	InUse     int
	Available int
	Ready     bool
}

// ---- 业务 RPC 载荷 ----

type OpenReq struct {
	Sticky  map[string]string
	KeepHot bool
}

type OpenReply struct {
	Token string
}

type RunReq struct {
	Token   string
	Actions []action.Action
}

type RunReply struct {
	Results []action.ActionResult
}

type CapReq struct {
	Token string
}

type CapReply struct {
	Checkpoint *web.Checkpoint
}

type RestoreReq struct {
	Token      string
	Checkpoint *web.Checkpoint
}

// ---- 控制 / 无用载荷 RPC ----

type StatReq struct{}

type StatReply struct {
	State PoolStat
}

type HeartbeatReq struct {
	Token string
}

type EmptyReply struct{}

type FetchReq struct {
	Path string
}

type FetchReply struct {
	Data []byte
}
