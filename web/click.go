package web

import (
	"context"
	"fmt"

	"github.com/LouYuanbo1/spider-nest/action"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// clickDelayMinMs / clickDelayMaxMs：点击前在目标上停留的随机时长（毫秒）
const (
	clickDelayMinMs = 200
	clickDelayMaxMs = 700
)

// clickLikeHuman 模拟真人点击：先把目标滚到可视区并悬停，停留随机时长后再点击。
func clickLikeHuman(el *rod.Element) error {
	if err := el.ScrollIntoView(); err != nil {
		return err
	}
	if err := el.Hover(); err != nil {
		return err
	}
	humanDelay(clickDelayMinMs, clickDelayMaxMs)
	return el.Click(proto.InputMouseButtonLeft, 1)
}

// clickElement 在网持有的页面上对目标元素执行真人化点击
func (w *Web) clickElement(el *rod.Element) error {
	if !w.humanize {
		return el.Click(proto.InputMouseButtonLeft, 1)
	}
	return clickLikeHuman(el)
}

func (w *Web) doClick(ctx context.Context, a *action.Click) error {
	page := w.Page.Context(ctx)
	var el *rod.Element
	var err error
	switch a.Type {
	case action.SelectorTypeCSS:
		el, err = page.Element(a.Selector)
	case action.SelectorTypeXPath:
		el, err = page.ElementX(a.Selector)
	default:
		return fmt.Errorf("未知选择器类型 %q", a.Type)
	}
	if err != nil {
		return fmt.Errorf("未找到元素 %q: %v", a.Selector, err)
	}
	return w.clickElement(el)
}

// DoClick 在网持有的页面上以 CSS 选择器点击
func (w *Web) DoClick(ctx context.Context, a *action.Click) error {
	return w.timeoutWrapper(ctx, a, w.doClick)
}

func (w *Web) doClickByAX(ctx context.Context, a *action.ClickByAXTree) error {
	page := w.Page.Context(ctx)
	var err error
	backendDOMNodeID := a.BackendDOMNodeID
	if backendDOMNodeID == 0 {
		if a.Role == "" && a.Name == "" {
			return fmt.Errorf("ClickByAXTree 缺少定位信息：node_id、role、name 至少提供一个")
		}
		backendDOMNodeID, err = getNodeIDByRoleAndName(page, a.Role, a.Name, a.Nth)
		if err != nil {
			return fmt.Errorf("获取节点 %d 失败: %v", backendDOMNodeID, err)
		}
	}
	el, err := page.ElementFromNode(&proto.DOMNode{BackendNodeID: backendDOMNodeID})
	if err != nil {
		return fmt.Errorf("解析节点 %d 失败: %v", backendDOMNodeID, err)
	}
	return w.clickElement(el)
}

// DoClickByAX 在网持有的页面上通过可访问性语义（role/name）或后端 DOM 节点 ID 精准点击，
// 避免同名元素/选择器歧义
func (w *Web) DoClickByAX(ctx context.Context, a *action.ClickByAXTree) error {
	return w.timeoutWrapper(ctx, a, w.doClickByAX)
}
