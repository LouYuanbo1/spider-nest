package web

import (
	"context"

	"github.com/LouYuanbo1/spider-nest/action"
)

// pageHTML 取出网持有的页面的完整原始 HTML。
func (w *Web) pageHTML(ctx context.Context, _ *action.GetHTML) (string, error) {
	return w.Page.Context(ctx).HTML()
}

// GetHTML 返回网持有的页面的完整原始 HTML（带超时控制）。
func (w *Web) GetHTML(ctx context.Context) (string, error) {
	return w.timeoutResultWrapper(ctx, &action.GetHTML{}, w.pageHTML)
}
