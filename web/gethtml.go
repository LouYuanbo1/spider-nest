package web

import (
	"context"
)

// pageHTML 取出网持有的页面的完整原始 HTML。
func (w *Web) pageHTML(ctx context.Context, _ *GetHTML) (string, error) {
	return w.Page.Context(ctx).HTML()
}

// GetHTML 返回网持有的页面的完整原始 HTML（带超时控制）。
func (w *Web) GetHTML(ctx context.Context) (string, error) {
	return w.timeoutResultWrapper(ctx, &GetHTML{}, w.pageHTML)
}
