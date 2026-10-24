package nest

import (
	"errors"
	"log/slog"

	"github.com/LouYuanbo1/spider-nest/config"
	"github.com/LouYuanbo1/spider-nest/spider"
)

// Nest 蜘蛛巢：一座巢(Nest)里住着很多只蜘蛛(spider)。每只蜘蛛都拥有
// 自己独立的浏览器实例与页面池（"网"），因此巢本身维护的是一个
// 蜘蛛池（即浏览器池）：并发上限 = 巢内蜘蛛数。
//
// 复用模型：
//   - 并发上限 = 巢内蜘蛛数（chan 容量即上限）；
//   - Acquire 从池里取一只可复用的蜘蛛（取到 nil 空槽位时才调用
//     createSpider 惰性新建），每只蜘蛛内部再通过其页面池管理多张"网"；
//   - Release 把用过的蜘蛛放回池中供其他任务复用，而非销毁，避免反复
//     启停 Chrome 的开销。
//
// 每只蜘蛛独占一个 *rod.Browser（独立 Chrome 进程，当前统一由
// spider.NewWeaver 创建），蜘蛛内部的页面池再进一步把并发 tab 收敛到
// 各自的进程中，扩展性与隔离性兼具。
//
// 池的实现与 go-rod 的 generic Pool 一致：内部就是一个预填 nil 槽位的
// 有缓冲 chan，取到 nil 就新建。这里不直接复用 rod.Pool，因为它的
// Get/Cleanup 在类型参数（Nest 最初想让 Spider 当泛型参数）下编译不过，
// 且创建失败时还会吞掉通道容量。故用同一思路自行实现。
type Nest struct {
	logger  *slog.Logger
	cfg     *config.SpiderConfig // 巢内所有蜘蛛共用配置（创建/重建蜘蛛时使用）
	spiders chan spider.Spider   // 蜘蛛池：每只蜘蛛独占一个浏览器进程
}

// NewNest 创建一座巢：
//   - cfg：巢内所有蜘蛛共用的配置；
//   - spiderSize：巢内蜘蛛数（并发上限）；
//
// 通道预填 spiderSize 个 nil 槽位（借用 rod.Pool 的做法）：空槽表示"可取
// 出并新建"，因此 Acquire 天然可阻塞——所有槽位都被占用时取不到即等待。
// 浏览器按需惰性启动，只有被 Acquire 取出并新建的蜘蛛才真正拉起 Chrome。
func NewNest(cfg *config.SpiderConfig, spiderSize int) (*Nest, error) {
	if spiderSize <= 0 {
		return nil, errors.New("spiderSize 必须大于 0")
	}
	if cfg == nil {
		return nil, errors.New("cfg 不能为空")
	}

	n := &Nest{
		logger:  slog.Default().With("component", "nest"),
		cfg:     cfg,
		spiders: make(chan spider.Spider, spiderSize),
	}
	for range spiderSize {
		n.spiders <- nil
	}
	return n, nil
}

// Size 返回并发上限（可同时活动的蜘蛛数）。通道容量即信号量，容量即上限。
func (n *Nest) Size() int { return cap(n.spiders) }

// createSpider 为蜘蛛池创建一只蜘蛛：惰性拉起独立的 Chrome 进程。
// 页面池容量按巢配置的 pagesPerSpider 覆盖 cfg.PagePoolLimit（>0 才覆盖）。
// 配置拷贝一块使用，避免把巢级配置的页面池容量改写成全局值。
func (n *Nest) createSpider() (spider.Spider, error) {
	return spider.NewWeaver(n.cfg)
}

// Acquire 派一只蜘蛛：从池中取一个槽位（必要时惰性新建），池满时阻塞等待，
// 直到有蜘蛛被 Release 放回。
func (n *Nest) Acquire() (spider.Spider, error) {
	slot := <-n.spiders // 池满则阻塞，直到有蜘蛛被 Release 放回
	if slot != nil {
		return slot, nil
	}
	created, err := n.createSpider()
	if err != nil {
		n.spiders <- nil // 创建失败，归还空槽位，避免容量泄漏
		return nil, err
	}
	return created, nil
}

// Release 让一只织完的蜘蛛回巢：把蜘蛛放回池中供复用，不销毁其浏览器。
// 若蜘蛛已失效（浏览器/页面连接关闭导致），则销毁它并放回一个空槽位，
// 由后续 Acquire 自动新建补上，避免劣化实例被反复复用到后续任务上。
func (n *Nest) Release(s spider.Spider) error {
	if s == nil {
		return nil
	}
	if s.IsActive() {
		n.spiders <- s
		return nil
	}
	n.logger.Warn("蜘蛛实例失效，销毁重建")
	_ = s.Close()
	n.spiders <- nil
	return nil
}

// Close 关闭整座巢：逐个关闭池中的蜘蛛（含各自浏览器与页面），释放资源。
func (n *Nest) Close() error {
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
func (n *Nest) Borrow(hijackCfg []config.HijackConfig, fn func(spider.Spider) error) error {
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
