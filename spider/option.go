package spider

import (
	"fmt"

	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
)

// LauncherOption 浏览器启动器选项函数
type LauncherOption func(*launcher.Launcher)

// 本文件封装启动一个浏览器实例所需的低层选项：
//   - LauncherOption 以函数式选项模式、类型安全地构造 go-rod 的 launcher 参数；
//   - CreateLauncher 依据 userMode 选择「复用本机用户资料」或「全新临时环境」，
//     并把所有选项逐个应用到启动器。
//
// 每种开关都有对应的 With* 工厂（如 WithHeadless / WithWindowSize / WithUserAgent），
// 上层 WithConfig 依据配置集中挑选并拼接这些选项；本文件只负责“把浏览器拉起来”。

// boolFlag 通用布尔 Chrome flag 选项工厂
func boolFlag(name flags.Flag, enable bool) LauncherOption {
	return func(l *launcher.Launcher) {
		if enable {
			l.Set(name)
		}
	}
}

func CreateLauncher(userMode bool, options ...LauncherOption) *launcher.Launcher {
	var l *launcher.Launcher
	if userMode {
		l = launcher.NewUserMode()
	} else {
		l = launcher.New()
	}
	for _, option := range options {
		option(l)
	}
	return l
}

func WithUserDataDir(dir string) LauncherOption {
	return func(l *launcher.Launcher) {
		if dir != "" {
			l.Set("user-data-dir", dir)
		}
	}
}

func WithHeadless(headless bool) LauncherOption {
	return func(l *launcher.Launcher) {
		l.Headless(headless)
	}
}

func WithDisableBlinkFeatures(features string) LauncherOption {
	return func(l *launcher.Launcher) {
		if features != "" {
			l.Set("disable-blink-features", features)
		}
	}
}

func WithIncognito(incognito bool) LauncherOption { return boolFlag("incognito", incognito) }
func WithDisableDevShmUsage(disable bool) LauncherOption {
	return boolFlag("disable-dev-shm-usage", disable)
}
func WithNoSandbox(noSandbox bool) LauncherOption { return boolFlag("no-sandbox", noSandbox) }
func WithDisableBackgroundNetworking(v bool) LauncherOption {
	return boolFlag("disable-background-networking", v)
}
func WithDisableBackgroundTimerThrottling(v bool) LauncherOption {
	return boolFlag("disable-background-timer-throttling", v)
}
func WithDisableBackgroundingOccludedWindows(v bool) LauncherOption {
	return boolFlag("disable-backgrounding-occluded-windows", v)
}
func WithDisableRendererBackgrounding(v bool) LauncherOption {
	return boolFlag("disable-renderer-backgrounding", v)
}

func WithBin(bin string) LauncherOption {
	return func(l *launcher.Launcher) {
		if bin != "" {
			l.Bin(bin)
		}
	}
}

func WithWindowSize(width, height int) LauncherOption {
	return func(l *launcher.Launcher) {
		if width > 0 && height > 0 {
			l.Set("window-size", fmt.Sprintf("%d,%d", width, height))
		}
	}
}

func WithUserAgent(ua string) LauncherOption {
	return func(l *launcher.Launcher) {
		if ua != "" {
			l.Set("user-agent", ua)
		}
	}
}

func WithLeakless(leakless bool) LauncherOption {
	return func(l *launcher.Launcher) {
		l.Leakless(leakless)
	}
}

func WithRemoteDebuggingPort(port int) LauncherOption {
	return func(l *launcher.Launcher) {
		l.RemoteDebuggingPort(port)
	}
}
