package nest

import (
	"errors"
	"log/slog"

	"github.com/LouYuanbo1/spider-nest/config"
	"github.com/LouYuanbo1/spider-nest/spider"
)

// hijackSpider 是携常驻监听能力的蜘蛛指针约束：形状必须是指针 *T，且该指针实现
// 完整的 Spider 契约（日常 API + 常驻监听能力，内嵌 PersistentHijacker）。它把
// 两件事一次性在编译期锁死：
//   - 能力可用：实例化出的 S 必然能静态调用 EnableHijack（无需运行时断言）；
//   - 形状是指针：S 必然可比较（指针），故 s == nil 合法，判空无需反射。
//
// 约束只需引用 Spider 一个接口：Spider 自身已内嵌 PersistentHijacker，故"能日常
// 使用且能常驻监听的蜘蛛"在编译期即被锁定，无需再并列引用两个接口。
type hijackSpider[T any] interface {
	*T
	spider.Spider
}

// Nest 蜘蛛巢：一座巢(Nest)里住着很多只蜘蛛(spider)。每只蜘蛛都拥有
// 自己独立的浏览器实例与页面池（"网"），因此巢本身维护的是一个
// 蜘蛛池（即浏览器池）：并发上限 = 巢内蜘蛛数。
//
// 泛型参数 S 是巢内蜘蛛的具体类型：它决定巢以何种类型存取蜘蛛（如 *Weaver）。
// 约束 hijackSpider 保证每只蜘蛛都具备常驻监听能力，编译器据此允许 DSL 直接
// 静态调用 EnableHijack，无需运行时类型断言。
//
// 复用模型：
//   - 并发上限 = 巢内蜘蛛数（chan 容量即上限）；
//   - Acquire 从池里取一只可复用的蜘蛛（取到 nil 空槽位时才调用
//     create 惰性新建），每只蜘蛛内部再通过其页面池管理多张"网"；
//   - Release 把用过的蜘蛛放回池中供其他任务复用，而非销毁，避免反复
//     启停 Chrome 的开销。
//
// 配置归属：Nest 只管蜘蛛池生命周期，**不持有蜘蛛配置**。蜘蛛按何种配置创建，
// 完全由 create 工厂决定（调用方可闭包捕获自己的配置）——谁会建蜘蛛，谁给配置。
//
// 每只蜘蛛独占一个 *rod.Browser（独立 Chrome 进程），蜘蛛内部的页面池再进一步
// 把并发 tab 收敛到各自的进程中，扩展性与隔离性兼具。
//
// 池的实现与 go-rod 的 generic Pool 一致：内部就是一个预填 nil 槽位的
// 有缓冲 chan，取到 nil 就新建。这里不直接复用 rod.Pool，因为创建失败时还会
// 吞掉通道容量。故用同一思路自行实现。
type Nest[T any, S hijackSpider[T]] struct {
	logger  *slog.Logger
	spiders chan S            // 蜘蛛池：每只蜘蛛独占一个浏览器进程
	create  func() (S, error) // 建蜘蛛工厂：由调用方按具体类型与配置提供
}

// NewNest 创建一座巢：
//   - spiderSize：巢内蜘蛛数（并发上限）；
//   - create：按具体指针类型 S 创建蜘蛛的工厂（如返回 *spider.Weaver）。create
//     应自行闭包捕获所需的配置——Nest 不持有、也不关心蜘蛛配置。
//     S 的形状由约束 *T 锁死为指针，故调用方传 create 时 T 会被自动推断。
//
// 通道预填 spiderSize 个 nil 槽位（借用 rod.Pool 的做法）：空槽表示"可取
// 出并新建"，因此 Acquire 天然可阻塞——所有槽位都被占用时取不到即等待。
// 浏览器按需惰性启动，只有被 Acquire 取出并新建的蜘蛛才真正拉起 Chrome。
func NewNest[T any, S hijackSpider[T]](spiderSize int, create func() (S, error)) (*Nest[T, S], error) {
	if spiderSize <= 0 {
		return nil, errors.New("spiderSize 必须大于 0")
	}
	if create == nil {
		return nil, errors.New("create 蜘蛛工厂不能为空")
	}

	n := &Nest[T, S]{
		logger:  slog.Default().With("component", "nest"),
		spiders: make(chan S, spiderSize),
		create:  create,
	}
	for range spiderSize {
		var zero S
		n.spiders <- zero
	}
	return n, nil
}

