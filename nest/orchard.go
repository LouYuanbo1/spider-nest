package nest

import (
	"container/list"
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/LouYuanbo1/spider-nest/action"
	"github.com/LouYuanbo1/spider-nest/config"
	"github.com/LouYuanbo1/spider-nest/spider"
)

var _ Nest[spider.Weaver, *spider.Weaver] = (*Orchard[spider.Weaver, *spider.Weaver])(nil)
var _ Colony[spider.Weaver, *spider.Weaver] = (*Orchard[spider.Weaver, *spider.Weaver])(nil)
var _ IdlePicker[spider.Weaver, *spider.Weaver] = (*Orchard[spider.Weaver, *spider.Weaver])(nil)

// Orchard 综合栖息池：一座果园(Orchard)里聚集着多种蜘蛛(spider)各自结网(Web)。
// 与朴素的 Grove（以有缓冲 chan 为信号量、随手取一只）不同，Orchard 面向
// "一只蜘蛛(浏览器)内养多张网、多会话共享"的高压榨场景：
//
//   - 空闲实例存放在一个互斥锁保护的 FIFO 空闲队列里（而非 chan），
//     可以在同一把锁内【按谓词挑一只符合条件的蜘蛛】并原子地"占用"，
//     不存在"查匝余量后被争抢"的竞态窗口；
//   - AcquireIf 是核心原语：调用方用闭包表达"我要一只有余量的蜘蛛"，由果园
//     在空闲队列里筛出第一只满足者，或在有并发余量时惰性新建（新建结果同样复核）；
//   - 因此"队列成员 == 有余量"可被上层用作恒真不变量，predicate 退化为防御校验。
//
// 与 Grove 的取舍：
//   - Grove = 简单快，每蜘蛛一网够用；Orchard = 多一个 AcquireIf 与锁，换来
//     按余量筛选的编排能力。两者均满足 Nest[T,S] 接口，可并存、可替换。
//
// ErrOrchardClosed 表示果园已关闭，拒绝再进行派/收蜘蛛。
var ErrOrchardClosed = errors.New("orchard: 果园已关闭")

type Orchard[T any, S NestSpider[T]] struct {
	logger *slog.Logger
	create func() (S, error) // 建蜘蛛工厂：由调用方按具体类型与配置提供

	mu       sync.Mutex
	idle     *list.List    // 空闲队列：当前在池中可复用的活动蜘蛛（FIFO 复用）
	live     int           // 已物化的蜘蛛总数 = 空闲成员数 + 借出中数
	capacity int           // 并发上限（spiderSize）
	closed   bool          // 已关闭：拒绝再借出，唤醒所有阻塞中的等待者
	notify   chan struct{} // 广播唤醒通道：每次状态变更后关闭重建用于唤醒等待者
}

// NewOrchard 创建一座果园：
//   - spiderSize：园内蜘蛛数（并发上限）；
//   - create：按具体指针类型 S 创建蜘蛛的工厂（如返回 *spider.Weaver）。
//     S 的形状由约束 *T 锁死为指针，故调用方传 create 时 T 会被自动推断。
//
// 起初 live=0（一座空园）：蜘蛛按需惰性启动，求而未得时才在 lock 内原子预留一个
// 名额后锁外 create() 新建。能否新建以 live < capacity 为判据——上限永远被尊重，
// live 同时可当作园的即时占用观测值。
func NewOrchard[T any, S NestSpider[T]](spiderSize int, create func() (S, error)) (*Orchard[T, S], error) {
	if spiderSize <= 0 {
		return nil, errors.New("spiderSize 必须大于 0")
	}
	if create == nil {
		return nil, errors.New("create 蜘蛛工厂不能为空")
	}
	return &Orchard[T, S]{
		logger:   slog.Default().With("component", "orchard"),
		create:   create,
		idle:     list.New(),
		live:     0,
		capacity: spiderSize,
		notify:   make(chan struct{}),
	}, nil
}

// NewOrchardFromConfig 按 config.NestConfig 创建一座果园：等价于
// NewOrchard(cfg.MaxSpiders, create)，供已持有 NestConfig 的调用方驱动园容量。
func NewOrchardFromConfig[T any, S NestSpider[T]](cfg config.NestConfig, create func() (S, error)) (*Orchard[T, S], error) {
	return NewOrchard(cfg.SpiderPoolLimit, create)
}

// Size 返回并发上限（可同时活动的蜘蛛数）。
func (o *Orchard[T, S]) Size() int { return o.capacity }

// Acquire 派一只蜘蛛：从园中取一个槽位（必要时惰性新建），满时阻塞等待，
// 直到有蜘蛛被 Release 放回。等价于以恒真谓词调用 AcquireIf。
func (o *Orchard[T, S]) Acquire() (S, error) {
	return o.AcquireCtx(context.Background())
}

// AcquireCtx 派一只蜘蛛：同 Acquire，但支持通过 ctx 取消"满时等待"；取消时
// 既不取走槽位也无误记占用。
func (o *Orchard[T, S]) AcquireCtx(ctx context.Context) (S, error) {
	return o.AcquireIf(ctx, func(S) bool { return true })
}

// broadcast 关闭当前 notify 通道并新建一个，从而唤醒所有阻塞中的等待者。
// 必须在持有 o.mu 时调用。
func (o *Orchard[T, S]) broadcast() {
	close(o.notify)
	o.notify = make(chan struct{})
}

