package config

import "time"

// HijackHandlerType 预设的 hijack handler 行为，通过 HijackConfig.Handler 字段显式选择。
//   - monitor：真实监听，全量采集请求方法/URL/请求头/请求体并放行到真实网络；
//   - echo：把命中请求的方法/URL/query 回喂成 JSON 假响应；
//
// 注意：handler 必填、无默认值。真实爬取常有数据处理，隐式默认"只监听"会掩盖规则
// 真实意图，故缺省一律报错，要求每条规则明确自己要做什么。
// 更自由的逻辑请直接在代码里用 spider.WithHijack + 手写 HijackHandler。
type HijackHandlerType string

const (
	HijackHandlerMonitor HijackHandlerType = "monitor" // 真实监听，全量采集后放行
	HijackHandlerEcho    HijackHandlerType = "echo"    // 回喂 echo JSON
)

// HijackConfig 一条声明式的 hijack 规则，对应 spider/hijack.HijackRule。
// 行为由 Handler 字段选择（见 HijackHandlerType），命中后交给对应 handler 处理。
// 规则通过 nest.Crawl / nest.BorrowSpider 的 hijackCfgs 变参传入（按"借-用-还"
// 窗口生效），亦可手动构造 spider.WithHijack + hijack.HijackRule 以便自定义逻辑。
type HijackConfig struct {
	Name    string `yaml:"name"`    // 规则名，仅用于可读性/日志
	Pattern string `yaml:"pattern"` // URL 匹配模式，如 "*/api/user*"（go-rod 通配语法，* 不跨 /）
	Method  string `yaml:"method"`  // 可选；只对该 HTTP 方法生效，空表示全部

	// Handler 选择预设行为（见 HijackHandlerType），必填且无默认值：缺省会报错。
	Handler HijackHandlerType `yaml:"handler"`
}

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

// NestConfig 描述"一座巢"的容量，是 Nest 与上层管理器（如 server.WebSessionManager）
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
