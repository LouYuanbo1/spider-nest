package server

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LouYuanbo1/spider-nest/nest"
	"github.com/LouYuanbo1/spider-nest/web"
)

// 本文件把"会话底层资源的来源"升级为面向云编排的真实接缝：从"叶子池(Nest)"或
// "云端编排 provider"借出可执行浏览器操作所需的资源。
//
// 继承并凝练了此前"两端生命周期闭环 + 归还闭包幂等"的要点，并补上云上缺的三件套：
//   - 令牌解耦：AcquireWeb 不再直接泄漏进程内指针，而返回 WebHandle（Token 可跨 RPC
//     寻址；Web 仅本地/节点内 provider 非空），从而"会话身份"与"底层活页"彻底解耦；
//   - 调度可见性：Stats() 暴露"容量/占用/余量"，让云编排器只按容量决策，不碰蜘蛛；
//   - 优雅排空：Drain() 停止接收新租约、等在途跑完，支撑滚动发布/缩容/故障摘除。

// AcquireOptions 表达一次借租的调度诉求。
//
//	Sticky  会话强身份/粘性选择（固定登录态、独享 IP、代理池、会话亲和等）。
//	        有强身份的会话必须落到特定叶节点——编排器据此路由，对应"方案A"。
//	KeepHot 明示需要持续保活（pin）：不参与管理器后续的"冷提交"回收。
type AcquireOptions struct {
	Sticky  map[string]string
	KeepHot bool
}

// WebHandle 借租的结果：把"跨链路可寻址的令牌"与"本进程内的具体页"绑定。
//
//	Token   可寻址令牌：跨 RPC 交给云端编排器/远端节点；
//	Web     本进程内的页：仅"本地/节点内 provider"非空，远端 provider 恒为 nil；
//	Release 归还闭包：把网还回所属浏览器、再把浏览器还回池；幂等，可安全重复调用。
type WebHandle struct {
	Token   string
	Web     *web.Web
	Release func() error
}

// PoolState 是池占用的一次只读快照，供云编排器"只看容量"地做放置决策。
type PoolState struct {
	Capacity  int
	InUse     int
	Available int
	Ready     bool // 是否可服务新租约（Drain 中 / 已关停时=false）
}

// 错误分类：调度侧据此决定重试 / 退避 / 改派 / 放弃。
var (
	// ErrPoolExhausted 池满：请求可稍后重试或由调度扩容。
	ErrPoolExhausted = errors.New("resource: 池已满,稍后重试或扩容")
	// ErrUnavailable provider 暂不可用（排空/关停中），应改派其它节点。
	ErrUnavailable = errors.New("resource: provider 暂不可用")
	// ErrTokenUnknown 令牌未知或已过期：不可盲目重试。
	ErrTokenUnknown = errors.New("resource: 令牌未知或已过期")
)

// WebProvider 是"会话底层资源"的来源契约。
//
// 设计注记：返回 WebHandle 而非裸 *web.Web，正是为了"编排层与 Nest 层彻底解耦"——
// 编排器眼里只有"各节点剩余容量(Stats)"，Nest 眼里只有"本地蜘蛛"。远端 provider
// 只需实现本接口，即可透明替换本地 Nest，会话管理器的其余逻辑零改动。
type WebProvider interface {
	// AcquireWeb 借一个逻辑会话资源（可能含强身份绑定），按 opts 路由。
	// 池满 / 排空时返回 ErrPoolExhausted / ErrUnavailable（本地池满可阻塞等待）。
	AcquireWeb(ctx context.Context, opts AcquireOptions) (*WebHandle, error)
	// Heartbeat 让"保活中的令牌"续期，避免长任务被误回收。远端 provider 必须实现。
	Heartbeat(ctx context.Context, token string) error
	// Stats 返回当前占用快照，供调度/负载均衡读取。
	Stats() PoolState
	// Drain 停止接收新租约并等在途租约归还完毕，供滚动发布/缩容/故障摘除。
	Drain(ctx context.Context) error
	// Close 硬关停：释放全部底层资源（浏览器进程等）。
	Close() error
}

// NestProvider 把本地 Nest（Grove 蜘蛛/浏览器池）适配成 WebProvider。
//
//	AcquireWeb 优先挑一只有余量的蜘蛛借出网，退化为两级阻塞路径（与旧实现一致）；
//	返回的 handle.Release 先网还到所属浏览器、再浏览器还回池，任一失效按各层语义
//	销毁重建；成功借出才计入 busy，用于 Stats 反映真实在途量。
type NestProvider[T any, S nest.NestSpider[T]] struct {
	nested   *nest.Grove[T, S]
	busy     atomic.Int64 // 当前成功借出、未归还的租约数
	draining atomic.Bool  // 排空中：拒绝新租约（Stats.Ready=false）
}

