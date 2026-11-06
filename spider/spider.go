package spider

import (
	"context"

	"github.com/LouYuanbo1/spider-nest/action"
	"github.com/LouYuanbo1/spider-nest/spider/hijack"
	"github.com/LouYuanbo1/spider-nest/web"
)

// Spider：任何能被蜘蛛巢(Nest)当作"一只蜘蛛"对待的抓取单元，
// 都要实现这组方法；后续再补充更聪明的蜘蛛类型（如 Portia 直连蜘蛛）时，
// 只要补全此契约即可无缝并入同一巢中复用/隔离。
type Spider interface {
	Acquire() (*web.Web, error)
	// AcquireCtx 与 Acquire 语义相同，但支持通过 ctx 取消"池满等待"；
	// 关闭后的蜘蛛也会立即返回 ErrClosed，而非永久阻塞。
	AcquireCtx(ctx context.Context) (*web.Web, error)
	Release(w *web.Web) error
	// BorrowWeb 借用一张网执行 fn，并在结束后自动归还（网层入口）。
	BorrowWeb(fn func(*web.Web) error) error
	Crawl(ctx context.Context, actions []action.Action) ([]action.ActionResult, error)
	CrawlConcurrent(ctx context.Context, actionGroups [][]action.Action) ([][]action.ActionResult, error)
	Close() error
	IsActive() bool
	// WebPoolStats 返回当前网池占用快照（容量/已借出），供上层选蛛时判断
	// 本蜘蛛是否还有余量能立即借出网（Available>0）而无需阻塞等待。
	WebPoolStats() WebPoolStats
	// WithHijack 在 fn 执行期间挂起一套监听、结束即自动撤除（一次操作窗口的便捷封装）。
	// 回调 fn 在前、监听规则为可选变参：不需要监听时直接传 fn 即可。
	WithHijack(fn func(Spider) error, rules ...hijack.HijackRule) error
}

// WebPoolStats 是一次蜘蛛网池占用情况的只读快照。
//   - Capacity：网池上限（cap(webs)），即本蜘蛛可并发的页面数；
//   - Busy：当前已借出的网数。
//
// Available() 即"还能立即借出几张网"（含空槽可即时新建），供选蛛/限流决策。
type WebPoolStats struct {
	Capacity int
	Busy     int
}

// Available 返回当前无需阻塞即可再借出的网数（= 池余量，空槽也算可即时新建）。
func (w WebPoolStats) Available() int { return w.Capacity - w.Busy }
