package provider

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LouYuanbo1/spider-nest/spider"
	"github.com/LouYuanbo1/spider-nest/web"
)

var _ WebProvider = (*SpiderProvider)(nil)

// SpiderProvider 是 WebProvider 契约的又一实体实现：把"单个蜘蛛（多网池）"直接适配成
// WebProvider。它与 NestProvider（整座巢）互补——当只需单浏览器实例时用本实现最小化依赖。
//
// 注意它与 NestProvider 的差异：本实现不独占整只蜘蛛，只在其网池上按需借/还网；
// 并发上限即该蜘蛛的网池余量。会话管理器不区分二者，只认
// WebProvider 接口，故可无缝热替换。
type SpiderProvider struct {
	spd spider.Spider
	// 当前成功借出、未归还的租约数
	busy atomic.Int64
	// 排空中：拒绝新租约
	draining atomic.Bool
}

// NewSpiderProvider 把一只蜘蛛（含其内部网池）适配为 WebProvider。
func NewSpiderProvider(spd spider.Spider) *SpiderProvider {
	return &SpiderProvider{spd: spd}
}

// lease 把借到的网装配成 WebHandle：成功封装即计 busy；Release 把网还回蜘蛛（幂等）。
func (p *SpiderProvider) lease(w *web.Page) *WebHandle {
	p.busy.Add(1)
	var once sync.Once
	var releaseErr error
	release := func() error {
		once.Do(func() {
			releaseErr = p.spd.Release(w) // 失效时蜘蛛网层自行销毁重建，无需本层操心
			p.busy.Add(-1)
		})
		return releaseErr
	}
	return &WebHandle{Web: w, Release: release}
}

// AcquireWeb 从单只蜘蛛的网池借一张网。排空中直接返回 ErrUnavailable；
// 池满时按 ctx 阻塞/取消；蜘蛛已关闭则返回其关闭错误。
func (p *SpiderProvider) AcquireWeb(ctx context.Context) (*WebHandle, error) {
	if p.draining.Load() {
		return nil, ErrUnavailable
	}
	w, err := p.spd.AcquireCtx(ctx)
	if err != nil {
		return nil, err
	}
	return p.lease(w), nil
}

// Drain 置排空位并等待在途租约全部归还，直到 ctx 到期。
func (p *SpiderProvider) Drain(ctx context.Context) error {
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

// Close 硬关停：置排空位并关闭整只蜘蛛（含其浏览器/页面），释放全部资源。
func (p *SpiderProvider) Close() error {
	p.draining.Store(true)
	return p.spd.Close()
}
