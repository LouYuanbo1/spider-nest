package nest

import (
	"context"
	"errors"
	"log/slog"

	"github.com/LouYuanbo1/spider-nest/action"
	"github.com/LouYuanbo1/spider-nest/config"
	"github.com/LouYuanbo1/spider-nest/spider"
)

// nestSpider 是"能被巢托管"的蜘蛛类型约束：形状必须是指针 *T，且该指针实现日常
// API 契约 spider.Spider。指针形状保证 S 必然可比较（故 s == nil 合法），判空
// 无需反射；Spider 只需承载"借网/织动作"的日常行为即可入巢。
type nestSpider[T any] interface {
	*T
	spider.Spider
}

// Nest 蜘蛛巢：一座巢(Nest)里住着很多只蜘蛛(spider)。每只蜘蛛都拥有
// 自己独立的浏览器实例与页面池（"网"），因此巢本身维护的是一个
// 蜘蛛池（即浏览器池）：并发上限 = 巢内蜘蛛数。
//
// 泛型参数 S 是巢内蜘蛛的具体类型：它决定巢以何种类型存取蜘蛛（如 *Weaver）。
// 约束 nestSpider 仅要求其是指针且实现日常 API 契约 spider.Spider。
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
type Nest[T any, S nestSpider[T]] struct {
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
func NewNest[T any, S nestSpider[T]](spiderSize int, create func() (S, error)) (*Nest[T, S], error) {
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

// BorrowSpider 借用一只蜘蛛执行 fn，并在结束后自动归还（推荐的安全用法）。
// 它等价于 Acquire + defer Release：无论 fn 正常返回还是 panic，
// 蜘蛛都会被放回巢中供复用，从根上避免"借了不还"导致的池占满死锁。
//
// fn 是本次要执行的抓取逻辑（拿到蜘蛛本体）；hijackCfgs 是可选的、本任务附带的
// 监听配置，按需传（零个即不监听）。有监听时由 WithHijack 为本次"借-用-还"挂起
// 一套独立的监听 goroutine，fn 执行完（含 panic）立即撤除，蜘蛛池里的实例因此
// 永远以"干净、不带监听"的状态被复用，多只蜘蛛不会互相重复挂监听。
func (n *Nest[T, S]) BorrowSpider(fn func(S) error, hijackCfgs ...config.HijackConfig) error {
	s, err := n.Acquire()
	if err != nil {
		return err
	}
	defer n.Release(s)

	rules, err := spider.RulesFromConfig(hijackCfgs, n.logger)
	if err != nil {
		return err
	}
	return s.WithHijack(
		func(spider.Spider) error {
			return fn(s)
		},
		rules...,
	)
}

// Crawl 一次性在巢内执行一组动作：借一只蜘蛛（可选挂监听）→ 顺序跑完
// actions → 自动归还。是对最常用"抓一次"场景的一站式封装，等价于
// BorrowSpider + s.Crawl。hijackCfgs 可选，零个即不监听。
func (n *Nest[T, S]) Crawl(ctx context.Context, actions []action.Action, hijackCfgs ...config.HijackConfig) ([]action.ActionResult, error) {
	var results []action.ActionResult
	err := n.BorrowSpider(func(s S) error {
		var err error
		results, err = s.Crawl(ctx, actions)
		return err
	}, hijackCfgs...)
	return results, err
}

// CrawlConcurrent 一次性在巢内并发执行多组动作：借一只蜘蛛（可选挂监听），
// 把各组分别委托给该蜘蛛内不同的网并发执行、每组独占一张页面，跑完自动归还。
// 所有并发网共享同一套可选监听（同一只蜘蛛的一次 WithHijack 窗口）。hijackCfgs 可选。
func (n *Nest[T, S]) CrawlConcurrent(ctx context.Context, actionGroups [][]action.Action, hijackCfgs ...config.HijackConfig) ([][]action.ActionResult, error) {
	var results [][]action.ActionResult
	err := n.BorrowSpider(func(s S) error {
		var err error
		results, err = s.CrawlConcurrent(ctx, actionGroups)
		return err
	}, hijackCfgs...)
	return results, err
}
