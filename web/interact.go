package web

import (
	"context"
	"encoding/json/v2"
	"strings"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// Link 页面链接
type Link struct {
	Name   string `json:"name"`
	Href   string `json:"href"`
	NodeID int64  `json:"node_id,omitzero"` // 后端 DOM 节点 ID，用于精准点击/定位
}

// Button 页面按钮
type Button struct {
	Name   string `json:"name"`
	Value  string `json:"value,omitempty"`
	NodeID int64  `json:"node_id,omitzero"` // 后端 DOM 节点 ID，用于精准点击
}

// Input 页面文本输入框
type InputBox struct {
	Name   string `json:"name"`
	Value  string `json:"value,omitempty"`
	NodeID int64  `json:"node_id,omitzero"` // 后端 DOM 节点 ID，用于精准定位输入
}

// Interactives 页面上的可交互元素：链接、按钮与文本输入框
type Interactives struct {
	Links   []Link     `json:"links"`
	Buttons []Button   `json:"buttons"`
	Inputs  []InputBox `json:"inputs"` // 页面上的文本输入框，可通过 node_id 定位用于 input_tool
}

// GetInteractives 遍历可访问性树，采集链接、按钮与文本输入框
func (w *Web) GetInteractives(ctx context.Context) (string, error) {
	p := w.Page.Context(ctx)
	result, err := getFullAXTree(p)
	if err != nil {
		return "", err
	}

	// 一次性批量拉取锚点 href 映射，避免对每个链接单独发一次
	// DOM.describeNode —— 200 个链接原本需 200 次串行 CDP 往返。
	hrefs := anchorHrefs(p)

	var interactives Interactives
	for _, node := range result.Nodes {
		// 过滤掉无可访问性名、以及没有对应 DOM 节点(无法用于交互/location)的节点
		if node.Role == nil || node.Name == nil || node.BackendDOMNodeID == 0 {
			continue
		}

		switch node.Role.Value.String() {
		case "link":
			interactives.Links = append(interactives.Links, Link{
				Name:   node.Name.Value.String(),
				Href:   hrefs[node.BackendDOMNodeID],
				NodeID: int64(node.BackendDOMNodeID),
			})
		case "button":
			btn := Button{
				Name:   node.Name.Value.String(),
				NodeID: int64(node.BackendDOMNodeID),
			}
			// 注意：button 的跳转动作通常是 JS 点击触发，静态 DOM/AX 里没有
			if node.Value != nil {
				btn.Value = node.Value.Value.String()
			}
			interactives.Buttons = append(interactives.Buttons, btn)
		case "textbox", "searchbox", "combobox", "input":
			// 文本输入框：用于 input_by_ax_tool 精准定位
			inp := InputBox{
				Name:   node.Name.Value.String(),
				NodeID: int64(node.BackendDOMNodeID),
			}
			if node.Value != nil {
				inp.Value = node.Value.Value.String()
			}
			interactives.Inputs = append(interactives.Inputs, inp)
		}
	}

	data, err := json.Marshal(interactives)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// anchorHrefs 批量拉取整棵 DOM，收集所有 <a> 节点的 BackendNodeID→href。
// 相比逐链接调用 attrOfNode（每次一次 DOM.describeNode），整棵 DOM 只需
// 一次 CDP 往返即可拿到全部锚点，彻底消除 N 个链接 = N 次串行往返的问题。
func anchorHrefs(p *rod.Page) map[proto.DOMBackendNodeID]string {
	hrefs := make(map[proto.DOMBackendNodeID]string)

	depth := -1 // 拉取整棵子树
	res, err := proto.DOMGetFlattenedDocument{Depth: &depth, Pierce: true}.Call(p)
	if err != nil {
		return hrefs
	}

	var walk func(ns []*proto.DOMNode)
	walk = func(ns []*proto.DOMNode) {
		for _, n := range ns {
			if n == nil {
				continue
			}
			// 锚点节点；Attributes 是扁平数组 [名称1, 值1, 名称2, 值2, ...]
			if n.NodeName == "A" || n.NodeName == "a" {
				attrs := n.Attributes
				for i := 0; i+1 < len(attrs); i += 2 {
					if attrs[i] == "href" {
						hrefs[n.BackendNodeID] = normalizeHref(attrs[i+1])
						break
					}
				}
			}
			if len(n.Children) > 0 {
				walk(n.Children)
			}
			if len(n.ShadowRoots) > 0 {
				walk(n.ShadowRoots)
			}
			if n.ContentDocument != nil {
				walk([]*proto.DOMNode{n.ContentDocument})
			}
			if n.TemplateContent != nil {
				walk([]*proto.DOMNode{n.TemplateContent})
			}
		}
	}
	walk(res.Nodes)
	return hrefs
}

// normalizeHref 规范化 href：协议相对地址 (//host/path) 补成绝对地址。
func normalizeHref(href string) string {
	if strings.HasPrefix(href, "//") {
		return "https:" + href
	}
	return href
}
