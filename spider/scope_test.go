package spider

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/LouYuanbo1/spider-nest/config"
	"github.com/LouYuanbo1/spider-nest/spider/hijack"
	"github.com/LouYuanbo1/spider-nest/web"
)

// stubSpider 是最小可用的 Spider 桩：不做任何真实浏览器行为，只把 Borrow 是否
// 被调用、EnableHijack 是否常驻挂载记录成可断言的状。
type stubSpider struct {
	borrowed  int
	hijackRan bool
}

func (s *stubSpider) Acquire() (*web.Web, error) { return &web.Web{}, nil }

func (s *stubSpider) Release(*web.Web) error { return nil }

func (s *stubSpider) Borrow(fn func(*web.Web) error) error {
	s.borrowed++
	return fn(&web.Web{})
}

func (s *stubSpider) Crawl(ctx context.Context, actions []web.Action) ([]web.ActionResult, error) {
	return nil, nil
}

func (s *stubSpider) CrawlConcurrent(ctx context.Context, groups [][]web.Action) ([][]web.ActionResult, error) {
	return nil, nil
}

func (s *stubSpider) Close() error { return nil }

func (s *stubSpider) IsActive() bool { return true }

func (s *stubSpider) WithHijack(rules []hijack.HijackRule, fn func(SpiderAPI) error) error {
	s.hijackRan = true
	return fn(s)
}

// stubSpider 实现常驻监听：EnableHijack 返回可撤销闭包。
func (s *stubSpider) EnableHijack(rules []hijack.HijackRule) (func(), error) {
	s.hijackRan = true
	return func() {}, nil
}

var _ Spider = (*stubSpider)(nil)

// TestResultsPreservesEachGoGroup 验证：Results 以二重数组按提交序保留每次 Go 的
// 结果组。Go 是异步提交，须在 Release 等待全部后台任务收尾后再断言组数。
func TestResultsPreservesEachGoGroup(t *testing.T) {
	stub := &stubSpider{}
	bs := NewAcquiredSpider(stub, nil)

	bs.Go().Go().Go()                    // 三次 Go（空动作），每次都应成为独立一组
	if err := bs.Release(); err != nil { // Release 内 wg.Wait 等后台全部借完
		t.Fatalf("Release 不应报错，实际: %v", err)
	}
	if stub.borrowed != 3 {
		t.Fatalf("三次 Go 应各借一张网，实际 borrowed=%d", stub.borrowed)
	}
	got := bs.Results()
	if len(got) != 3 {
		t.Fatalf("Results 应有 3 组结果（每次 Go 一组），实际 %d 组", len(got))
	}
}

// TestReleaseCallsRelHookOnce 验证：调用一次 Release 恰好触发一次归还回调。
func TestReleaseCallsRelHookOnce(t *testing.T) {
	stub := &stubSpider{}
	called := 0
	bs := NewAcquiredSpider(stub, func() error { called++; return nil })
	if err := bs.Release(); err != nil {
		t.Fatalf("Release 不应报错，实际: %v", err)
	}
	if called != 1 {
		t.Fatalf("归还回调应恰好调用 1 次，实际 %d", called)
	}
}

// TestErrorsAccumulateUntilRelease 验证：错误累积在句柄内，可由 Err 查看、
// 最终由 Release 一并带回（errors.Join）。
func TestErrorsAccumulateUntilRelease(t *testing.T) {
	bs := NewAcquiredSpider(&stubSpider{}, nil)
	bs.errorf(errors.New("a"))
	bs.errorf(errors.New("b"))
	if err := bs.Err(); err == nil || !strings.Contains(err.Error(), "a") {
		t.Fatalf("Err 应已能取到累积错误，实际: %v", bs.Err())
	}
	got := bs.Release()
	if got == nil {
		t.Fatal("Release 应返回累积错误")
	}
	if !strings.Contains(got.Error(), "a") || !strings.Contains(got.Error(), "b") {
		t.Fatalf("Release 应同时带回 a 与 b，实际: %q", got.Error())
	}
}

