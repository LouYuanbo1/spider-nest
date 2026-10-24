package spider

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/LouYuanbo1/spider-nest/config"
	"github.com/LouYuanbo1/spider-nest/spider/hijack"
	"github.com/LouYuanbo1/spider-nest/web"
)

// AcquiredSpider 是从巢借出一只蜘蛛后、由调用方持有直到释放的句柄。
//
// 它的生命周期分三阶段，语义各自清晰：
//
//	构建阶段  WithContext / Hijack  配置上下文与蜘蛛级监听（可省略）
//	执行阶段  Go                    每次自动"借网→跑动作→还网"
//	终结阶段  Release               归还蜘蛛、撤销监听、取回全部错误
//
// 设计约定：
//   - 蜘蛛的借出是显式的（调用方持有本句柄）；归还必须调用 Release，
//     否则会一直占住巢的并发槽位。
//   - 网页的借还完全隐式：每次 Go 自动借一张网、执行完整组动作、自动归还。
//   - 监听归属蜘蛛而非某个网页：Hijack 声明一次，跨多次 Go 持续生效，
//     直到 Release 才随蜘蛛一起撤除。
//   - 所有中途错误累积在句柄内，可随时用 Err 查看，最终由 Release 一次性取回。
type AcquiredSpider struct {
	sp     Spider // 已借到的蜘蛛本体（完整契约，内置常驻监听能力，无需断言）
	rel    func() error
	ctx    context.Context
	logger *slog.Logger

	started    bool // 已开始提交首个 Go 任务，自此锁定监听不再可增删
	mounted    bool // 常驻监听已挂载（幂等，只挂一次）
	mounting   bool // 常驻监听正在挂载中（并发首个 Go 只允许一个真正执行挂载）
	released   bool // 已归还（幂等，Release 只生效一次）
	errs       []error
	rules      []hijack.HijackRule  // 蜘蛛级监听
	stopHijack func()               // 常驻监听的撤销闭包（nil 表示未挂载）
	results    [][]web.ActionResult // 各次 Go 的结果组（外层按下标对齐第几次 Go）

	mu        sync.Mutex // 保护 errs 与 results（多个并发后台任务共享）
	lifecycle sync.Mutex // 保护生命周期字段（started/mounted/released/stopHijack/rules/ctx）
	wg        sync.WaitGroup
}

// NewAcquiredSpider 构造一只已借到手的蜘蛛句柄。rel 为归还回调（可为 nil）。
// sp 必须实现 Spider（完整契约：日常 API + 常驻监听能力合并），因此句柄
// 内部可直接、无需断言地挂载 Hijack——监听能力在编译期即已收于接口之内。
// 通常通过 Nest.AcquireSpider 获得；此处导出供测试与非巢的借用路径使用。
func NewAcquiredSpider(sp Spider, rel func() error) *AcquiredSpider {
	return &AcquiredSpider{
		sp:     sp,
		rel:    rel,
		ctx:    context.Background(),
		logger: slog.Default(),
	}
}

// WithContext 设置后续动作执行所用的 context（默认 context.Background）。
// 不改变任何生命周期，只改变动作执行的取消/超时源。返回自身以继续链式调用。
func (b *AcquiredSpider) WithContext(ctx context.Context) *AcquiredSpider {
	if ctx != nil {
		b.lifecycle.Lock()
		b.ctx = ctx
		b.lifecycle.Unlock()
	}
	return b
}

// Hijack 声明一组监听（声明式 config）归属这只蜘蛛。首次执行动作时以常驻
// 方式挂载，跨后续每次 Go 持续生效，Release 时统一撤除——生命周期与蜘蛛绑定，
// 而非绑定到某一次借出的网页。执行开始后（首个 Go 之后）不可再增删规则，对应
// go-rod"Run 后不得 Add"的约束，所以声明须发生在首个动作之前。
func (b *AcquiredSpider) Hijack(cfg ...config.HijackConfig) *AcquiredSpider {
	b.lifecycle.Lock()
	if b.started {
		b.lifecycle.Unlock()
		b.errorf(errors.New("监听已挂载，执行开始后不可再增删规则"))
		return b
	}
	b.lifecycle.Unlock()

	rules, err := RulesFromConfig(cfg, b.logger)
	if err != nil {
		b.errorf(err)
		return b
	}
	b.lifecycle.Lock()
	b.rules = append(b.rules, rules...)
	b.lifecycle.Unlock()
	return b
}