// NewNestProvider 用一座 Grove 巢构造本地 WebProvider。
func NewNestProvider[T any, S nest.NestSpider[T]](n *nest.Grove[T, S]) WebProvider {
	return &NestProvider[T, S]{nested: n}
}

// Stats 返回 Nest 层级池占用快照：Capacity=巢内蜘蛛数，InUse=成功借出未归还数。
func (p *NestProvider[T, S]) Stats() PoolState {
	busy := p.busy.Load()
	capacity := int64(p.nested.Size())
	avail := capacity - busy
	if avail < 0 {
		avail = 0
	}
	return PoolState{
		Capacity:  int(capacity),
		InUse:     int(busy),
		Available: int(avail),
		Ready:     !p.draining.Load(),
	}
}

// AcquireWeb 从巢借一只蜘蛛（浏览器）再从其上借一张网，返回 WebHandle。
// 池满时按 ctx 阻塞/取消；排空中直接返回 ErrUnavailable。
func (p *NestProvider[T, S]) AcquireWeb(ctx context.Context, opts AcquireOptions) (*WebHandle, error) {
	if p.draining.Load() {
		return nil, ErrUnavailable
	}

	// 优先挑一只有余量的蜘蛛（Available>0 即可立即借出网，不阻塞），避免抢到网全满
	// 的蜘蛛后再干等。有限次尝试后仍抢不到余量，再退化到两级阻塞路径兜底。
	for range 3 {
		sp, err := p.nested.AcquireCtx(ctx)
		if err != nil {
			return nil, err
		}
		if sp.WebPoolStats().Available() <= 0 {
			_ = p.nested.Release(sp)
			continue
		}
		w, err := sp.AcquireCtx(ctx)
		if err != nil {
			_ = p.nested.Release(sp)
			return nil, err
		}
		return p.wrap(sp, w), nil
	}

	sp, err := p.nested.AcquireCtx(ctx)
	if err != nil {
		return nil, err
	}
	w, err := sp.AcquireCtx(ctx)
	if err != nil {
		_ = p.nested.Release(sp)
		return nil, err
	}
	return p.wrap(sp, w), nil
}

// wrap 把"蜘蛛+网"装配成 WebHandle：成功封装即计 busy；Release 先把网还到所属浏览器、
// 再浏览器还回池，任一失效按各层语义销毁重建。Release 幂等。
func (p *NestProvider[T, S]) wrap(sp S, w *web.Web) *WebHandle {
	p.busy.Add(1)
	var once sync.Once
	var releaseErr error
	release := func() error {
		once.Do(func() {
			releaseErr = errors.Join(
				sp.Release(w),
				p.nested.Release(sp),
			)
			p.busy.Add(-1)
		})
		return releaseErr
	}
	return &WebHandle{Token: providerToken(), Web: w, Release: release}
}

// Heartbeat 本地 provider 在进程内天然保活令牌，无远程析构，故为空操作。
// 远端 provider 应在此续期，防止长任务持有的令牌被节点回收。
func (p *NestProvider[T, S]) Heartbeat(ctx context.Context, token string) error {
	return nil
}

// Drain 置排空位并等待在途租约全部归还，直到 ctx 到期。
func (p *NestProvider[T, S]) Drain(ctx context.Context) error {
	p.draining.Store(true)
	for {
		if p.busy.Load() == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("resource: drain 超时,仍有在途租约")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// Close 关闭整座巢，释放全部浏览器进程。
func (p *NestProvider[T, S]) Close() error {
	p.draining.Store(true)
	return p.nested.Close()
}

// tokenCounter 是 providerToken 在随机源不可用时的兜底序列。仅用于保证令牌唯一性，
// 不承载安全语义；正常情况下走 newWebID（crypto/rand）生成 128 位随机令牌。
var tokenCounter uint64

// providerToken 生成一个够用且必成功的进程内令牌：优先 crypto/rand，异常时回退到
// "时间戳+计数器"兜底，避免借租路径上因随机源异常而失败。
func providerToken() string {
	if id, err := newWebID(); err == nil {
		return id
	}
	return fmt.Sprintf("tok-%d-%d", time.Now().UnixNano(), atomic.AddUint64(&tokenCounter, 1))
}
