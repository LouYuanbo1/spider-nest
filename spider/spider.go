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
	"github.com/LouYuanbo1/spider-nest/web"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// Spider 蜘蛛：持有浏览器实例（*rod.Browser）与网池。
//
// 每张页面被封装成一张轻量的 web.Web（"网"）：网才真正持有并操作页面。
// Spider 只负责启动/持有唯一一个浏览器进程，并通过网池并发管理多张页面，
// 把抓取任务整体委托给 Web 去执行。动作与结果类型统一来自 web 包。
type Spider struct {
	browser   *rod.Browser
	webs      rod.Pool[web.Web]        // 网池：每张网持有一张页面
	create    func() (*web.Web, error) // 创建网的函数
	logger    *slog.Logger
	humanize  bool
	opTimeout time.Duration
}

func NewSpider(cfg *config.SpiderConfig) (*Spider, error) {
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

	s := &Spider{
		browser:   browser,
		logger:    logger,
		humanize:  cfg.Humanize,
		opTimeout: cfg.OperationTimeout,
	}
	s.webs = rod.NewPool[web.Web](cfg.PagePoolLimit)
	s.create = s.createWeb
	return s, nil
}

// createWeb 为网池创建一张可用网（Web）：在蜘蛛自己的浏览器实例上新建一张
// 空白标签页作为"网"，并注入蜘蛛的人类化与超时行为配置。
func (s *Spider) createWeb() (*web.Web, error) {
	page, err := s.browser.Page(proto.TargetCreateTarget{})
	if err != nil {
		return nil, fmt.Errorf("创建页面失败: %v", err)
	}
	return web.New(page,
		web.WithHumanize(s.humanize),
		web.WithOpTimeout(s.opTimeout),
	), nil
}

// Acquire 派一张网：从网池取一张可复用的页面（轻量，封装为 Web），
// 必要时新建，池满时阻塞等待，直到有网被 Release 放回。
// 与 Nest.Acquire（派一只蜘蛛）结构对齐。
func (s *Spider) Acquire() (*web.Web, error) {
	w, err := s.webs.Get(s.create)
	if err != nil {
		return nil, fmt.Errorf("从网池取网失败: %v", err)
	}
	return w, nil
}

// Release 让一张织完的网回池：把网放回池中供复用，不销毁其页面。
// 若网已失效（页面连接关闭导致），则销毁整张网并补建一张新的，
// 避免劣化实例被反复复用到后续任务上。与 Nest.Release 结构对齐。
func (s *Spider) Release(w *web.Web) error {
	if w == nil {
		return nil
	}
	if w.IsActive() {
		s.webs.Put(w)
		return nil
	}
	s.logger.Warn("网实例失效，重建中")
	if w.Page != nil {
		_ = w.Page.Close()
	}
	created, err := s.create()
	if err != nil {
		return err
	}
	s.webs.Put(created)
	return nil
}

// Borrow 借用一张网执行 fn，并在结束后自动归还（推荐的安全用法）。
// 它等价于 Acquire + defer Release：无论 fn 正常返回还是 panic，
// 网都会被放回池中供复用，从根上避免"借了不还"导致的池占满死锁。
// 需要跨操作/跨 goroutine 长期持有网（如 agent 自动化会话）时，
// 再改用 Acquire/Release。与 Nest.Borrow 结构对齐。
func (s *Spider) Borrow(fn func(*web.Web) error) error {
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
func (s *Spider) Crawl(ctx context.Context, actions []web.Action) ([]web.ActionResult, error) {
	w, err := s.Acquire()
	if err != nil {
		return nil, err
	}
	defer s.Release(w)
	return w.RunActions(ctx, actions)
}

// CrawlConcurrent 把多组动作分别委托给不同网并发执行，每组独占一张页面。
func (s *Spider) CrawlConcurrent(
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

// Close 关闭爬虫：清理蜘蛛持有的所有网（Web/页面）并释放浏览器资源。
func (s *Spider) Close() error {
	s.logger.Info("关闭爬虫")
	s.webs.Cleanup(func(w *web.Web) {
		if w != nil && w.Page != nil {
			_ = w.Page.Close()
		}
	})
	if s.browser != nil {
		return s.browser.Close()
	}
	return nil
}

// IsActive 判断蜘蛛所连接的浏览器进程是否仍然可用（未关闭/崩溃）。
// 供上层（Nest）在复用蜘蛛前区分失效实例，从而及时销毁并用新实例替换。
func (s *Spider) IsActive() bool {
	if s.browser == nil {
		return false
	}
	_, err := s.browser.Version()
	return err == nil
}