// Size 返回并发上限（可同时活动的蜘蛛数）。通道容量即信号量，容量即上限。
func (n *Nest[T, S]) Size() int { return cap(n.spiders) }

// Acquire 派一只蜘蛛：从池中取一个槽位（必要时惰性新建），池满时阻塞等待，
// 直到有蜘蛛被 Release 放回。返回类型为具体 S。
func (n *Nest[T, S]) Acquire() (S, error) {
	slot := <-n.spiders // 池满则阻塞，直到有蜘蛛被 Release 放回
	if slot != nil {
		return slot, nil
	}
	created, err := n.create()
	if err != nil {
		var zero S
		n.spiders <- zero // 创建失败，归还空槽位，避免容量泄漏
		return zero, err
	}
	return created, nil
}

// Release 让一只织完的蜘蛛回巢：把蜘蛛放回池中供复用，不销毁其浏览器。
// 若蜘蛛已失效（浏览器/页面连接关闭导致），则销毁它并放回一个空槽位，
// 由后续 Acquire 自动新建补上，避免劣化实例被反复复用到后续任务上。
func (n *Nest[T, S]) Release(s S) error {
	if s == nil {
		return nil
	}
	if s.IsActive() {
		n.spiders <- s
		return nil
	}
	n.logger.Warn("蜘蛛实例失效，销毁重建")
	_ = s.Close()
	var zero S
	n.spiders <- zero
	return nil
}

// Close 关闭整座巢：逐个关闭池中的蜘蛛（含各自浏览器与页面），释放资源。
func (n *Nest[T, S]) Close() error {
	n.logger.Info("关闭蜘蛛巢", "size", n.Size())
	for range n.Size() {
		select {
		case s := <-n.spiders:
			if s != nil {
				_ = s.Close()
			}
		default:
		}
	}
	return nil
}

// Borrow 借用一只蜘蛛执行 fn，并在结束后自动归还（推荐的安全用法）。
// 它等价于 Acquire + defer Release：无论 fn 正常返回还是 panic，
// 蜘蛛都会被放回巢中供复用，从根上避免"借了不还"导致的池占满死锁。
//
// hijackCfg 是本任务附带的一次性监听配置：借到蜘蛛后由 WithHijack 为本次
// "借-用-还"挂起一套独立的监听 goroutine，fn 执行完（含 panic）立即撤除，
// 蜘蛛池里的实例因此永远以"干净、不带监听"的状态被复用，多只蜘蛛不会
// 互相重复挂监听。不需要监听时传 nil 即可。
func (n *Nest[T, S]) Borrow(hijackCfg []config.HijackConfig, fn func(spider.SpiderAPI) error) error {
	s, err := n.Acquire()
	if err != nil {
		return err
	}
	defer n.Release(s)

	rules, err := spider.RulesFromConfig(hijackCfg, n.logger)
	if err != nil {
		return err
	}
	return s.WithHijack(rules, fn)
}

// AcquireSpider 显式地从巢借出一只蜘蛛，返回一只 AcquiredSpider 句柄。
// 这是 DSL（显式借-用-还）的入口：拿到句柄后可在其下借网、叠动作，最后必须
// 调用句柄.Release() 归还——否则会占住巢的一个并发槽位不放。
//
// 它始终返回可链式书写的句柄：借出失败时句柄携带错误，可继续在链上 Report/
// Go，或直接 Release() 一次性取回——不因借用失败打断链式写法。
//
// 泛型约束 hijackSpider 在此锁定"巢内蜘蛛必能常驻监听"（编译期保证创建的类型
// 静态实现 PersistentHijacker）。句柄拿到宽接口 Spider 后，首次执行时再对本体做
// PersistentHijacker 断言按需取回常驻能力（方案A思想）——编译期锁定建池类型、
// 运行时探测具体能力，两者各司其职。
func (n *Nest[T, S]) AcquireSpider() *spider.AcquiredSpider {
	s, err := n.Acquire()
	if err != nil {
		return spider.NewAcquiredSpider(nil, nil).Report(err)
	}
	return spider.NewAcquiredSpider(
		s,
		func() error { return n.Release(s) },
	)
}
