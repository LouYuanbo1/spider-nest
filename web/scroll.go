package web

import (
	"context"
	"math/rand/v2"

	"github.com/LouYuanbo1/spider-nest/action"
	"github.com/go-rod/rod"
)

// 滚动节奏参数（毫秒）：每一段滚动后的"阅读"停留时间范围
const (
	scrollPauseMinMs = 500
	scrollPauseMaxMs = 1800
)

// scrollLikeHuman 分段式滚动并穿插随机"阅读"停顿，模拟真人边读边往下翻。
// totalY 为总滚动距离（可为负，向上滚动）。
func scrollLikeHuman(p *rod.Page, totalY int) error {
	direction := 1
	if totalY < 0 {
		direction = -1
	}
	remaining := totalY * direction

	// 随机拆成 2~4 段
	segments := rand.IntN(3) + 2
	for segments > 0 && remaining > 0 {
		segments--

		// 每段步长由剩余量打折再加抖动，避免严格的均分（显得机械）
		step := remaining / (segments + 1)
		if step <= 0 {
			step = 1
		}
		if randChance(0.5) {
			step = step + rand.IntN(step+1)/2 + 1
		}
		if step > remaining {
			step = remaining
		}

		if _, err := p.Eval(`(dy) => window.scrollBy(0, dy)`, step*direction); err != nil {
			return err
		}
		remaining -= step

		// 每段滚完后"阅读"一会儿（停留时长随机）
		humanDelay(scrollPauseMinMs, scrollPauseMaxMs)
	}
	return nil
}

// DoScroll 在网持有的页面上滚动，滚动后等待网络空闲以捕获懒加载内容。
// 开启 humanize 时分段滚动并在每段间随机"阅读"停留（见 scrollLikeHuman）。
func (w *Web) doScroll(ctx context.Context, a *action.Scroll) error {
	page := w.Page.Context(ctx)
	// 即时滚动（非 smooth）：后台/非激活标签页 rAF 会被节流，
	// 平滑滚动动画因此不推进；即时改变 scrollTop 则与激活状态无关，规避该问题。
	if !w.humanize || a.ScrollY == 0 {
		_, err := page.Eval(`(dy) => window.scrollBy(0, dy)`, a.ScrollY)
		return err
	}
	return scrollLikeHuman(page, a.ScrollY)
}

func (w *Web) DoScroll(ctx context.Context, a *action.Scroll) error {
	return w.timeoutWrapper(ctx, a, w.doScroll)
}
