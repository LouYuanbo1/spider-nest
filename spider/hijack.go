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

// WithHijack 为一次"借-用-还"挂起一套浏览器级监听，fn 结束即自动撤除。
// 浏览器级拦截对后台/未聚焦标签页同样生效（page 级拦截会因渲染层节流失效），
// 同一次 WithHijack 内的所有网（页面）共享这套规则。
//
// 生命周期约束：每次调用都新建一个独立、临时的 HijackRouter——先把全部规则
// Add 齐全，再 Run 启动监听 goroutine，fn 执行完立即 Stop。监听 goroutine 的存亡
// 与本次操作窗口严格一致，绝不会与页面池复用期的高频 Add/Remove 并存。
// 这是因为 go-rod 明确要求"Run 之后不得再 Add handler"，其 handler 切片非并发安全；
// 若把监听做成常驻 router（如旧实现在建蜘蛛时就 go router.Run），复用时再增删
// 规则就会踩这个并发险。规则为空时 fn 原样执行（不启用 Fetch 域、不关缓存）。
func (s *Weaver) WithHijack(rules []hijack.HijackRule, fn func() error) error {
	if len(rules) == 0 {
		return fn()
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
	return fn()
}
