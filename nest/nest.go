package nest

import (
	"context"

	"github.com/LouYuanbo1/spider-nest/action"
	"github.com/LouYuanbo1/spider-nest/config"
	"github.com/LouYuanbo1/spider-nest/spider"
)

// NestSpider 是"能被巢托管"的蜘蛛类型约束：形状必须是指针 *T，且该指针实现日常
// API 契约 spider.Spider。指针形状保证 S 必然可比较（故 s == nil 合法），判空
// 无需反射；Spider 只需承载"借网/织动作"的日常行为即可入巢。
type NestSpider[T any] interface {
	*T
	spider.Spider
}

// Nest 抽象"一池蜘蛛"的生命周期契约：定义一座巢该具备的行为——借/还/关一个
// 蜘蛛(浏览器)实例。所有实现（Grove 朴素的 chan 池、Orchard 列表+筛选池）都应
// 满足它，上层（session/manager）只依赖本接口，从而可在不同实现间无感切换。
//
// T 是蜘蛛承载的模型类型，S 是具体蜘蛛类型（指针 *T），约束见 NestSpider。
type Nest[T any, S NestSpider[T]] interface {
	// Size 返回并发上限（可同时活动的蜘蛛数）。
	Size() int
	// Acquire 派一只蜘蛛：池满时阻塞等待，直到有蜘蛛被 Release 放回。
	Acquire() (S, error)
	// AcquireCtx 同 Acquire，但支持通过 ctx 取消"池满等待"。
	AcquireCtx(ctx context.Context) (S, error)
	// Release 让一只织完的蜘蛛回巢，供后续任务复用。
	Release(s S) error
	// Close 关闭整座巢：逐个关闭池中蜘蛛（含浏览器/页面），释放资源。
	Close() error
}

// Colony(聚居地) 完整协作池：在 Nest 的生命周期契约之上，再叠加"借-用-还"的一次性封装
// （BorrowSpider / Crawl / CrawlConcurrent），代表一座协力做工的蜘蛛聚居地。
// 它是 Nest 的超集：凡上层需要直接驱动整池去做完整抓取（而非只借还原语）的，
// 应依赖 Colony 而非裸 Nest。Grove 与 Orchard 均已实现这些方法，都满足本接口。
type Colony[T any, S NestSpider[T]] interface {
	Nest[T, S]
	// BorrowSpider 借用一只蜘蛛执行 fn，并在结束后自动归还（无论 fn 正常返回
	// 还是 panic，蜘蛛都会被放回巢中供复用，避免"借了不还"导致池占满）。
	BorrowSpider(fn func(S) error, hijackCfgs ...config.HijackConfig) error
	// Crawl 一次性在池内执行一组动作：借一只蜘蛛（可选挂监听）→ 顺序跑完
	// actions → 自动归还。hijackCfgs 可选，零个即不监听。
	Crawl(ctx context.Context, actions []action.Action, hijackCfgs ...config.HijackConfig) ([]action.ActionResult, error)
	// CrawlConcurrent 一次性在池内并发执行多组动作：借一只蜘蛛（可选挂监听），
	// 把各组分别委托给该蜘蛛内不同的网并发执行，跑完自动归还。hijackCfgs 可选。
	CrawlConcurrent(ctx context.Context, actionGroups [][]action.Action, hijackCfgs ...config.HijackConfig) ([][]action.ActionResult, error)
}

// IdlePicker 是"余量感知巢"的窄接口：在 Nest 生命周期契约之上，再暴露原子化的
// "按谓词挑有余量的蜘蛛"原语 AcquireIf。
//
// 对上层（如 server.NestProvider）而言，它把"挑到一只有余量的蜘蛛"这件事变成
// 一次在巢内同一把锁里完成的原子操作——不存在"先取一只、再查余量、不够放回再试"
// 的竞态窗口。Grove（chan 信号量、随手取一只）不含此原语，故只有 Orchard 满足
// 本接口；语义上正对应"每蜘蛛养多网、多会话共享"的高压榨编排场景。
type IdlePicker[T any, S NestSpider[T]] interface {
	// AcquireIf 借一只满足 predicate 的蜘蛛；在同一把巢锁内原子完成筛选与占用。
	// 返回的蜘蛛此刻必定满足 predicate，且为调用方独占（余量对它是单调不减的）。
	AcquireIf(ctx context.Context, predicate func(S) bool) (S, error)
	// Release 让一只织完的蜘蛛回巢（失效即销毁重建，语义同 Nest.Release）。
	Release(s S) error
	// Size 返回并发上限。
	Size() int
	// Close 关闭整座巢。
	Close() error
}
