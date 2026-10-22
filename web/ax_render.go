package web

import (
	"fmt"
	"slices"
	"strings"

	"github.com/go-rod/rod/lib/proto"
)

// roleName 从 AX 角色节点提取角色名字符串。
func roleName(v *proto.AccessibilityAXValue) string {
	if v == nil {
		return ""
	}
	return v.Value.String()
}

// axString 从 AX 属性节点提取文本值（Name/Value/Description 通用）。
func axString(v *proto.AccessibilityAXValue) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(v.Value.String())
}

// axPropertyValue 在节点属性列表中按名查找值（如 heading 的 level）。
func axPropertyValue(n *proto.AccessibilityAXNode, want string) string {
	for _, p := range n.Properties {
		if p == nil || string(p.Name) != want {
			continue
		}
		return p.Value.Value.String()
	}
	return ""
}

// axTreeNode AX 树节点的树形包装，用于重建父子宫级与表格结构。
type axTreeNode struct {
	node     *proto.AccessibilityAXNode
	children []*axTreeNode
}

// buildAXTree 用 ParentID 把扁平 AX 节点列表重建为森林（多根）。
// ignored 节点保留在树上（用于定位层级），但在输出时会跳过。
//
// 注意：AX 节点的 NodeID 只保证上下文内的稳定与唯一，不具备顺序语义，
// 绝不能按它排序。真正的文档顺序是浏览器 GetFullAXTree 返回时的树遍历
// 顺序（父在前、子在后）。Go 的 map 迭代顺序是随机的，若直接按 map 遍历
// 构建，父子顺序会被打乱且每次运行不一致；因此这里按输入顺序收集节点，
// 再在构建时按它填充父子关系，从而稳定保留浏览器给出的树结构。
func buildAXTree(nodes []*proto.AccessibilityAXNode) []*axTreeNode {
	byID := make(map[proto.AccessibilityAXNodeID]*axTreeNode, len(nodes))
	ordered := make([]*axTreeNode, 0, len(nodes))
	for _, n := range nodes {
		if n == nil {
			continue
		}
		tn := &axTreeNode{node: n}
		byID[n.NodeID] = tn
		ordered = append(ordered, tn)
	}
	var roots []*axTreeNode
	for _, tn := range ordered {
		if tn.node.ParentID == "" {
			roots = append(roots, tn)
			continue
		}
		if parent, ok := byID[tn.node.ParentID]; ok {
			parent.children = append(parent.children, tn)
		} else {
			// 父节点缺失（未返回），当作根，避免节点丢失。
			roots = append(roots, tn)
		}
	}
	return roots
}

// renderAXTable 从 table/grid 节点重建 Markdown 表格。
// 收集 row 及每行的 cell 单元格文本，第一行若为表头（含 columnheader/rowheader）则加分隔线。
func renderAXTable(root *axTreeNode) string {
	var rows []*axTreeNode
	var collect func(n *axTreeNode)
	collect = func(n *axTreeNode) {
		for _, c := range n.children {
			if r := roleName(c.node.Role); r == "row" {
				rows = append(rows, c)
			} else {
				collect(c) // 穿透 rowgroup 等容器
			}
		}
	}
	collect(root)

	var out strings.Builder
	first := true
	for _, row := range rows {
		var cells []string
		isHeader := false
		for _, c := range row.children {
			r := roleName(c.node.Role)
			switch r {
			case "cell", "columnheader", "rowheader":
				txt := axString(c.node.Name)
				if txt == "" {
					txt = " "
				}
				cells = append(cells, txt)
				if r == "columnheader" || r == "rowheader" {
					isHeader = true
				}
			}
		}
		if len(cells) == 0 {
			continue
		}
		if first && isHeader {
			out.WriteString("| ")
			out.WriteString(strings.Join(cells, " | "))
			out.WriteString(" |\n")
			out.WriteString("| ")
			out.WriteString(strings.TrimSuffix(strings.Repeat("--- | ", len(cells)), " | "))
			out.WriteString(" |\n")
			first = false
			continue
		}
		out.WriteString("| ")
		out.WriteString(strings.Join(cells, " | "))
		out.WriteString(" |\n")
	}
	return strings.TrimRight(out.String(), "\n")
}

