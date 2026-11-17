package config

import (
	"time"
)

type RodConfig struct {
	UserDataDir          string `yaml:"user_data_dir"`
	UserMode             bool   `yaml:"user_mode"`
	Headless             bool   `yaml:"headless"`
	DisableBlinkFeatures string `yaml:"disable_blink_features"`
	Incognito            bool   `yaml:"incognito"`
	DisableDevShmUsage   bool   `yaml:"disable_dev_shm_usage"`
	NoSandbox            bool   `yaml:"no_sandbox"`
	DefaultPageWidth     int    `yaml:"default_page_width"`
	DefaultPageHeight    int    `yaml:"default_page_height"`
	UserAgent            string `yaml:"user_agent"`
	Leakless             bool   `yaml:"leakless"`
	Bin                  string `yaml:"bin"`
	//禁用后台网络（后台标签页挂起的请求不下载资源）
	DisableBackgroundNetworking bool `yaml:"disable_background_networking"`
	//禁用后台定时器节流（后台标签页的 setTimeout/setInterval 不被降频）
	DisableBackgroundTimerThrottling bool `yaml:"disable_background_timer_throttling"`
	//禁用后台窗口遮挡
	DisableBackgroundingOccludedWindows bool `yaml:"disable_backgrounding_occluded_windows"`
	//禁用渲染器后台处理
	DisableRendererBackgrounding bool `yaml:"disable_renderer_backgrounding"`
	//远程调试端口
	BasicRemoteDebuggingPort int `yaml:"basic_remote_debugging_port"`
	//开启 CDP 通信追踪
	Trace bool `yaml:"trace"`
}

type SpiderConfig struct {
	RodConfig        `yaml:",inline"`
	Humanize         bool          `yaml:"humanize"`
	OperationTimeout time.Duration `yaml:"operation_timeout"`
	PagePoolLimit    int           `yaml:"page_pool_limit"`
}

// NestConfig 描述"一座巢"的容量，是 Nest 与上层管理器（如 session.Manager）
// 共用的配置类型。放在 config 包而非 nest 包，是因为依赖方向本就允许：nest 已依赖
// config，config 不依赖 nest，故此处定义可被 nest 直接复用而不会成环。
type NestConfig struct {
	// MaxSpiders 并发上限：巢内可同时活动的蜘蛛（浏览器）实例数。
	// 每只蜘蛛独占一个浏览器进程，故该值即最大并发浏览器数。
	SpiderPoolLimit int `yaml:"spider_pool_limit"`
}

type ModelConfig struct {
	APIKey string `yaml:"api_key"`
}

// MCPConfig 配置 agent 侧工具调用所连接的远端 MCP 服务端点。
// agent 不再直接操纵本地浏览器，而是把每个工具调用作为 tools/call 发给这里指定的 MCP HTTP 服务。
type MCPConfig struct {
	// BaseURL 是 MCP Streamable HTTP 的 JSON-RPC 端点，例如 http://127.0.0.1:8080/mcp。
	BaseURL string `yaml:"base_url"`
}