// TestGoBorrowsOncePerCall 验证：Go() 每次自动借一张网（内部走 Borrow 一次），
// 两次 Go 借两次；无监听时不会触发 hijack。
func TestGoBorrowsOncePerCall(t *testing.T) {
	stub := &stubSpider{}
	bs := NewAcquiredSpider(stub, nil)

	bs.Go().Go() // 空动作：自动借网→执行→归还
	// Go 异步提交，借网在后台完成，须等 Release（内部 wg.Wait）后才计数齐全。
	if err := bs.Release(); err != nil {
		t.Fatalf("Release 不应报错，实际: %v", err)
	}
	if stub.borrowed != 2 {
		t.Fatalf("两次 Go 应各借一张网，实际 borrowed=%d", stub.borrowed)
	}
	if stub.hijackRan {
		t.Fatal("未声明监听时不应触发 hijack")
	}
}

// TestHijackPersistsAcrossGo 验证：声明在蜘蛛级的监听跨多次 Go 以常驻方式只挂一次，
// 而非每次 Go 都临时重建——监听生命周期与蜘蛛绑定，而不是与单张网页绑定。
func TestHijackPersistsAcrossGo(t *testing.T) {
	stub := &stubSpider{}
	bs := NewAcquiredSpider(stub, nil)

	bs.
		Hijack(config.HijackConfig{Name: "x", Pattern: "*/a*", Method: "POST", Handler: config.HijackHandlerMonitor}).
		Go().Go()

	if !stub.hijackRan {
		t.Fatal("声明监听后应触发 hijack")
	}
	// 常驻模式下 EnableHijack 只挂一次：持有 stopHijack 即证明走的是常驻路径。
	if bs.stopHijack == nil {
		t.Fatal("蜘蛛支持常驻监听，应注册 stopHijack")
	}
	if err := bs.Release(); err != nil {
		t.Fatalf("Release 不应报错，实际: %v", err)
	}
}

// TestHijackLockedAfterFirstGo 验证：首个动作后不得再增删 hijack 规则。
func TestHijackLockedAfterFirstGo(t *testing.T) {
	stub := &stubSpider{}
	bs := NewAcquiredSpider(stub, nil)

	bs.Hijack(config.HijackConfig{Name: "x", Pattern: "*/a*", Method: "POST", Handler: config.HijackHandlerMonitor}).Go()
	bs.Hijack(config.HijackConfig{Name: "y", Pattern: "*/b*", Method: "GET", Handler: config.HijackHandlerMonitor}) // 执行开始后追加应被拒绝
	if err := bs.Release(); err == nil || !strings.Contains(err.Error(), "不可再增删") {
		t.Fatalf("执行开始后追加规则应报错，实际: %v", err)
	}
}

// TestGoOnNilSpiderReportsError 验证：对未借出成功（sp 为 nil）的句柄调用 Go
// 应累积"未借出"错误而非崩溃。
func TestGoOnNilSpiderReportsError(t *testing.T) {
	bs := NewAcquiredSpider(nil, nil)
	bs.Go()
	if err := bs.Release(); err == nil || !strings.Contains(err.Error(), "未成功借出") {
		t.Fatalf("sp 为 nil 时 Go 应报未借出错误，实际: %v", err)
	}
}

// TestReleaseIsIdempotent 验证：Release 幂等——重复调用不会再次触发归还回调。
// 这与"归还即终结、句柄不可复用"的语义呼应，避免把蜘蛛两次放回巢而污染槽位。
func TestReleaseIsIdempotent(t *testing.T) {
	stub := &stubSpider{}
	called := 0
	bs := NewAcquiredSpider(stub, func() error { called++; return nil })

	if err := bs.Release(); err != nil {
		t.Fatalf("首次 Release 不应报错，实际: %v", err)
	}
	if err := bs.Release(); err != nil {
		t.Fatalf("重复 Release 应幂等返回、不报错，实际: %v", err)
	}
	if called != 1 {
		t.Fatalf("归还回调应仅触发 1 次（幂等），实际 %d", called)
	}
}

// TestRulesLockedAfterFirstGoEvenWithoutRules 验证：即使首个 Go 未声明任何监听，
// 执行一旦开始规则同样被锁定——删去旧逻辑"无规则时首 Go 不置位"的漏洞。
func TestRulesLockedAfterFirstGoEvenWithoutRules(t *testing.T) {
	stub := &stubSpider{}
	bs := NewAcquiredSpider(stub, nil)

	bs.Go() // 首个 Go 不带任何监听规则
	bs.Hijack(config.HijackConfig{Name: "x", Pattern: "*/a*", Method: "POST", Handler: config.HijackHandlerMonitor})
	if err := bs.Release(); err == nil || !strings.Contains(err.Error(), "不可再增删") {
		t.Fatalf("执行开始后（即便先前无规则）追加规则应报错，实际: %v", err)
	}
}
