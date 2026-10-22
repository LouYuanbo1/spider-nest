package web

import (
	"fmt"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// getFullAXTree 启用 Accessibility 域并取回页面完整 AX 树。
// 供所有需要 AX 树的动作共用（点击/输入定位、内容提取、交互采集），提取了重复的初始化逻辑。
func getFullAXTree(page *rod.Page) (*proto.AccessibilityGetFullAXTreeResult, error) {
	// Chrome 只有在 Accessibility 域启用后才会开始计算页面的 AX 树，
	// 未启用时 getFullAXTree 拿不到完整节点。
	err := proto.AccessibilityEnable{}.Call(page)
	if err != nil {
		return nil, err
	}
	return proto.AccessibilityGetFullAXTree{}.Call(page)
}

// resolveAXNodeID 从页面的完整 AX 树中，按 role（+可选的 name 最佳努力）定位第 nth 个节点。
//
// 不用 Accessibility.queryAXTree：它依赖按需构建好的 AX 子集，未命中/动态页面时经常返回空；
// 改用 getFullAXTree 一次取回全量 AX 节点，在结果里按 role/name 过滤，稳定可靠。
func resolveAXNodeID(
	page *rod.Page,
	role RoleType,
	name string,
	nth int,
) (proto.DOMBackendNodeID, error) {
	fullTree, err := getFullAXTree(page)
	if err != nil {
		return 0, fmt.Errorf("获取完整 AX 树失败: %w", err)
	}

	/*
		// 临时调试：打印非忽略、可桥接到 DOM 节点的去重 role 分布，确认目标元素真实 role。
		{
			roleCount := map[string]int{}
			for _, node := range fullTree.Nodes {
				if node.Ignored || node.BackendDOMNodeID == 0 || node.Role == nil {
					continue
				}
				roleCount[node.Role.Value.Str()]++
			}
			fmt.Println("AX role 分布:", roleCount)
		}
	*/

	// 过滤：跳过被忽略/无法桥接到 DOM 的节点，只保留匹配 role 的。
	var roleMatches []*proto.AccessibilityAXNode
	for _, node := range fullTree.Nodes {
		if node.Ignored || node.BackendDOMNodeID == 0 {
			continue
		}
		if node.Role != nil && node.Role.Value.Str() == string(role) {
			roleMatches = append(roleMatches, node)
		}
	}

	// 若指定了 name：优先精确匹配；没有精确命中则改用 role 全量结果。
	candidates := roleMatches
	if name != "" {
		var named []*proto.AccessibilityAXNode
		for _, n := range roleMatches {
			if n.Name != nil && n.Name.Value.Str() == name {
				named = append(named, n)
			}
		}
		if len(named) > 0 {
			candidates = named
		}
	}

	if len(candidates) <= nth {
		return 0, fmt.Errorf("未找到 role=%q name=%q 的第 %d 个节点，共匹配 %d 个", role, name, nth, len(candidates))
	}
	target := candidates[nth]
	return target.BackendDOMNodeID, nil
}

func getNodeIDByRoleAndName(
	page *rod.Page,
	role RoleType,
	name string,
	nth int,
) (proto.DOMBackendNodeID, error) {
	// 后台/非激活标签页的 AX 树可能不完整，置前以确保其处于激活状态。
	if _, err := page.Activate(); err != nil {
		return 0, fmt.Errorf("页面置前失败: %w", err)
	}

	return resolveAXNodeID(page, role, name, nth)
}