// Go 异步提交一组动作：立即返回、不阻塞本 goroutine，内部借一张网、在蜘蛛级
// 监听下跑完整组动作、然后自动归还。多次 Go 之间是并发的——各自异步借网，
// 充分利用蜘蛛内部网页池的并发能力（而非串行逐个借网）。返回自身以继续链式。
//
// 结果按提交先后存入 Results 对应槽位（外层下标 = 第几次 Go），不受完成顺序影响。
// 数据竞态由内部锁与 WaitGroup 负责：本方法与同一句柄的其它方法可放心并发调用，
// 但须保证最终调用一次 Release 等待全部后台任务收尾并归还蜘蛛。
func (b *AcquiredSpider) Go(actions ...web.Action) *AcquiredSpider {
	b.lifecycle.Lock()
	b.started = true
	ctx := b.ctx // 在后台 goroutine 内只读本次捕获的 ctx，避免与 WithContext 竞争
	b.lifecycle.Unlock()

	if b.sp == nil {
		b.errorf(errors.New("蜘蛛未成功借出，无法执行"))
		return b
	}
	// 首次提交前挂载蜘蛛级常驻监听，此后跨多次 Go 统一生效（提交前同步完成，
	// 确保每个后台任务借网时监听已就位）。
	b.mountHijack()

	b.mu.Lock()
	idx := len(b.results)
	b.results = append(b.results, nil) // 预分配槽位，后台按提交下标回填
	b.mu.Unlock()

	b.wg.Go(func() {
		err := b.sp.Borrow(func(w *web.Web) error {
			r, e := w.RunActions(ctx, actions)
			b.mu.Lock()
			b.results[idx] = r // 按提交序回填，完成先后不影响外层下标
			b.mu.Unlock()
			return e
		})
		b.errorf(err)
	})
	return b
}

// Results 返回自构造以来每次 Go 带回的结果组（二重数组：外层下标 = 第几次 Go，
// 内层为该次 Go 内按顺序执行的动作结果）。多次 Go 的结果全部保留、互不覆盖。
// 结果按提交顺序对齐外层层级，与各 Go 后台完成的先后无关；若某次 Go 仍在执行，
// 对应槽位此刻可能为空切片，须等 Release 收齐后再读。
func (b *AcquiredSpider) Results() [][]web.ActionResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([][]web.ActionResult, len(b.results))
	copy(out, b.results)
	return out
}

// Err 返回自借用以来累积的全部错误（errors.Join 合并）。
func (b *AcquiredSpider) Err() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return errors.Join(b.errs...)
}

// Report 把借出阶段或链式中途的一个错误记录进句柄，返回自身以保持链式。
// 适合在"必须先拿到句柄才能继续链式、但又需要把前置错误带进链"的场景
// （如 Nest.AcquireSpider 借出失败时）。错误最终由 Err 或 Release 取回。
func (b *AcquiredSpider) Report(err error) *AcquiredSpider {
	b.errorf(err)
	return b
}

// Release 等待全部后台 Go 任务收尾，然后归还这只蜘蛛（向巢还一个并发槽位），
// 撤销常驻监听，并返回累积的全部错误。这是句柄的终结方法：调用后句柄即已归还
// （幂等，重复调用只返回已有错误，不会再触发归还回调或撤销监听），因此不应再被
// 用于任何执行。调用前应已把本句柄上所有想跑的动作通过 Go 提交完。
func (b *AcquiredSpider) Release() error {
	b.lifecycle.Lock()
	if b.released {
		b.lifecycle.Unlock()
		return b.Err()
	}
	b.released = true
	stop := b.stopHijack
	b.stopHijack = nil
	b.lifecycle.Unlock()

	// 先等后台任务全部收尾，确保没有任务仍在借网执行。
	b.wg.Wait()

	if stop != nil {
		stop()
	}
	if b.rel != nil {
		b.errorf(b.rel())
	}
	return b.Err()
}

// mountHijack 在首次执行（首个 Go）时把已声明的监听一次性以常驻方式挂载到蜘蛛上
// （幂等，只挂一次）。挂载刻意选在首个 Go 而非 Hijack 声明时：Hijack 只是累积规则，
// 若借到中途放弃（不 Go 即 Release），监听便无需真正挂上，避免白占一个监听 goroutine；
// 同时首个 Go 正是规则被锁定的时刻（started），"规则上墙"与"规则锁定"于此同点达成。
// 蜘蛛以完整的 Spider 契约持有，EnableHijack 编译期即可直接调用，无需要时
// 断言探测——能力在构造时已通过接口合并被收编进句柄。
func (b *AcquiredSpider) mountHijack() {
	// 快照规则并登记挂载意图；真正的 EnableHijack 在锁外执行，避免持有锁
	// 期间做阻塞式网络调用，也避免被并发 Hijack 追加规则造成数据竞争。
	b.lifecycle.Lock()
	if b.mounted || len(b.rules) == 0 {
		b.lifecycle.Unlock()
		return
	}
	if b.mounting {
		b.lifecycle.Unlock()
		return // 已有并发 Go 在挂载，等它完成后本批规则由对方一并挂上
	}
	b.mounting = true
	rules := append([]hijack.HijackRule(nil), b.rules...)
	b.lifecycle.Unlock()

	stop, err := b.sp.EnableHijack(rules)
	if err != nil {
		b.errorf(err)
		b.lifecycle.Lock()
		b.mounting = false
		b.lifecycle.Unlock()
		return
	}
	b.lifecycle.Lock()
	b.mounted = true
	b.stopHijack = stop
	b.mounting = false
	b.lifecycle.Unlock()
}

// errorf 累积一个错误（errors.Join 保留全部）。可被并发后台任务与 Release 调用，
// 故加锁保护 errs。
func (b *AcquiredSpider) errorf(err error) {
	if err != nil {
		b.mu.Lock()
		b.errs = append(b.errs, err)
		b.mu.Unlock()
	}
}
