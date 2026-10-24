package spider

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/LouYuanbo1/spider-nest/config"
	"github.com/LouYuanbo1/spider-nest/spider/hijack"
	"github.com/LouYuanbo1/spider-nest/web"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// SpiderAPI 蜘蛛的"日常 API 面"：任何能被蜘蛛巢(Nest)当作"一只蜘蛛"对待的抓取单元，
// 都要实现这组方法；未来落地"更聪明的蜘蛛"（如 direct 的直连回放蜘蛛）时，
// 只要补全此契约即可无缝并入同一巢中复用/隔离。
//
// 当前契约以"浏览器驱动"为核心（借网/织动作），对非浏览器的直连蜘蛛而言
// Acquire/Borrow/Crawl 一族并不适用。后续出现非浏览器蜘蛛时，可视情况把
// 契约收敛为更窄的核心再分层，现阶段忠实反映 Weaver 的日常公开行为。
//
// 它刻意独立于常驻监听能力（PersistentHijacker）：DSL 与平时使用只需触碰这套
// 日常 API，不必关心"常驻监听"这种需要主动管理撤销时机的重能力。
type SpiderAPI interface {
	Acquire() (*web.Web, error)
	// AcquireCtx 与 Acquire 语义相同，但支持通过 ctx 取消"池满等待"；
	// 关闭后的蜘蛛也会立即返回 ErrClosed，而非永久阻塞。
	AcquireCtx(ctx context.Context) (*web.Web, error)
	Release(w *web.Web) error
	Borrow(fn func(*web.Web) error) error
	Crawl(ctx context.Context, actions []web.Action) ([]web.ActionResult, error)
	CrawlConcurrent(ctx context.Context, actionGroups [][]web.Action) ([][]web.ActionResult, error)
	Close() error
	IsActive() bool
	// WithHijack 在 fn 执行期间挂起一套监听并自动撤除（一次操作窗口的便捷封装）。
	WithHijack(rules []hijack.HijackRule, fn func(SpiderAPI) error) error
}

// 说明：蜘蛛的"常驻监听"能力归属具体实现，刻意不放进 SpiderAPI 契约。
// 它把监听挂上后可在同一蜘蛛借用期内跨多次操作持续生效，直到调用 stop 才撤，需要
// 调用方主动管理 stop 的时机。正因如此，它不适合作为所有持有 SpiderAPI 者都能触碰的
// 默认入口——由知道具体实现的地方（如 Nest）把该能力捕获成闭包，注入给真正需要的
// 消费者（如 AcquiredSpider）。WithHijack 是面向普通调用方的安全窗口封装：即用即撤。

// PersistentHijacker 是可"常驻监听"的蜘蛛能力契约：EnableHijack 把一组规则以常驻
// 方式挂到蜘蛛上，返回撤销闭包与错误。监听归属蜘蛛生命周期，跨多次操作持续生效，
// 直到调用者主动 stop 才撤——是"持久"而非"永久"：撤销时机始终握在调用方手里。
//
// 它刻意独立于 SpiderAPI 契约：并非所有蜘蛛都支持常驻监听，但凡是支持的（如 *Weaver、
// 以及 Nest 的一切蜘蛛）都以本接口为统一载体。它亦是构成完整 Spider 契约的横切面，供
// 需要完整能力的消费者按需合并引用。
type PersistentHijacker interface {
	EnableHijack(rules []hijack.HijackRule) (stop func(), err error)
}

// Spider 是"能常驻监听的蜘蛛"完整契约：日常 API 面（SpiderAPI）+ 常驻监听能力
// （PersistentHijacker）的合并接口。构造方（如 Nest 与 AcquiredSpider）拿到一只
// 实现了此接口的蜘蛛后，即可在编译期直接、无需断言地调用 EnableHijack——无论
// 日常使用还是常驻监听能力，都唾手可得，无需各自独立引用两个接口。
//
// 它与 Nest 的泛型约束 hijackSpider[T] 呼应：后者在编译期保证巢内蜘蛛必然
// 满足本契约，此处把这一保证收敛成单个可引用接口，供 AcquiredSpider 这类
// 携带蜘蛛的消费者直接使用。
type Spider interface {
	SpiderAPI
	PersistentHijacker
}

// Weaver 织网蜘蛛：最普通的那种浏览器爬虫——持有浏览器实例（*rod.Browser）与网池，
// 是 Spider 契约的基础实现。创意类比：织网蜘蛛(orb-weaver)牢牢守住一张网，
// 捕食源于网，本类也把所有抓取动作收敛在这几张页面上。
//
// 每张页面被封装成一张轻量的 web.Web（"网"）：网才真正持有并操作页面。
// Weaver 只负责启动/持有唯一一个浏览器进程，并通过网池并发管理多张页面，
// 把抓取任务整体委托给 Web 去执行。动作与结果类型统一来自 web 包。
type Weaver struct {
	browser   *rod.Browser
	webs      chan *web.Web            // 网池：桶内以 nil 表示为尚未填实的空槽位
	create    func() (*web.Web, error) // 创建网的函数
	logger    *slog.Logger
	humanize  bool
	opTimeout time.Duration

	mu     sync.Mutex    // 保护 closed 标志读写
	closed bool          // 已关闭：关闭后拒绝再派网、归还网直接关页
	done   chan struct{} // Close 时 close，用于唤醒阻塞中的 AcquireCtx
}

