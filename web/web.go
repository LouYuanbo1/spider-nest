package web

import (
	"context"

	"github.com/LouYuanbo1/spider-nest/action"
)

// Web 是"能执行浏览器动作的一张活页"的门面契约（Route B 的关键切缝）。
//
// 设计动机：路由 B（动作级 RPC）要求"远端替身"与本地 *Page 走同一条调用面，
// 而上层（server 的 MCP 工具、session 的冷热切换）只需三类操作：跑动作、抓/放检查点。
// 因此把 *Page 的受用面抽象收敛成一个小接口 Web：
//   - 本地实现：*Page（本包），零成本已满足；
//   - 远端实现：云端 agent 发出的 RPC 代理（见 provider.remote / cloud 侧），实现同一契约。
//
// 如此上层只依赖 Web，不关心活页在本进程还是隔了 RPC——这正是"编排层与底层解耦"的落点。
// 注意：*Page 其余方法（Navigate/DoClick/GetHTML...）是其执行细节，供本进程内 Page
// 的 RunAction 分派用；超 RPC 边界后，动作统一收敛到 action.Action，再以 RunAction 派发。
type Web interface {
	// RunAction 在活页上执行单个动作，返回结构化结果。
	RunAction(ctx context.Context, a action.Action) (action.ActionResult, error)

	// RunActions 依次执行一组动作，出错即停并返回出错前结果。
	RunActions(ctx context.Context, actions []action.Action) ([]action.ActionResult, error)

	// CaptureCheckpoint 捕获可序列化的会话状态（URL+cookies+恢复脚本），用于热→冷提交。
	CaptureCheckpoint(ctx context.Context) (*Checkpoint, error)

	// RestoreCheckpoint 在（重物化的）活页上重放检查点，用于冷→热重水化。
	RestoreCheckpoint(ctx context.Context, cp *Checkpoint) error
}

// _ 编译期断言：Page 是 Web 契约的本地实现。
// 变更契约后若漏改 Page，会在编译期立刻暴露。
var _ Web = (*Page)(nil)
