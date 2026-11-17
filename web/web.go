package web

import (
	"context"

	"github.com/LouYuanbo1/spider-nest/action"
	"github.com/LouYuanbo1/spider-nest/hijack"
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
	RunAction(ctx context.Context, action action.Action) (action.ActionResult, error)
	// RunActions 依次执行一组动作，出错即停并返回出错前结果。
	// 只做执行，不做监听；需要监听请包一层 WithHijack 闭包。
	RunActions(ctx context.Context, actions []action.Action) ([]action.ActionResult, error)
	// WithHijack 在活页上挂起一套 page 级拦截，fn 结束即自动撤除。
	// 与 Spider.WithHijack 同构：fn 内任意多轮操作共享同一监听窗，生命周期收敛在
	// fn 作用域上，无需调用方锁或手动 Stop。规则为空时 fn 原样执行。
	WithHijack(fn func(Web) error, rules ...hijack.HijackRule) error
}
