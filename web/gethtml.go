package web

import (
	"context"

	"github.com/LouYuanbo1/spider-nest/action"
)

// pageHTML 取出网持有的页面的完整原始 HTML。
func (p *Page) pageHTML(ctx context.Context, _ *action.GetHTML) (string, error) {
	return p.page.Context(ctx).HTML()
}

// GetHTML 返回网持有的页面的完整原始 HTML（带超时控制）。
func (p *Page) GetHTML(ctx context.Context) (string, error) {
	return p.timeoutResultWrapper(ctx, &action.GetHTML{}, p.pageHTML)
}
