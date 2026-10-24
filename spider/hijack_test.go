package spider

import (
	"testing"

	"github.com/LouYuanbo1/spider-nest/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRulesFromConfigMonitor 验证声明式配置编译为真实监听 handler：
// pattern/method 原样保留、每条规则都有非空的监听 handler、缺 pattern 报错。
func TestRulesFromConfigMonitor(t *testing.T) {
	rules, err := RulesFromConfig([]config.HijackConfig{
		{Name: "a", Pattern: "*/api/a*", Method: "GET", Handler: config.HijackHandlerMonitor},
		{Name: "b", Pattern: "*/api/b*", Handler: config.HijackHandlerEcho},
	}, nil)
	require.NoError(t, err)
	require.Len(t, rules, 2)

	assert.Equal(t, "*/api/a*", rules[0].Pattern)
	assert.Equal(t, "GET", rules[0].Method)
	assert.NotNil(t, rules[0].Handler, "monitor 规则应被编译出监听 handler")

	assert.Equal(t, "*/api/b*", rules[1].Pattern)
	assert.NotNil(t, rules[1].Handler)

	// 缺少 pattern 的规则应当报错。
	_, err = RulesFromConfig([]config.HijackConfig{{Name: "x", Method: "GET", Handler: config.HijackHandlerMonitor}}, nil)
	assert.Error(t, err, "缺少 pattern 应返回错误")

	// 缺少/未知 handler 应报错：no default。
	_, err = RulesFromConfig([]config.HijackConfig{{Name: "y", Pattern: "*/api/y*"}}, nil)
	assert.Error(t, err, "缺少 handler 应返回错误")
	_, err = RulesFromConfig([]config.HijackConfig{
		{Name: "z", Pattern: "*/api/z*", Handler: "no-such-handler"},
	}, nil)
	assert.Error(t, err, "未知 handler 应返回错误")
}

// TestBuildHandlerByConfigPreset 验证预设 handler 的选择逻辑：
// 空/monitor 走真实监听，echo 返回 EchoHandler，未知值报错。
func TestBuildHandlerByConfigPreset(t *testing.T) {
	// handler 必填：缺省（空串）应报错，而非隐式回落为监听。
	_, err := buildHandlerByConfig(config.HijackConfig{}, nil)
	assert.Error(t, err, "缺省 handler 应返回错误")

	// monitor 走真实监听，全量采集，无需参数子节点。
	h, err := buildHandlerByConfig(config.HijackConfig{Handler: config.HijackHandlerMonitor}, nil)
	require.NoError(t, err)
	assert.NotNil(t, h, "monitor 应落到内置监听")

	h, err = buildHandlerByConfig(config.HijackConfig{Handler: config.HijackHandlerEcho}, nil)
	require.NoError(t, err)
	assert.NotNil(t, h, "echo 应返回预设的 EchoHandler")

	_, err = buildHandlerByConfig(config.HijackConfig{Handler: "unknown-behavior"}, nil)
	assert.Error(t, err, "未知 handler 值应报错")
}
