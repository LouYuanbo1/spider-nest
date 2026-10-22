package nest

import (
	"errors"
	"log/slog"

	"github.com/LouYuanbo1/spider-nest/config"
	"github.com/LouYuanbo1/spider-nest/spider"
	"github.com/go-rod/rod"
)

// Nest 蜘蛛巢：一座巢(Nest)里住着很多只蜘蛛(spider)。每只蜘蛛都拥有
// 自己独立的浏览器实例与页面池（"网"），因此巢本身维护的是一个
// 蜘蛛池（即浏览器池）：并发上限 = 巢内蜘蛛数。
//
// 复用模型（对应 rod.Pool[Spider]）：
//   - 并发上限 = 巢内蜘蛛数（rod.NewPool(spiderSize) 预填 spiderSize 个槽位）；
//   - Acquire 从池里取一只可复用的蜘蛛（空池时才调用 create 新建），
//     每只蜘蛛内部再通过其页面池管理多张"网"；
//   - Release 把用过的蜘蛛"放回巢里"供其他任务复用，而非销毁，避免反复
//     启停 Chrome 的开销。
//
// 每只蜘蛛独占一个 *rod.Browser（独立 Chrome 进程），蜘蛛内部的页面池再
// 进一步把并发 tab 收敛到各自的进程中，扩展性与隔离性兼具。
type Nest struct {
	cfg     *config.SpiderConfig // 每只蜘蛛共用的基础配置
	logger  *slog.Logger
	spiders rod.Pool[spider.Spider]        // 蜘蛛池：每只蜘蛛独占一个浏览器进程
	create  func() (*spider.Spider, error) // 创建蜘蛛的函数
}

// NewNest 创建一座巢：spiderSize 决定巢内蜘蛛数（并发上限），
// pagesPerSpider 决定每只蜘蛛各自页面池的容量（以 SpiderConfig 为准注入）。
// 浏览器按需惰性启动，只有被 Acquire 取出的蜘蛛才真正拉起对应的 Chrome 进程。
func NewNest(cfg *config.SpiderConfig, spiderSize, pagesPerSpider int) (*Nest, error) {
	if cfg == nil {
		return nil, errors.New("cfg 不能为空")
	}
	if spiderSize <= 0 {
		return nil, errors.New("spiderSize 必须大于 0")
	}
	if pagesPerSpider <= 0 {
		pagesPerSpider = 1
	}

	n := &Nest{
		cfg:    cfg,
		logger: slog.Default().With("component", "nest"),
	}
	n.spiders = rod.NewPool[spider.Spider](spiderSize)
	n.create = n.createSpider
	return n, nil
}

// Size 返回并发上限（可同时活动的蜘蛛数）。rod.Pool 本质是固定容量的 channel。
func (n *Nest) Size() int { return cap(n.spiders) }

// createSpider 为蜘蛛池创建一只蜘蛛：惰性拉起独立的 Chrome 进程与页面池。
// 页面池容量以巢配置的 pagesPerSpider 为准。
func (n *Nest) createSpider() (*spider.Spider, error) {
	// NewSpider 会负责拉起独立的 Chrome 进程；这里显式覆盖其页面池容量。
	cfg := *n.cfg
	return spider.NewSpider(&cfg)
}

// Acquire 派一只蜘蛛：从池中取一只（必要时新建），池满时阻塞等待，
// 直到有蜘蛛被 Release 放回。
func (n *Nest) Acquire() (*spider.Spider, error) {
	return n.spiders.Get(n.create)
}

// Release 让一只织完的蜘蛛回巢：把蜘蛛放回池中供复用，不销毁其浏览器。
func (n *Nest) Release(s *spider.Spider) error {
	if s == nil {
		return nil
	}
	// 若蜘蛛已失效（浏览器/页面连接关闭导致），则整只蜘蛛销毁并补建一只
	// 新的，避免劣化实例被反复复用到后续任务上。
	if s.IsActive() {
		n.spiders.Put(s)
		return nil
	}
	n.logger.Warn("蜘蛛实例失效，重建中")
	if err := s.Close(); err != nil {
		n.logger.Warn("销毁失效蜘蛛失败", "err", err)
	}
	created, err := n.create()
	if err != nil {
		return err
	}
	n.spiders.Put(created)
	return nil
}

// Close 关闭整座巢：逐个关闭池中的蜘蛛（含各自浏览器与页面），释放资源。
func (n *Nest) Close() error {
	n.logger.Info("关闭蜘蛛巢", "size", n.Size())
	n.spiders.Cleanup(func(s *spider.Spider) { _ = s.Close() })
	return nil
}

// Borrow 借用一只蜘蛛执行 fn，并在结束后自动归还（推荐的安全用法）。
// 它等价于 Acquire + defer Release：无论 fn 正常返回还是 panic，
// 蜘蛛都会被放回巢中供复用，从根上避免"借了不还"导致的池占满死锁。
// 需要跨操作/跨 goroutine 长期持有蜘蛛时，再改用 Acquire/Release。
func (n *Nest) Borrow(fn func(*spider.Spider) error) error {
	s, err := n.Acquire()
	if err != nil {
		return err
	}
	defer n.Release(s)
	return fn(s)
}