// AcquireIf 派一只"满足谓词"的蜘蛛：从空闲队列里挑第一只 predicate(sp)==true
// 复用；若无现成符合者，则在有并发余量时惰性新建（新建结果同样被谓词复核，
// 不满足则放回并继续等待）。满（无可复用实例且无并发余量）时阻塞等待，支持 ctx 取消。
//
// predicate 在持有 o.mu 的临界区内、与【借出+占用】原子地执行，因此不存在
// "查到余量后被争抢"的竞态窗口。返回值确保此刻满足 predicate（新建路径的
// 持逻辑：新建实例一经复核通过即被占用，余量在占用瞬间必然成立）。
func (o *Orchard[T, S]) AcquireIf(ctx context.Context, predicate func(S) bool) (S, error) {
	var zero S
	o.mu.Lock()
	for {
		if o.closed {
			o.mu.Unlock()
			return zero, ErrOrchardClosed
		}

		// 1) 优先复用：在空闲队列里找第一只满足谓词的活动蜘蛛。
		for e := o.idle.Front(); e != nil; e = e.Next() {
			sp := e.Value.(S)
			if predicate(sp) {
				o.idle.Remove(e)
				o.mu.Unlock()
				return sp, nil
			}
		}

		// 2) 无可复用者：若还有并发余量（live < capacity）则惰性新建。新建在锁外
		//    执行，避免持锁阻塞别人等待；但"余量判定 + 名额预留"与新建结果复核
		//    都在锁内原子完成，杜绝并发下越过 capacity 的过度新建。
		if o.live < o.capacity {
			o.live++
			o.mu.Unlock()

			created, err := o.create()
			if err != nil {
				o.mu.Lock()
				o.live-- // 创建失败，把名额还回并发余量，避免容量泄漏
				o.broadcast()
				o.mu.Unlock()
				return zero, err
			}

			o.mu.Lock()
			if predicate(created) {
				o.mu.Unlock()
				return created, nil
			}
			// 新建的不满足谓词：放回空闲队列，继续等待符合条件的实例。
			o.idle.PushBack(created)
			o.broadcast()
			continue
		}

		// 3) 满：挂到 notify 通道上阻塞等待，支持 ctx 取消。
		ch := o.notify
		o.mu.Unlock()
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-ch:
		}
		o.mu.Lock()
	}
}

// Release 让一只织完的蜘蛛回园：把蜘蛛放回池中供复用，不销毁其浏览器。
// 若蜘蛛已失效（浏览器/页面连接关闭导致），则销毁它并释放一个并发名额
// （live--），由后续借出自动新建补上，避免劣化实例被反复复用。
func (o *Orchard[T, S]) Release(s S) error {
	if s == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if s.IsActive() {
		o.idle.PushBack(s)
	} else {
		o.logger.Warn("蜘蛛实例失效，销毁重建")
		_ = s.Close()
		o.live-- // 释放一个并发名额，供后续惰性新建补上
	}
	o.broadcast()
	return nil
}

// Close 关闭整座果园：唤醒所有阻塞等待者，并逐个关闭池中空闲的蜘蛛
// （含各自浏览器与页面）。借出中的蜘蛛由各自的 Release 在发现已关闭后销毁。
func (o *Orchard[T, S]) Close() error {
	o.logger.Info("关闭蜘蛛果园", "size", o.Size())
	o.mu.Lock()
	o.closed = true
	for e := o.idle.Front(); e != nil; e = e.Next() {
		if sp, ok := e.Value.(S); ok && sp != nil {
			_ = sp.Close()
		}
	}
	o.idle.Init()
	o.broadcast()
	o.mu.Unlock()
	return nil
}

// BorrowSpider 借用一只蜘蛛执行 fn，并在结束后自动归还（推荐的安全用法）。
// 等价于 Acquire + defer Release：无论 fn 正常返回还是 panic，蜘蛛都会被放回
// 园中供复用。hijackCfgs 可选，零个即不监听；有监听时由 WithHijack 为本次
// "借-用-还"挂起一套独立监听 goroutine，执行完（含 panic）立即撤除。
func (o *Orchard[T, S]) BorrowSpider(fn func(S) error, hijackCfgs ...config.HijackConfig) error {
	s, err := o.Acquire()
	if err != nil {
		return err
	}
	defer o.Release(s)

	rules, err := spider.RulesFromConfig(hijackCfgs, o.logger)
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

// Crawl 一次性在园内执行一组动作：借一只蜘蛛（可选挂监听）→ 顺序跑完
// actions → 自动归还。是对最常用"抓一次"场景的一站式封装。
func (o *Orchard[T, S]) Crawl(ctx context.Context, actions []action.Action, hijackCfgs ...config.HijackConfig) ([]action.ActionResult, error) {
	var results []action.ActionResult
	err := o.BorrowSpider(func(s S) error {
		var err error
		results, err = s.Crawl(ctx, actions)
		return err
	}, hijackCfgs...)
	return results, err
}

// CrawlConcurrent 一次性在园内并发执行多组动作：借一只蜘蛛（可选挂监听），
// 把各组分别委托给该蜘蛛内不同的网并发执行、每组独占一张页面，跑完自动归还。
// 所有并发网共享同一套可选监听。hijackCfgs 可选。
func (o *Orchard[T, S]) CrawlConcurrent(ctx context.Context, actionGroups [][]action.Action, hijackCfgs ...config.HijackConfig) ([][]action.ActionResult, error) {
	var results [][]action.ActionResult
	err := o.BorrowSpider(func(s S) error {
		var err error
		results, err = s.CrawlConcurrent(ctx, actionGroups)
		return err
	}, hijackCfgs...)
	return results, err
}
