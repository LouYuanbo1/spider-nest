package spider

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LouYuanbo1/spider-nest/action"
	"github.com/LouYuanbo1/spider-nest/config"
	"github.com/LouYuanbo1/spider-nest/spider/hijack"
	"github.com/LouYuanbo1/spider-nest/web"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/stealth"
)

var _ Spider = (*Weaver)(nil)

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

	closed    atomic.Bool   // 已关闭：关闭后拒绝再派网、归还网直接关页；只置位不复位
	closeOnce sync.Once     // 保证 Close 的副作用（close(done)/清池/关浏览器）恰好执行一次
	done      chan struct{} // Close 时 close，用于唤醒阻塞中的 AcquireCtx
}

// ErrClosed 表示蜘蛛（网池）已关闭，拒绝再进行派/收网。
var ErrClosed = errors.New("spider: 爬虫已关闭")

// WeaverOption 以"函数式选项"方式配置一只新织网蜘蛛（NewWeaver）。未设置的字段
// 落在 config.SpiderConfig 的零值上；高级启动器参数不在预设项中时，用
// WithConfig 从一整份 *config.SpiderConfig 批量带入配置基线。
type WeaverOption func(*config.SpiderConfig)

// WithConfig 用一整份 *config.SpiderConfig 作为蜘蛛配置基线（YAML 驱动场景）。
// 可与其它 With* 选项叠加：后面的选项会覆盖 WithConfig 带入的同名字段。
func WithConfig(cfg *config.SpiderConfig) WeaverOption {
	return func(c *config.SpiderConfig) {
		if cfg != nil {
			*c = *cfg
		}
	}
}

// WithOperationTimeout 设置单步操作超时（默认 30s）。
func WithOperationTimeout(d time.Duration) WeaverOption {
	return func(c *config.SpiderConfig) { c.OperationTimeout = d }
}

// WithPagePoolLimit 设置网池容量，即单只蜘蛛内可并发的页面数（默认 1）。
func WithPagePoolLimit(n int) WeaverOption {
	return func(c *config.SpiderConfig) { c.PagePoolLimit = n }
}

// WithHumanize 控制在页面操作前是否模拟人类化的随机延迟等待。
func WithHumanize(b bool) WeaverOption {
	return func(c *config.SpiderConfig) { c.Humanize = b }
}

// WithUserMode 使用用户现有浏览器身份（不新建实例）。
func WithUserMode(b bool) WeaverOption {
	return func(c *config.SpiderConfig) { c.UserMode = b }
}

func NewWeaver(opts ...WeaverOption) (Spider, error) {
	var cfg config.SpiderConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
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
	logger.Info("浏览器就绪", "url", url)

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
// 页面经由 stealth.Page 创建：其对每张新页面调用 EvalOnNewDocument 注入 stealth JS，
// 抹平 navigator.webdriver / window.chrome / plugins 等可被反爬识别的自动化特征。
func (s *Weaver) createWeb() (*web.Web, error) {
	page, err := stealth.Page(s.browser)
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
	if s.closed.Load() {
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

// Release 让一张织完的网回池：把网放回池中供复用，不销毁其页面。
// 若网已失效（页面连接关闭导致），则销毁整张网并补建一张新的，
// 避免劣化实例被反复复用到后续任务上。创建失败会归还空槽位避免
// 池容量泄漏；蜘蛛已关闭时直接关页而非塞回已关闭的池。
func (s *Weaver) Release(w *web.Web) error {
	if w == nil {
		return nil
	}
	if s.closed.Load() {
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

// BorrowWeb 借用一张网执行 fn，并在结束后自动归还（推荐的安全用法）。
// 它等价于 Acquire + defer Release：无论 fn 正常返回还是 panic，
// 网都会被放回池中供复用，从根上避免"借了不还"导致的池占满死锁。
// 需要跨操作/跨 goroutine 长期持有网（如 agent 自动化会话）时，
// 再改用 Acquire/Release。与 Nest.BorrowSpider 结构对齐。
func (s *Weaver) BorrowWeb(fn func(*web.Web) error) error {
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
func (s *Weaver) Crawl(ctx context.Context, actions []action.Action) ([]action.ActionResult, error) {
	var results []action.ActionResult
	err := s.BorrowWeb(
		func(w *web.Web) error {
			var err error
			results, err = w.RunActions(ctx, actions)
			return err
		},
	)
	return results, err
}

// CrawlConcurrent 把多组动作分别委托给不同网并发执行，每组独占一张页面。
func (s *Weaver) CrawlConcurrent(
	ctx context.Context,
	actionGroups [][]action.Action,
) ([][]action.ActionResult, error) {
	if len(actionGroups) == 0 {
		return nil, nil
	}

	results := make([][]action.ActionResult, len(actionGroups))
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
	var closeErr error
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		close(s.done) // 唤醒所有阻塞中的 AcquireCtx/Acquire

		// 清空池中空闲的网并关闭其页面；借出中的网由其各自的 Release 在
		// 发现已关闭后自行关页（见 Release）。
		n := len(s.webs)
		for range n {
			if w := <-s.webs; w != nil && w.Page != nil {
				_ = w.Page.Close()
			}
		}

		if s.browser != nil {
			closeErr = s.browser.Close()
		}
	})
	return closeErr
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

// enableRouter 校验并装配一组规则到一把新建的独立 HijackRouter 上（Add 齐即返回，不 Run）。
// 供 WithHijack 装配监听规则时使用；空规则不经此路径（见 WithHijack）。
func (s *Weaver) enableRouter(rules []hijack.HijackRule) (*rod.HijackRouter, error) {
	router := s.browser.HijackRequests()
	for _, r := range rules {
		if r.Pattern == "" {
			return nil, fmt.Errorf("hijack 规则无效：pattern 不能为空")
		}
		if r.Handler == nil {
			r.Handler = func(*rod.Hijack) {} // 兜底：命中也不填内容，回喂空 200
		}
		if err := router.Add(r.Pattern, proto.NetworkResourceType(""), hijack.HijackWrapper(r, s.logger)); err != nil {
			return nil, fmt.Errorf("注册 hijack 规则 %q(%s) 失败: %v", r.Pattern, r.Method, err)
		}
	}
	return router, nil
}

// WithHijack 为一次"借-用-还"在蜘蛛上挂起一套监听，fn 结束即自动撤除。
// 回调 fn 在前、监听规则为可选变参（与 Nest.BorrowSpider 的 fn 在前约定对齐）：
// 规则为空时 fn 原样执行（不启用 Fetch 域、不关缓存）。
// 浏览器级拦截对后台/未聚焦标签页同样生效（page 级拦截会因渲染层节流失效），
// 同一次监听内的所有网（页面）共享这套规则。
//
// 回调 fn 会拿到蜘蛛本体（本接口自身），意在把"hijack 之后紧接着的动作"与这只
// 蜘蛛显式绑定：如 fn 里再 BorrowWeb/Crawl，读起来就是"在这只蜘蛛上监听并行事"。
func (s *Weaver) WithHijack(fn func(Spider) error, rules ...hijack.HijackRule) error {
	if len(rules) != 0 {
		router, err := s.enableRouter(rules)
		if err != nil {
			return err
		}
		go router.Run() // 全部规则 Add 完毕后才启动监听
		defer router.Stop()
	}
	return fn(s)
}