// renderAXContent 把 AX 森林渲染为紧凑 Markdown。
//
// 去重策略（解决聚合 name 与文本叶子重复输出的问题）：
//   - 结构节点（heading/link/button/input/image/listitem/table）作为独立语义行输出，
//     其聚合 name 已完整表达文本；若其子树再无嵌套结构，则不再下钻子节点；
//   - 其他容器/文本节点：若其子树无任何结构后代（纯文本块），直接输出聚合 name 一行，
//     天然合并被 CSS 打断的文本碎片，且不重复输出子叶子；
//   - 仅当子树含有结构后代（如 listitem 内含链接）时才递归下钻。
//
// 噪音过滤：contentinfo(footer)/banner 地标与 skip-link 整棵跳过，
// navigation 等导航墙按链接密度启发式剔除。
func renderAXContent(forest []*axTreeNode, resolve func(id proto.DOMBackendNodeID) string) string {
	var b strings.Builder
	for _, root := range forest {
		walkAX(root, 0, &b, resolve)
	}
	return strings.TrimRight(b.String(), "\n")
}

// walkAX 深度优先遍历单个 AX 子树并写入 b。
func walkAX(tn *axTreeNode, depth int, b *strings.Builder, resolve func(id proto.DOMBackendNodeID) string) {
	if tn == nil || tn.node == nil {
		return
	}
	n := tn.node
	role := roleName(n.Role)

	// 噪音节点（footer/banner/skip-link）：整棵跳过，不输出也不下钻。
	if isNoiseAX(n) {
		return
	}
	// 隐藏/装饰节点不输出，但保留其在树上的位置，子节点继续前进。
	if n.Ignored {
		for _, c := range tn.children {
			walkAX(c, depth, b, resolve)
		}
		return
	}

	name := axString(n.Name)

	// 表格：整体还原为 Markdown 表格，子节点不再单独遍历。
	if role == "table" || role == "grid" {
		if tbl := renderAXTable(tn); tbl != "" {
			for _, line := range strings.Split(tbl, "\n") {
				writeAXLine(b, depth, line, 0)
			}
		}
		return
	}

	// 结构节点：作为独立语义行输出。聚合 name 已完整表达其文本，
	// 仅当子树中还有别的结构节点（如 listitem 内的链接）时才下钻。
	if isAXStructural(role) {
		writeAXStructural(n, depth, role, name, b, resolve)
		if hasStructuralDescendant(tn) {
			for _, c := range tn.children {
				walkAX(c, depth+1, b, resolve)
			}
		}
		return
	}

	// 非结构容器/文本：整棵无结构后代时，聚合 name 即全部文本，输出一次并停止下钻。
	if hasStructuralDescendant(tn) {
		for _, c := range tn.children {
			walkAX(c, depth, b, resolve)
		}
		return
	}
	if name != "" {
		writeAXLine(b, depth, name, 0)
	}
}

// isNoiseAX 判定节点是否属于应整棵剔除的噪音：footer/banner 地标与 skip-link。
func isNoiseAX(n *proto.AccessibilityAXNode) bool {
	switch roleName(n.Role) {
	case "contentinfo", "banner":
		return true
	}
	// skip-link：跳到主内容的锚点链接，对内容提取无价值。
	if roleName(n.Role) == "link" {
		if href := strings.ToLower(axString(n.Name)); strings.Contains(href, "skip to") {
			return true
		}
	}
	return false
}

// isAXStructural 判断该 role 是否构成独立语义行（需输出聚合 name 并带 node_id）。
func isAXStructural(role string) bool {
	switch role {
	case "heading", "headingWithoutLevel", "link", "button", "image", "img", "listitem",
		"textbox", "searchbox", "combobox", "input":
		return true
	}
	return false
}

