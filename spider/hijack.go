package spider

import (
	"fmt"
	"log/slog"

	"github.com/LouYuanbo1/spider-nest/config"
	"github.com/LouYuanbo1/spider-nest/spider/hijack"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// 本文件只保留"Weaver 侧的 hijack 编排"：
//   - WithHijack：以"借-用-还"为单位，临时挂起一套浏览器级监听、用完即撤；
//   - buildHandlerByConfig / RulesFromConfig：从声明式 config 装配成 hijack 规则。
//
// 其余纯机制（HijackHandler / HijackRule / 三种 Capture / 各工厂 handler）已在
// spider/hijack 叶包中，供 fetch / direct / cmd 等消费者共用而不必背负整个 Spider。

// buildHandlerByConfig 依据配置解析 handler：只支持预设的几种行为（见 config.HijackHandlerType）。
// 预设行为之外的自由逻辑，请直接在代码里构造 hijack.HijackHandler 并通过 WithHijack 传入。
func buildHandlerByConfig(c config.HijackConfig, logger *slog.Logger) (hijack.HijackHandler, error) {
	switch c.Handler {
	case config.HijackHandlerMonitor:
		return hijack.MonitorHandler(logger), nil
	case config.HijackHandlerEcho:
		return hijack.EchoHandler(), nil
	default:
		// 无默认 handler：宁可启动报错，也不让规则带着"未知意图"静默放行。
		return nil, fmt.Errorf("hijack 配置无效：规则 %q 缺少或使用了未知 handler %q（可选 %q/%q）",
			c.Name, c.Handler, config.HijackHandlerMonitor, config.HijackHandlerEcho)
	}
}

// RulesFromConfig 把声明式配置编译成一组合法的 hijack.HijackRule。
// 供 Nest 在借蜘蛛时按需将该规则挂到目标蜘蛛上（见 nest.Borrow）。
// 每条规则按 c.Handler 挑选预设行为编译；无法识别即报错，避免启动时静默放行。
func RulesFromConfig(cfg []config.HijackConfig, logger *slog.Logger) ([]hijack.HijackRule, error) {
	rules := make([]hijack.HijackRule, 0, len(cfg))
	for _, c := range cfg {
		if c.Pattern == "" {
			return nil, fmt.Errorf("hijack 配置无效：规则 %q 缺少 pattern", c.Name)
		}
		handler, err := buildHandlerByConfig(c, logger)
		if err != nil {
			return nil, err
		}
		rules = append(rules, hijack.HijackRule{
			Pattern: c.Pattern,
			Method:  c.Method,
			Handler: handler,
		})
	}
	return rules, nil
}

// WithHijack 为一次"借-用-还"在蜘蛛上挂起一套浏览器级监听，fn 结束即自动撤除。
// 浏览器级拦截对后台/未聚焦标签页同样生效（page 级拦截会因渲染层节流失效），
// 同一次 WithHijack 内的所有网（页面）共享这套规则。
//
// 回调 fn 会拿到蜘蛛本体（本接口自身），意在把"hijack 之后紧接着的动作"与这只
// 蜘蛛显式绑定：如 fn 里再 Borrow/Crawl，读起来就是"在这只蜘蛛上监听并行事"。
//
// 生命周期约束：每次调用都新建一个独立、临时的 HijackRouter——先把全部规则
// Add 齐全，再 Run 启动监听 goroutine，fn 执行完立即 Stop。监听 goroutine 的存亡
// 与本次操作窗口严格一致，绝不会与页面池复用期的高频 Add/Remove 并存。
// 这是因为 go-rod 明确要求"Run 之后不得再 Add handler"，其 handler 切片非并发安全；
// 若把监听做成常驻 router（如旧实现在建蜘蛛时就 go router.Run），复用时再增删
// 规则就会踩这个并发险。规则为空时 fn 原样执行（不启用 Fetch 域、不关缓存）。
//
// ─── 链式风格预览（设计草案，未启用；不影响现有 API）─────────────────────────
//
// 想做成 `s.Hijack(rules...).Borrow(todo)` 的链式调用，难点在 Go：方法链返回的
// 中间构建器必须替调用方兜住"撤销监听 + 归还资源"两件收尾，否则链越好看越容易漏。
// 可行形态大致如下——它是 WithHijack 的另一种"甜语法"，生命周期语义完全一致：
//
//	// HijackBuilder 是"带着监听的一次性作用域"：直到调用终止方法（Borrow/Use/Done）才真正
//	// 装上监听并执行，执行完自动 Stop。链上每次调用都返回自己，便于追加规则或动作。
//	type HijackBuilder struct {
//		s     *Weaver
//		rules []hijack.HijackRule
//	}
//
//	// Hijack 开启一条带监听的链路：只收集规则，暂不生效。
//	func (s *Weaver) Hijack(rules ...hijack.HijackRule) *HijackBuilder {
//		return &HijackBuilder{s: s, rules: rules}
//	}
//
//	// Rule 追加规则（可选，可后置），读起来像"先监听、再加规则"。
//	func (b *HijackBuilder) Rule(rules ...hijack.HijackRule) *HijackBuilder {
//		b.rules = append(b.rules, rules...)
//		return b
//	}
//
//	// Borrow 在监听生效窗口内借一张网执行 fn（fn 结束后撤销监听、还网）。
//	// 这是最贴近现有 Borrow 语义的终止方法。
//	func (b *HijackBuilder) Borrow(fn func(w *web.Web) error) error {
//		return b.s.WithHijack(b.rules, func(Spider) error {
//			return b.s.Borrow(fn)
//		})
//	}
//
//	// Use 在监听窗口内把蜘蛛交给 fn 自行调度（fn 拿蜘蛛再做 Borrow/Crawl）。
//	func (b *HijackBuilder) Use(fn func(Spider) error) error {
//		return b.s.WithHijack(b.rules, fn)
//	}
//
// 调用示例（链式版）：
//
//	err := s.
//		Hijack(
//			hijack.HijackRule{Pattern: "*/api/a*", Handler: hijack.MonitorHandler(log)},
//		).
//		Use(func(sp spider.Spider) error {
//			return sp.Borrow(func(w *web.Web) error { _, e := w.RunAction(ctx, act); return e })
//		})
//
// 当前已启用的是回调版 WithHijack（`fn func(Spider) error`）；上方 HijackBuilder 仅作
// 设计参考。若日后需要真正落地，把它平铺到本包即可，二者可共存。
//
// ─────────────────────────────────────────────────────────────────────────────
func (s *Weaver) WithHijack(rules []hijack.HijackRule, fn func(Spider) error) error {
	if len(rules) == 0 {
		return fn(s)
	}
	router := s.browser.HijackRequests() // 本次专属临时 router（构造即 enable Fetch 域）
	for _, r := range rules {
		if r.Pattern == "" {
			return fmt.Errorf("hijack 规则无效：pattern 不能为空")
		}
		if r.Handler == nil {
			r.Handler = func(*rod.Hijack) {} // 兜底：命中也不填内容，回喂空 200
		}
		if err := router.Add(r.Pattern, proto.NetworkResourceType(""), hijack.HijackWrapper(r, s.logger)); err != nil {
			return fmt.Errorf("注册 hijack 规则 %q(%s) 失败: %v", r.Pattern, r.Method, err)
		}
	}
	go router.Run() // 全部规则 Add 完毕后才启动监听
	defer router.Stop()
	return fn(s)
}
