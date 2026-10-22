package web

import (
	"context"

	"github.com/go-rod/rod/lib/proto"
)

// GetAXContent 基于浏览器可访问性树（Accessibility/AX）提取页面内容，输出紧凑 Markdown。
//
// AX 树是浏览器渲染后的语义投影，本身已剔除 script、隐藏
// （display:none / visibility:hidden / aria-hidden）与纯装饰节点（node.Ignored=true）。
// 渲染逻辑（含去重/碎片合并/噪音过滤）由 renderAXContent 承担，本函数仅负责取回 AX 树。
//   - 保留父子宫级结构，正文/列表按嵌套深度缩进；
//   - 按 role 输出 Markdown：heading→# 层级、list→- 、link→[txt](href)、button/input 保留 node_id；
//   - table/grid 还原为 Markdown 表格（表头/分隔线）；
//   - 保留 node_id（即 BackendDOMNodeID，与 get_interactives 的 node_id 同源），
//     使 LLM 拿到内容后可直接用 click_by_node_id / input_by_node_id 交互。
func (w *Web) getAXContent(ctx context.Context, _ *GetAXContent) (string, error) {
	p := w.Page.Context(ctx)
	result, err := getFullAXTree(p)
	if err != nil {
		return "", err
	}

	forest := buildAXTree(result.Nodes)
	return renderAXContent(forest, func(id proto.DOMBackendNodeID) string {
		return resolveHref(p, id)
	}), nil
}

func (w *Web) GetAXContent(ctx context.Context) (string, error) {
	return w.timeoutResultWrapper(ctx, &GetAXContent{}, w.getAXContent)
}
