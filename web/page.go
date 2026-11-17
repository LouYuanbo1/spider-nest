package web

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/LouYuanbo1/spider-nest/action"
	"github.com/LouYuanbo1/spider-nest/hijack"
	"github.com/go-rod/rod"
)

var _ Web = (*Page)(nil)

// Spider 只持有浏览器，把抓取任务委托给一张张网页去执行。
type Page struct {
	page      *rod.Page
	humanize  bool
	opTimeout time.Duration
	broken    bool // 已标记为失效（执行出错时探活判定），归还时据此销毁重建
	logger    *slog.Logger
}

// Option 网选项
type Option func(*Page)

// WithHumanize 是否在动作中插入随机人类化延迟。
func WithHumanize(b bool) Option {
	return func(p *Page) { p.humanize = b }
}

// WithOpTimeout 设置单个动作的超时时间。
func WithOpTimeout(d time.Duration) Option {
	return func(p *Page) { p.opTimeout = d }
}

func (p *Page) OpTimeout() time.Duration {
	return p.opTimeout
}

func (p *Page) WithLogger(logger *slog.Logger) Option {
	return func(p *Page) { p.logger = logger }
}

// IsActive 判断网所持页面是否仍然可用（未关闭/崩溃）。
// 供上层（Spider）在复用网前区分失效实例，从而及时销毁并用新实例替换，
// 语义与 Spider.IsActive / Nest.Release 对齐。
func (p *Page) IsActive() bool {
	if p == nil || p.page == nil {
		return false
	}
	_, err := p.page.Info()
	return err == nil
}

// MarkBroken 把这张网标记为失效（页面不可再复用），归还时将被销毁重建。
// 仅在"实际执行出错且探活确认页面已死"时由 RunAction 调用。
func (p *Page) MarkBroken() { p.broken = true }

// Broken 报告网是否已被标记失效。纯内存判断，无任何网络往返，
// 供 Release 在热路径上决定复用还是淘汰，替代原先每次归还都发的 Page.Info。
func (p *Page) Broken() bool { return p != nil && p.broken }

// ClosePage 关闭网持有的页面（幂等）：支持 nil 接收者与空页面，
// 把外层"判空 + 关闭"的样板收敛到一处，供 Spider 在归还/关停时统一调用。
func (p *Page) ClosePage() {
	if p == nil || p.page == nil {
		return
	}
	_ = p.page.Close()
}

