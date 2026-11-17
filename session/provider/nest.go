package provider

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LouYuanbo1/spider-nest/config"
	"github.com/LouYuanbo1/spider-nest/nest"
	"github.com/LouYuanbo1/spider-nest/spider"
	"github.com/LouYuanbo1/spider-nest/web"
)

var _ WebProvider = (*NestProvider[spider.Weaver, *spider.Weaver])(nil)

// 本文件是 WebProvider 契约的一个"实体实现"：把本地 Nest（Orchard 果园：蜘蛛/浏览器池）
// 适配成 provider.WebProvider。契约（provider.go）与实现（本文件）同处一包，构成
// "约定 + 引用实现"，与"单个蜘蛛后端"等其它实现相平行，均由上层会话管理器消费。

// NestProvider 把（Orchard 果园：蜘蛛池）适配成 WebProvider：
// 编排层只见统一的"借还租约"，不关心里头是一窝蜘蛛。
type NestProvider[T any, S nest.NestSpider[T]] struct {
	picker   nest.IdlePicker[T, S] // 余量感知巢：AcquireIf 原子挑蛛
	busy     atomic.Int64          // 当前成功借出、未归还的租约数
	draining atomic.Bool           // 排空中：拒绝新租约
}

// NewNestProvider 用一座 Orchard 果园构造基于 Nest 的 WebProvider 实现。
// 用 Orchard 而非 Grove：其 AcquireIf 在巢锁内原子完成"按余量筛选+占用"，
// 天然契合本适配器"挑一只有余量的蜘蛛"的诉求。
func NewNestProvider[T any, S nest.NestSpider[T]](o *nest.Orchard[T, S]) *NestProvider[T, S] {
	return &NestProvider[T, S]{picker: o}
}

// NewNestFromConfig 按容量配置 + 蜘蛛工厂直接构造"本地 Nest 后端"的 WebProvider，
// 供各入口（cmd/server、cmd/ai）少走一步无缝接入。
func NewNestFromConfig[T any, S nest.NestSpider[T]](
	cfg config.NestConfig,
	create func() (S, error),
) (*NestProvider[T, S], error) {
	o, err := nest.NewOrchardFromConfig(cfg, create)
	if err != nil {
		return nil, err
	}
	return NewNestProvider(o), nil
}

// lease 把"蜘蛛+网"装配成 WebHandle：成功封装即计 busy；Release 先把网还到所属浏览器、
// 再浏览器还回池，任一失效按各层语义销毁重建。Release 幂等。
func (p *NestProvider[T, S]) lease(sp S, w *web.Page) *WebHandle {
	p.busy.Add(1)
	var once sync.Once
	var releaseErr error
	release := func() error {
		once.Do(func() {
			releaseErr = errors.Join(
				sp.Release(w),
				p.picker.Release(sp),
			)
			p.busy.Add(-1)
		})
		return releaseErr
	}
	return &WebHandle{Web: w, Release: release}
}

// AcquireWeb 借一只"还有余量"的蜘蛛并从中借一张网，返回 WebHandle。
// 排空中直接返回 ErrUnavailable；池满时 AcquireIf 按 ctx 阻塞/取消。
func (p *NestProvider[T, S]) AcquireWeb(ctx context.Context) (*WebHandle, error) {
	if p.draining.Load() {
		return nil, ErrUnavailable
	}

	// AcquireIf 在果园同一把锁内原子完成"按余量筛选 + 占用"：返回的蜘蛛此刻
	// 必有余量且被本适配器独占（已出队）。独占意味着在要网之前，该蜘蛛的网池
	// 余量只会因他人归还而增大、不会减小，故下面的 sp.AcquireCtx 必然立即成功。
	// 无需旧实现那种轮询重试与两级阻塞兜底。
	sp, err := p.picker.AcquireIf(ctx, func(s S) bool {
		return s.WebPoolStats().Available() > 0
	})
	if err != nil {
		return nil, err
	}
	w, err := sp.AcquireCtx(ctx)
	if err != nil {
		_ = p.picker.Release(sp) // 页借失败（极少），蜘蛛放回果园
		return nil, err
	}
	return p.lease(sp, w), nil
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
	return p.picker.Close()
}
