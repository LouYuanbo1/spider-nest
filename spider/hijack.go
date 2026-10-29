package spider

import (
	"fmt"
	"log/slog"

	"github.com/LouYuanbo1/spider-nest/config"
	"github.com/LouYuanbo1/spider-nest/spider/hijack"
)

// 本文件只保留"Weaver 侧的 hijack 编排"：
//   - WithHijack：以"借-用-还"为单位，临时挂起一套浏览器级监听、用完即撤；
//   - enableRouter / buildHandlerByConfig / RulesFromConfig：装配合法 hijack 规则。
//
// 其余纯机制（HijackHandler / HijackRule / 三种 Capture / 各工厂 handler）已在
// spider/hijack 叶包中，供 crawl / 各类蜘蛛 / cmd 等消费者共用而不必背负整个 Spider。

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
// 供 Nest 在借蜘蛛时按需将该规则挂到目标蜘蛛上（见 nest.BorrowSpider）。
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