// New 新建一张网（Page，满足契约 Web）。网是持有单张页面的轻量单位，页面在 page 字段上；
// Spider 只持有浏览器并把任务委托给网执行；需要在网上设置人类化与超时等
// 行为时通过 Option 注入。
func New(page *rod.Page, opts ...Option) *Page {
	p := &Page{
		page:      page,
		opTimeout: 30 * time.Second,
		logger:    slog.Default().With("component", "web"),
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

func (p *Page) timeoutWrapper[T action.Action](
	ctx context.Context,
	action T,
	f func(ctx context.Context, action T) error,
) error {
	ctx, cancel := context.WithTimeout(ctx, p.opTimeout)
	defer cancel()
	return f(ctx, action)
}

// timeoutResultWrapper 与 timeoutWrapper 相同，区别在于内部函数返回 (V, error)，
// 用于包装需取回具体值（如 HTML）的超时控制。
func (p *Page) timeoutResultWrapper[T action.Action, V any](
	ctx context.Context,
	action T,
	f func(ctx context.Context, action T) (V, error),
) (V, error) {
	ctx, cancel := context.WithTimeout(ctx, p.opTimeout)
	defer cancel()
	return f(ctx, action)
}

// RunAction 在网持有的页面上执行单个动作，并把"是否生效"（如输入是否真正写入成功）
// 与"返回内容"（如 HTML/JS 结果）如实记录进结构化的 ActionResult。
func (p *Page) RunAction(ctx context.Context, a action.Action) (action.ActionResult, error) {
	actionName := actionName(a)
	var err error
	effective := true
	value := ""
	artifact := ""
	switch a := a.(type) {
	case *action.Navigate:
		err = p.Navigate(ctx, a)
	case *action.Click:
		err = p.DoClick(ctx, a)
	case *action.ClickByAXTree:
		err = p.DoClickByAX(ctx, a)
	case *action.Input:
		effective, err = p.DoInput(ctx, a) // bool = 输入框是否已真正写入目标文本
	case *action.InputByAXTree:
		effective, err = p.DoInputByAX(ctx, a)
	case *action.Scroll:
		err = p.DoScroll(ctx, a)
	case *action.JavaScript:
		value, err = p.DoJavaScript(ctx, a)
		effective = err == nil && value != "" && value != "undefined"
	case *action.GetHTML:
		value, err = p.GetHTML(ctx)
		effective = err == nil
	case *action.GetAXContent:
		value, err = p.GetAXContent(ctx)
		effective = err == nil
	case *action.GetStructured:
		value, err = p.GetStructured(ctx)
		effective = err == nil
	case *action.GetContent:
		switch action.DefaultContentMode(a.Mode) {
		case action.ContentModeStructured:
			value, err = p.GetStructured(ctx)
		case action.ContentModeFull:
			value, err = p.GetHTML(ctx)
		case action.ContentModeAX:
			value, err = p.GetAXContent(ctx)
		default:
			err = fmt.Errorf("未知的 mode=%q,可选:ax|structured|full", a.Mode)
		}
		effective = err == nil
	case *action.GetInteractive:
		value, err = p.GetInteractives(ctx)
		effective = err == nil
	case *action.GetPDF:
		// 流式落盘到 tp.Dir，产物路径记录在 ArtifactPath，内存 O(1)。
		artifact, err = p.GetPDF(ctx, a)
		effective = err == nil
	default:
		return action.ActionResult{}, fmt.Errorf("不支持的动作类型 %T", a)
	}
	if err != nil {
		// 出错时才探活（#12）：若页面确实已失效，就地标记，归还时据此销毁重建，
		// 取代原先"每次归还都发一次 Page.Info"的热路径往返。
		if !p.IsActive() {
			p.MarkBroken()
		}
		return action.ActionResult{}, err
	}
	return p.ActionResult(ctx, actionName, value, artifact, effective), nil
}

// WithHijack 在网持有的页面上挂起一套 page 级拦截，fn 结束即自动撤除。
// 拦截规则为可选变参：规则为空时 fn 原样执行（不启用 Fetch 域、不关缓存）。
// 回调 fn 在前（与 Spider.WithHijack 的 fn 在前约定对齐），意在让 fn 内部任意多轮
// 操作共享同一套监听窗口——这正是它区别于 RunActions"一次调用绑一套"的地方：
// RunActions 每调一次就重建/销毁一次 router，而 WithHijack 把 router 生命周期
// 收敛到 fn 的作用域上，创建一次、结束即撤，无需调用方锁控制或手动 Stop。
//
// fn 会拿到这张网自身，方便把"挂监听 + 紧接着的动作"显式绑在同一窗口内。
// 注意：go-rod 的 Router 一旦 Run 后不应再 Add/Remove handler（源码自带此约束），
// 故规则在 fn 开头一次性注入，fn 内不要试图动态改规则。
func (p *Page) WithHijack(fn func(Web) error, rules ...hijack.HijackRule) error {
	if len(rules) != 0 {
		router, err := hijack.EnableRouter(p.page, rules, p.logger)
		if err != nil {
			return err
		}
		go router.Run()
		defer router.Stop()
	}
	return fn(p)
}

// RunActions 在网持有的页面上依次执行一系列动作，并返回每步动作的结构化结果。
// 它是网对外暴露的总入口：Spider 拿到一张网后，把整组任务直接委托给
// RunActions 执行，执行异常时返回出错前已成功产生的部分结果与错误。
// 本方法只做执行、不承载监听；需要监听时用 WithHijack 包住它即可。
func (p *Page) RunActions(ctx context.Context, actions []action.Action) ([]action.ActionResult, error) {
	results := make([]action.ActionResult, 0, len(actions))
	for _, a := range actions {
		result, err := p.RunAction(ctx, a)
		if err != nil {
			return results, err
		}
		results = append(results, result)
	}
	return results, nil
}

// actionName 返回动作的简短名称（去掉包前缀，如 *action.Navigate → "Navigate"），
// 用于摘要标记，如 Navigate、Click、Input、JavaScript 等。
func actionName(a action.Action) string {
	return strings.TrimPrefix(fmt.Sprintf("%T", a), "*action.")
}

func (p *Page) navigate(ctx context.Context, a *action.Navigate) error {
	page := p.page.Context(ctx)
	if err := page.Navigate(a.URL); err != nil {
		return fmt.Errorf("导航失败: %v", err)
	}
	if err := page.WaitLoad(); err != nil {
		if strings.Contains(err.Error(), "navigated or closed") {
			return nil
		}
		return fmt.Errorf("页面加载等待失败: %v", err)
	}
	return nil
}

// Navigate 在网持有的页面上导航到目标URL并等待页面加载与网络空闲。
func (p *Page) Navigate(ctx context.Context, a *action.Navigate) error {
	return p.timeoutWrapper(ctx, a, p.navigate)
}

// humanDelay 在 [min,max] 毫秒之间随机休眠，模拟人类操作间隔。
// 仅当 humanize 开启时才会真正等待；关闭时立即返回。
func humanDelay(minMs, maxMs int) {
	if maxMs <= minMs {
		time.Sleep(time.Duration(minMs) * time.Millisecond)
		return
	}
	ms := rand.IntN(maxMs-minMs) + minMs
	time.Sleep(time.Duration(ms) * time.Millisecond)
}
