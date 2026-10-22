package spider

import (
	"testing"

	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/stretchr/testify/assert"
)

// newEmptyLauncher 构造一个无任何预置旗标的 launcher，
// 用于隔离验证每个 With* 选项函数的效果（New/NewUserMode 自带大量默认旗标）。
func newEmptyLauncher() *launcher.Launcher {
	return &launcher.Launcher{Flags: map[flags.Flag][]string{}}
}

// applyOption 在空 launcher 上应用一个选项函数并返回它。
// bt 用于在子测试中复用同一 launcher 做多项断言。
func applyOption(t *testing.T, opt LauncherOption) *launcher.Launcher {
	t.Helper()
	l := newEmptyLauncher()
	opt(l)
	return l
}

func TestBoolFlag(t *testing.T) {
	t.Run("启用时设置旗标", func(t *testing.T) {
		l := applyOption(t, boolFlag("enable-flag-name", true))
		assert.True(t, l.Has("enable-flag-name"), "enable=true 应设置对应旗标")
	})

	t.Run("禁用时不设置旗标", func(t *testing.T) {
		l := applyOption(t, boolFlag("enable-flag-name", false))
		assert.False(t, l.Has("enable-flag-name"), "enable=false 不应设置对应旗标")
	})
}

// TestCreateLauncher 验证不同 userMode 下能正确选出 launcher，且选项会被应用。
func TestCreateLauncher(t *testing.T) {
	t.Run("userMode=false 使用默认 launcher", func(t *testing.T) {
		l := CreateLauncher(false)
		assert.NotNil(t, l, "CreateLauncher(false) 不应返回 nil")
	})

	t.Run("userMode=true 使用用户模式 launcher", func(t *testing.T) {
		l := CreateLauncher(true)
		assert.NotNil(t, l, "CreateLauncher(true) 不应返回 nil")
	})

	t.Run("应用所有传入的选项", func(t *testing.T) {
		l := CreateLauncher(false,
			WithHeadless(false),
			WithRemoteDebuggingPort(9333),
		)
		assert.NotNil(t, l)
		assert.False(t, l.Has(flags.Headless), "WithHeadless(false) 应移除 headless 旗标")
		assert.Equal(t, "9333", l.Get(flags.RemoteDebuggingPort), "远程调试端口应被设置为 9333")
	})
}

func TestWithUserDataDir(t *testing.T) {
	t.Run("非空目录时设置", func(t *testing.T) {
		l := applyOption(t, WithUserDataDir("/tmp/instance_0"))
		assert.Equal(t, "/tmp/instance_0", l.Get(flags.UserDataDir))
	})

	t.Run("空目录时不设置", func(t *testing.T) {
		l := applyOption(t, WithUserDataDir(""))
		assert.False(t, l.Has(flags.UserDataDir), "空目录不应设置 user-data-dir")
	})
}

func TestWithHeadless(t *testing.T) {
	assert.True(t, applyOption(t, WithHeadless(true)).Has(flags.Headless), "headless=true 应设置 headless")
	assert.False(t, applyOption(t, WithHeadless(false)).Has(flags.Headless), "headless=false 应删除 headless")
}

func TestWithDisableBlinkFeatures(t *testing.T) {
	t.Run("非空特性串时设置", func(t *testing.T) {
		l := applyOption(t, WithDisableBlinkFeatures("AutomationControlled,GetCtlMsgsJS"))
		assert.Equal(t, "AutomationControlled,GetCtlMsgsJS", l.Get("disable-blink-features"))
	})

	t.Run("空串时不设置", func(t *testing.T) {
		l := applyOption(t, WithDisableBlinkFeatures(""))
		assert.False(t, l.Has("disable-blink-features"))
	})
}

// TestBooleanOptions 覆盖所有经由 boolFlag 工厂实现的开关类选项。
func TestBooleanOptions(t *testing.T) {
	cases := []struct {
		name string
		opt  func(bool) LauncherOption
		flag flags.Flag
	}{
		{"WithIncognito", WithIncognito, "incognito"},
		{"WithDisableDevShmUsage", WithDisableDevShmUsage, "disable-dev-shm-usage"},
		{"WithNoSandbox", WithNoSandbox, flags.NoSandbox},
		{"WithDisableBackgroundNetworking", WithDisableBackgroundNetworking, "disable-background-networking"},
		{"WithDisableBackgroundTimerThrottling", WithDisableBackgroundTimerThrottling, "disable-background-timer-throttling"},
		{"WithDisableBackgroundingOccludedWindows", WithDisableBackgroundingOccludedWindows, "disable-backgrounding-occluded-windows"},
		{"WithDisableRendererBackgrounding", WithDisableRendererBackgrounding, "disable-renderer-backgrounding"},
	}

	for _, tc := range cases {
		t.Run(tc.name+"=true 设置旗标", func(t *testing.T) {
			l := applyOption(t, tc.opt(true))
			assert.True(t, l.Has(tc.flag), "%s=true 应设置 %q", tc.name, tc.flag)
		})
		t.Run(tc.name+"=false 不设置旗标", func(t *testing.T) {
			l := applyOption(t, tc.opt(false))
			assert.False(t, l.Has(tc.flag), "%s=false 不应设置 %q", tc.name, tc.flag)
		})
	}
}

func TestWithBin(t *testing.T) {
	t.Run("非空路径时设置", func(t *testing.T) {
		l := applyOption(t, WithBin(`C:\Chrome\chrome.exe`))
		assert.Equal(t, `C:\Chrome\chrome.exe`, l.Get(flags.Bin))
	})

	t.Run("空路径时不设置", func(t *testing.T) {
		l := applyOption(t, WithBin(""))
		assert.False(t, l.Has(flags.Bin))
	})
}

func TestWithWindowSize(t *testing.T) {
	t.Run("宽高均大于 0 时设置", func(t *testing.T) {
		l := applyOption(t, WithWindowSize(1920, 1080))
		assert.Equal(t, "1920,1080", l.Get("window-size"))
	})

	t.Run("任一为 0 时不设置", func(t *testing.T) {
		l := applyOption(t, WithWindowSize(0, 1080))
		assert.False(t, l.Has("window-size"))

		l = applyOption(t, WithWindowSize(1920, 0))
		assert.False(t, l.Has("window-size"))
	})
}

func TestWithUserAgent(t *testing.T) {
	t.Run("非空 UA 时设置", func(t *testing.T) {
		l := applyOption(t, WithUserAgent("Mozilla/5.0 (go-rod)"))
		assert.Equal(t, "Mozilla/5.0 (go-rod)", l.Get("user-agent"))
	})

	t.Run("空 UA 时不设置", func(t *testing.T) {
		l := applyOption(t, WithUserAgent(""))
		assert.False(t, l.Has("user-agent"))
	})
}

func TestWithLeakless(t *testing.T) {
	assert.True(t, applyOption(t, WithLeakless(true)).Has(flags.Leakless), "leakless=true 应设置")
	assert.False(t, applyOption(t, WithLeakless(false)).Has(flags.Leakless), "leakless=false 应删除")
}

func TestWithRemoteDebuggingPort(t *testing.T) {
	l := applyOption(t, WithRemoteDebuggingPort(9222))
	assert.Equal(t, "9222", l.Get(flags.RemoteDebuggingPort))
}