// writeAXStructural 按 role 输出结构节点的一行 Markdown。
func writeAXStructural(n *proto.AccessibilityAXNode, depth int, role, name string, b *strings.Builder, resolve func(id proto.DOMBackendNodeID) string) {
	switch {
	case role == "heading" || role == "headingWithoutLevel":
		level := headingDepth(axPropertyValue(n, "level"))
		writeAXLine(b, depth, strings.Repeat("#", level)+" "+name, 0)

	case role == "link":
		href := ""
		if resolve != nil {
			href = resolve(n.BackendDOMNodeID)
		}
		txt := name
		if txt == "" {
			txt = href
		}
		// 链接数量通常远多于真正可交互控件，不叠加 [CLICK] 标记以控制输出体积，但仍保留 node_id 供按节点点击。
		writeAXLine(b, depth, fmt.Sprintf("[%s](%s)", txt, href), n.BackendDOMNodeID)

	case role == "button":
		// 可点击动作标识，与 click_by_ax_tool 对应。
		label := name
		if v := axString(n.Value); v != "" {
			label += " (value:" + v + ")"
		}
		writeAXLine(b, depth, "[BUTTON] "+label, n.BackendDOMNodeID)

	case isInputRole(role):
		// 可输入动作标识，与 input_by_ax_tool 对应。
		label := name
		if v := axString(n.Value); v != "" {
			label += " (value:" + v + ")"
		}
		writeAXLine(b, depth, "[INPUT] "+label, n.BackendDOMNodeID)

	case role == "image" || role == "img":
		writeAXLine(b, depth, "![image: "+name+"]", 0)

	case role == "listitem":
		writeAXLine(b, depth, "- "+name, 0)
	}
}

// hasStructuralDescendant 判断 tn 的子树中是否存在"结构节点（含表格/噪音）"。
// 注意仅检查后代、不含当前节点自身：用于决定一个容器如何下钻——
// 对结构节点判断其子是否还有需独立输出的结构，对纯容器判断其下是否有结构。
func hasStructuralDescendant(tn *axTreeNode) bool {
	if tn == nil {
		return false
	}
	return slices.ContainsFunc(tn.children, subtreeHasStructural)
}

// subtreeHasStructural 判断以 tn 为根的子树（含自身）是否存在结构节点或噪音。
func subtreeHasStructural(tn *axTreeNode) bool {
	if tn == nil || tn.node == nil {
		return false
	}
	if isAXStructural(roleName(tn.node.Role)) || isNoiseAX(tn.node) {
		return true
	}
	return slices.ContainsFunc(tn.children, subtreeHasStructural)
}

// writeAXLine 按缩进深度写入一行内容（缩进 2 空格/层），并附加 node_id 标记。
func writeAXLine(b *strings.Builder, depth int, line string, nodeID proto.DOMBackendNodeID) {
	if line == "" {
		return
	}
	line = strings.Join(strings.Fields(line), " ")
	if nodeID != 0 {
		line += fmt.Sprintf(" (node:%d)", nodeID)
	}
	b.WriteString(strings.Repeat("  ", depth))
	b.WriteString(line)
	b.WriteString("\n")
}

// isInputRole 判断该 role 是否为文本输入类控件。
func isInputRole(role string) bool {
	switch role {
	case "textbox", "searchbox", "combobox", "input":
		return true
	}
	return false
}

// headingDepth 解析 heading 层级字符串为缩进级数（越界归一到安全区间）。
func headingDepth(level string) int {
	if level == "" {
		return 2
	}
	var depth int
	for _, r := range level {
		if r < '0' || r > '9' {
			continue
		}
		depth = depth*10 + int(r-'0')
	}
	if depth < 1 || depth > 6 {
		return 2
	}
	return depth
}
