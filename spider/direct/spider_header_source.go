package direct

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LouYuanbo1/spider-nest/spider"
	"github.com/LouYuanbo1/spider-nest/spider/hijack"
	"github.com/LouYuanbo1/spider-nest/web"
)

const defaultCaptureTimeout = 30 * time.Second

// SpiderHeaderSource 基于基础织网蜘蛛（spider.SpiderAPI 契约）的"请求头来源"，
// 实现 direct.HeaderSource 接口。借助蜘蛛的浏览器级劫持（hijack）临时监听目标请求，
// 捕获请求头快照后回传，供 direct.Client 走纯 HTTP 直连复用
// （把蜘蛛的浏览器当"头制造机"，造完即撤退）。
//
// 触发方式：对每条目标"注册劫持规则 -> 借网在页面跑 fetch 触发真实请求 ->
// 从回调取回请求头 -> 移除规则"，全程在单 goroutine 顺序完成，避免与
// router 事件循环并发修改 handler（go-rod 的 handler 切片并非并发安全）。
type SpiderHeaderSource struct {
	s spider.SpiderAPI
}

// NewSpiderHeaderSource 基于一只已就绪的基础蜘蛛构造请求头来源。
func NewSpiderHeaderSource(s spider.SpiderAPI) *SpiderHeaderSource {
	return &SpiderHeaderSource{s: s}
}

// Capture 触发一次真实请求（页面内 fetch 目标 URL），从劫持回调里捕获请求头快照。
// 事件循环里的请求头与浏览器真实发出的完全一致（含 cookie/反爬签名等），可直接用于直连。
func (hs *SpiderHeaderSource) Capture(ctx context.Context, t Target) (*hijack.RequestCapture, error) {
	if t.URL == "" {
		return nil, errors.New("Capture 目标 URL 不能为空")
	}

	ch := make(chan *hijack.RequestCapture, 1)

	// 1) 以"借-用-还"为一组，挂起一次性劫持监听：命中目标 URL 且方法一致时
	//    捕获请求头回传。WithHijack 保证监听 goroutine 只在本次窗口存活、用完即撤。
	err := hs.s.WithHijack([]hijack.HijackRule{{
		Pattern: t.URL,
		Method:  t.method(),
		Handler: hijack.RequestHandler(func(c *hijack.RequestCapture) {
			select {
			case ch <- c:
			default: // 已满足请求，后续多余命中直接丢弃
			}
		}),
	}}, func(sp spider.SpiderAPI) error {
		// 2) 借一张网，在其页面内跑一条 fetch 触发真实请求。
		//    用 await 等到请求被真正发出（hijack 在请求 pause 时触发），
		//    而非只把任务抛给事件循环就返回。
		return sp.Borrow(func(w *web.Web) error {
			if _, err := w.RunAction(ctx, &web.JavaScript{JS: fetchScript(t)}); err != nil {
				return err
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}

	// 3) 等待劫持回调把请求头送回。
	timeout := time.After(defaultCaptureTimeout)
	select {
	case cap := <-ch:
		if cap == nil {
			return nil, errors.New("捕获到空的请求头快照")
		}
		return cap, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timeout:
		return nil, fmt.Errorf("在 %v 内未捕获到目标请求 %s", defaultCaptureTimeout, t.URL)
	}
}

// fetchScript 生成页面端 fetch 脚本：await 让请求真实发出（hijack 才能 pause 命中）。
// body 为空视为 GET；否则发起 method + body 的请求。响应会被丢弃，只为触发请求。
func fetchScript(t Target) string {
	if t.Body == "" {
		return fmt.Sprintf("fetch(%q).then(r=>r.status).catch(e=>'fetch-error').then(v=>String(v));", t.URL)
	}
	return fmt.Sprintf("fetch(%q, {method:%q, body:%q}).then(r=>r.status).catch(e=>'fetch-error').then(v=>String(v));",
		t.URL, t.method(), t.Body)
}
