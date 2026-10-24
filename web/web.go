package web

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/go-rod/rod"
)

// Web 网：蜘蛛织出的网，持有并操作单张页面（轻量单位）。
// Spider 只持有浏览器，把抓取任务委托给一张张网去执行。
type Web struct {
	Page      *rod.Page
	humanize  bool
	opTimeout time.Duration
	broken    bool // 已标记为失效（执行出错时探活判定），归还时据此销毁重建
}

// Option 网选项
type Option func(*Web)

// WithHumanize 是否在动作中插入随机人类化延迟。
func WithHumanize(b bool) Option {
	return func(w *Web) { w.humanize = b }
}

// WithOpTimeout 设置单个动作的超时时间。
func WithOpTimeout(d time.Duration) Option {
	return func(w *Web) { w.opTimeout = d }
}

func (w *Web) OpTimeout() time.Duration {
	return w.opTimeout
}

// IsActive 判断网所持页面是否仍然可用（未关闭/崩溃）。
// 供上层（Spider）在复用网前区分失效实例，从而及时销毁并用新实例替换，
// 语义与 Spider.IsActive / Nest.Release 对齐。
func (w *Web) IsActive() bool {
	if w == nil || w.Page == nil {
		return false
	}
	_, err := w.Page.Info()
	return err == nil
}

// MarkBroken 把这张网标记为失效（页面不可再复用），归还时将被销毁重建。
// 仅在"实际执行出错且探活确认页面已死"时由 RunAction 调用。
func (w *Web) MarkBroken() { w.broken = true }

// Broken 报告网是否已被标记失效。纯内存判断，无任何网络往返，
// 供 Release 在热路径上决定复用还是淘汰，替代原先每次归还都发的 Page.Info。
func (w *Web) Broken() bool { return w != nil && w.broken }

// New 新建一张网（Web）。网是持有单张页面的轻量单位，页面在其 Page 字段上；
// Spider 只持有浏览器并把任务委托给网执行；需要在网上设置人类化与超时等
// 行为时通过 Option 注入。
func New(page *rod.Page, opts ...Option) *Web {
	w := &Web{Page: page, opTimeout: 30 * time.Second}
	for _, o := range opts {
		o(w)
	}
	return w
}

func (w *Web) timeoutWrapper[T Action](
	ctx context.Context,
	action T,
	f func(ctx context.Context, action T) error,
) error {
	ctx, cancel := context.WithTimeout(ctx, w.opTimeout)
	defer cancel()
	return f(ctx, action)
}

// timeoutResultWrapper 与 timeoutWrapper 相同，区别在于内部函数返回 (V, error)，
// 用于包装需取回具体值（如 HTML）的超时控制。
func (w *Web) timeoutResultWrapper[T Action, V any](
	ctx context.Context,
	action T,
	f func(ctx context.Context, action T) (V, error),
) (V, error) {
	ctx, cancel := context.WithTimeout(ctx, w.opTimeout)
	defer cancel()
	return f(ctx, action)
}

// RunAction 在网持有的页面上执行单个动作，并把"是否生效"（如输入是否真正写入成功）
// 与"返回内容"（如 HTML/JS 结果）如实记录进结构化的 ActionResult。
func (w *Web) RunAction(ctx context.Context, action Action) (ActionResult, error) {
	actionName := actionName(action)
	var err error
	effective := true
	value := ""
	artifact := ""
	switch a := action.(type) {
	case *Navigate:
		err = w.Navigate(ctx, a)
	case *Click:
		err = w.DoClick(ctx, a)
	case *ClickByAXTree:
		err = w.DoClickByAX(ctx, a)
	case *Input:
		effective, err = w.DoInput(ctx, a) // bool = 输入框是否已真正写入目标文本
	case *InputByAXTree:
		effective, err = w.DoInputByAX(ctx, a)
	case *Scroll:
		err = w.DoScroll(ctx, a)
	case *JavaScript:
		value, err = w.DoJavaScript(ctx, a)
		effective = err == nil && value != "" && value != "undefined"
	case *GetHTML:
		value, err = w.GetHTML(ctx)
		effective = err == nil
	case *GetAXContent:
		value, err = w.GetAXContent(ctx)
		effective = err == nil
	case *GetStructured:
		value, err = w.GetStructured(ctx)
		effective = err == nil
	case *GetContent:
		switch DefaultContentMode(a.Mode) {
		case ContentModeStructured:
			value, err = w.GetStructured(ctx)
		case ContentModeFull:
			value, err = w.GetHTML(ctx)
		case ContentModeAX:
			value, err = w.GetAXContent(ctx)
		default:
			err = fmt.Errorf("未知的 mode=%q,可选:ax|structured|full", a.Mode)
		}
		effective = err == nil
	case *GetInteractive:
		value, err = w.GetInteractives(ctx)
		effective = err == nil
	case *GetPDF:
		// 流式落盘到 tp.Dir，产物路径记录在 ArtifactPath，内存 O(1)。
		artifact, err = w.GetPDF(ctx, a)
		effective = err == nil
	default:
		return ActionResult{}, fmt.Errorf("不支持的动作类型 %T", action)
	}
	if err != nil {
		// 出错时才探活（#12）：若页面确实已失效，就地标记，归还时据此销毁重建，
		// 取代原先"每次归还都发一次 Page.Info"的热路径往返。
		if !w.IsActive() {
			w.MarkBroken()
		}
		return ActionResult{}, err
	}
	return w.ActionResult(ctx, actionName, value, artifact, effective), nil
}

// RunActions 在网持有的页面上依次执行一系列动作，并返回每步动作的结构化结果。
// 它是网对外暴露的总入口：Spider 拿到一张网后，把整组任务直接委托给
// RunActions 执行，执行异常时返回出错前已成功产生的部分结果与错误。
func (w *Web) RunActions(ctx context.Context, actions []Action) ([]ActionResult, error) {
	results := make([]ActionResult, 0, len(actions))
	for _, action := range actions {
		result, err := w.RunAction(ctx, action)
		if err != nil {
			return results, err
		}
		results = append(results, result)
	}
	return results, nil
}

// actionName 返回动作的简短名称（去掉包前缀），用于摘要标记，如 Navigate、DoClick 等。
func actionName(a Action) string {
	return strings.TrimPrefix(fmt.Sprintf("%T", a), "*web.")
}

func (w *Web) navigate(ctx context.Context, action *Navigate) error {
	page := w.Page.Context(ctx)
	if err := page.Navigate(action.URL); err != nil {
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
func (w *Web) Navigate(ctx context.Context, action *Navigate) error {
	return w.timeoutWrapper(ctx, action, w.navigate)
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