// ErrClosed 表示蜘蛛（网池）已关闭，拒绝再进行派/收网。
var ErrClosed = errors.New("spider: 爬虫已关闭")

// Weaver 完整实现 Spider 契约（日常 API + 常驻监听能力），此处锁死在编译期：
// 若后续 Weaver 的方法集合与 Spider 契约漂移，会在此编译报错，避免带病演进。
var _ Spider = (*Weaver)(nil)

func NewWeaver(cfg *config.SpiderConfig) (*Weaver, error) {
	if cfg.OperationTimeout <= 0 {
		cfg.OperationTimeout = 30 * time.Second
	}

	logger := slog.Default().With("component", "spider")
	// Chrome 136+ 要求远程调试必须搭配非默认的 --user-data-dir，
	// 无论是否 user 模式都必须显式设置，否则启动会被拒绝。
	// 路径不得为""
	if cfg.UserDataDir != "" {
		if err := os.MkdirAll(cfg.UserDataDir, 0755); err != nil {
			return nil, fmt.Errorf("创建实例数据目录失败: %v", err)
		}
	}

	launcherOptions := []LauncherOption{
		WithBin(cfg.Bin),
		WithHeadless(cfg.Headless),
		WithDisableBlinkFeatures(cfg.DisableBlinkFeatures),
		WithIncognito(cfg.Incognito),
		WithDisableDevShmUsage(cfg.DisableDevShmUsage),
		WithNoSandbox(cfg.NoSandbox),
		WithUserAgent(cfg.UserAgent),
		WithLeakless(cfg.Leakless),
		WithDisableBackgroundNetworking(cfg.DisableBackgroundNetworking),
		WithDisableBackgroundTimerThrottling(cfg.DisableBackgroundTimerThrottling),
		WithDisableBackgroundingOccludedWindows(cfg.DisableBackgroundingOccludedWindows),
		WithDisableRendererBackgrounding(cfg.DisableRendererBackgrounding),
		WithRemoteDebuggingPort(cfg.BasicRemoteDebuggingPort),
		WithWindowSize(cfg.DefaultPageWidth, cfg.DefaultPageHeight),
		WithUserDataDir(cfg.UserDataDir),
	}

	launcher := CreateLauncher(cfg.UserMode, launcherOptions...)
	url, err := launcher.Launch()
	if err != nil {
		return nil, fmt.Errorf("启动浏览器失败: %v", err)
	}
	logger.Info("浏览器就绪", "instance", 1, "url", url)

	browser := rod.New().ControlURL(url).Trace(cfg.Trace)
	if err := browser.Connect(); err != nil {
		return nil, fmt.Errorf("连接浏览器失败: %v", err)
	}

	s := &Weaver{
		browser:   browser,
		logger:    logger,
		humanize:  cfg.Humanize,
		opTimeout: cfg.OperationTimeout,
		done:      make(chan struct{}),
	}
	// 网池预填 PagePoolLimit 个 nil 空槽位：取到空槽才新建，创建失败
	// 必须归还空槽，否则池容量永久减一（见 Release/AcquireCtx）。
	if cfg.PagePoolLimit < 1 {
		cfg.PagePoolLimit = 1
	}
	s.webs = make(chan *web.Web, cfg.PagePoolLimit)
	for i := 0; i < cfg.PagePoolLimit; i++ {
		s.webs <- nil
	}
	s.create = s.createWeb

	return s, nil
}

// createWeb 为网池创建一张可用网（Web）：在蜘蛛自己的浏览器实例上新建一张
// 空白标签页作为"网"，并注入蜘蛛的人类化与超时行为配置。
func (s *Weaver) createWeb() (*web.Web, error) {
	page, err := s.browser.Page(proto.TargetCreateTarget{})
	if err != nil {
		return nil, fmt.Errorf("创建页面失败: %v", err)
	}
	return web.New(page,
		web.WithHumanize(s.humanize),
		web.WithOpTimeout(s.opTimeout),
	), nil
}

// AcquireCtx 派一张网：从网池取一张可复用的页面（轻量，封装为 Web），
// 必要时新建，池满时阻塞等待，直到有网被 Release 放回。等待期间支持
// 通过 ctx 取消；蜘蛛关闭后立即返回 ErrClosed，而非永久阻塞（#13）。
// 与 Nest.Acquire（派一只蜘蛛）结构对齐。
func (s *Weaver) AcquireCtx(ctx context.Context) (*web.Web, error) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return nil, ErrClosed
	}

	select {
	case slot := <-s.webs:
		if slot != nil {
			return slot, nil
		}
		// 命中空槽位 → 新建。创建失败必须归还一个空槽位，
		// 否则该槽被消费后不归还，池容量永久减一（#7）。
		created, err := s.create()
		if err != nil {
			s.webs <- nil
			return nil, fmt.Errorf("从网池取网失败: %v", err)
		}
		return created, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.done:
		return nil, ErrClosed
	}
}

