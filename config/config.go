package config

import "time"

type SpiderConfig struct {
	Humanize         bool          `yaml:"humanize"`
	OperationTimeout time.Duration `yaml:"operation_timeout"`
	PagePoolLimit    int           `yaml:"page_pool_limit"`
	RodConfig
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
	//禁用后台网络(后台标签页挂起的请求不下载资源)
	DisableBackgroundNetworking bool `yaml:"disable_background_networking"`
	//禁用后台定时器节流(后台标签页的 setTimeout/setInterval 不被降频)
	DisableBackgroundTimerThrottling bool `yaml:"disable_background_timer_throttling"`
	//(禁用后台窗口遮挡)
	DisableBackgroundingOccludedWindows bool `yaml:"disable_backgrounding_occluded_windows"`
	//(禁用渲染器后台)
	DisableRendererBackgrounding bool `yaml:"disable_renderer_backgrounding"`
	//(远程调试端口)
	BasicRemoteDebuggingPort int `yaml:"basic_remote_debugging_port"`
	//(开启CDP通信追踪)
	Trace bool `yaml:"trace"`
}

type ModelConfig struct {
	APIKey string `yaml:"api_key"`
}
