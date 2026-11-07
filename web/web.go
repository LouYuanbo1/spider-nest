package web

import (
	"context"

	"github.com/LouYuanbo1/spider-nest/action"
)

// Web 是"能执行浏览器动作的一张活页"的门面契约。
//
// 设计动机：上层（server 的 MCP 工具、session 会话器）只需跑动作这一类操作，
// 因此把 *Page 的受用面抽象收敛成一个小接口 Web：
//   - 本地实现：*Page（本包），零成本已满足。
//
// 上层只依赖 Web 契约，不关心活页的底层实现细节；*Page 其余方法
// （Navigate/DoClick/GetHTML...）是其执行细节，供本进程内 Page 的 RunAction
// 分派用，动作统一收敛到 action.Action 再以 RunAction 派发。
type Web interface {
	// RunAction 在活页上执行单个动作，返回结构化结果。
	RunAction(ctx context.Context, a action.Action) (action.ActionResult, error)

	// RunActions 依次执行一组动作，出错即停并返回出错前结果。
	RunActions(ctx context.Context, actions []action.Action) ([]action.ActionResult, error)
}

// _ 编译期断言：Page 是 Web 契约的本地实现。
// 变更契约后若漏改 Page，会在编译期立刻暴露。
var _ Web = (*Page)(nil)