// Acquire 派一张网：等价于以 Background 上下文调用 AcquireCtx。
func (s *Weaver) Acquire() (*web.Web, error) {
	return s.AcquireCtx(context.Background())
}

// Release 让一张织完的网回池把网放回池中供复用，不销毁其页面。
// 若网已失效（页面连接关闭导致），则销毁整张网并补建一张新的，
// 避免劣化实例被反复复用到后续任务上。创建失败会归还空槽位避免
// 池容量泄漏；蜘蛛已关闭时直接关页而非塞回已关闭的池。
func (s *Weaver) Release(w *web.Web) error {
	if w == nil {
		return nil
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		// 已关闭：借出的网直接关闭，不再放回（池已被回收）。
		if w.Page != nil {
			_ = w.Page.Close()
		}
		return ErrClosed
	}
	// 仅当执行路径已把网标记为失效（见 RunAction 出错时探活）才重建。
	// 成功路径只读内存标记，不再每次归还都发 Page.Info 探测（#12）。
	if !w.Broken() {
		s.webs <- w
		return nil
	}
	s.logger.Warn("网实例失效，重建中")
	if w.Page != nil {
		_ = w.Page.Close()
	}
	created, err := s.create()
	if err != nil {
		// #7 创建失败也归还空槽位，避免池容量永久减一。
		s.webs <- nil
		return err
	}
	s.webs <- created
	return nil
}

// Borrow 借用一张网执行 fn，并在结束后自动归还（推荐的安全用法）。
// 它等价于 Acquire + defer Release：无论 fn 正常返回还是 panic，
// 网都会被放回池中供复用，从根上避免"借了不还"导致的池占满死锁。
// 需要跨操作/跨 goroutine 长期持有网（如 agent 自动化会话）时，
// 再改用 Acquire/Release。与 Nest.Borrow 结构对齐。
func (s *Weaver) Borrow(fn func(*web.Web) error) error {
	w, err := s.Acquire()
	if err != nil {
		return err
	}
	defer s.Release(w)
	return fn(w)
}

// Crawl 借取一张网，把整组动作委托给那张网顺序执行，并返回每步动作的
// 结构化结果。执行完毕后该网自动放回池中供后续任务复用（而非销毁）；
// 失效实例也会在 Release 时被销毁重建。
func (s *Weaver) Crawl(ctx context.Context, actions []web.Action) ([]web.ActionResult, error) {
	w, err := s.Acquire()
	if err != nil {
		return nil, err
	}
	defer s.Release(w)
	return w.RunActions(ctx, actions)
}

// CrawlConcurrent 把多组动作分别委托给不同网并发执行，每组独占一张页面。
func (s *Weaver) CrawlConcurrent(
	ctx context.Context,
	actionGroups [][]web.Action,
) ([][]web.ActionResult, error) {
	if len(actionGroups) == 0 {
		return nil, nil
	}

	results := make([][]web.ActionResult, len(actionGroups))
	errs := make([]error, len(actionGroups))

	var wg sync.WaitGroup
	for i, actions := range actionGroups {
		wg.Go(
			func() {
				res, err := s.Crawl(ctx, actions)
				results[i] = res
				errs[i] = err
			},
		)
	}
	wg.Wait()

	return results, errors.Join(errs...)
}

// Close 关闭爬虫：标记为已关闭、唤醒阻塞中的 Acquire 令其返回
// ErrClosed，清理池中空闲的网（Web/页面），最后释放浏览器资源。
// 幂等：重复调用直接返回。
func (s *Weaver) Close() error {
	s.logger.Info("关闭爬虫")
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.done) // 唤醒所有阻塞中的 AcquireCtx/Acquire

	// 清空池中空闲的网并关闭其页面；借出中的网由其各自的 Release 在
	// 发现已关闭后自行关页（见 Release）。
	n := len(s.webs)
	for i := 0; i < n; i++ {
		if w := <-s.webs; w != nil && w.Page != nil {
			_ = w.Page.Close()
		}
	}
	s.mu.Unlock()

	if s.browser != nil {
		return s.browser.Close()
	}
	return nil
}

// IsActive 判断蜘蛛所连接的浏览器进程是否仍然可用（未关闭/崩溃）。
// 供上层（Nest）在复用蜘蛛前区分失效实例，从而及时销毁并用新实例替换。
func (s *Weaver) IsActive() bool {
	if s.browser == nil {
		return false
	}
	_, err := s.browser.Version()
	return err == nil
}
