package web

import (
	"context"
	"strings"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/strikethrough"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
	"github.com/LouYuanbo1/spider-nest/action"
	"golang.org/x/net/html"
)

// nonContentTags 是从结果中整体剔除的"无正文内容/技术噪音"标签。
// 它们对 LLM 理解正文语义几乎无帮助，却占用大量 token。
// 不做 nav/header/footer/aside 的静态剔除：这些常含正文级内容
// （如 header 里的 h1 标题），交给 prune 的内容取舍与链接密度判断更准。
var nonContentTags = map[string]struct{}{
	"script": {}, "style": {}, "link": {}, "meta": {}, "noscript": {},
	"svg": {}, "template": {}, "iframe": {}, "object": {}, "embed": {},
	"canvas": {}, "audio": {}, "video": {}, "picture": {}, "source": {},
	"head": {}, "title": {}, "base": {},
}

// attrWhitelist 在剪枝时仅保留的"语义属性"。
// 丢弃 class/id/style/事件属性等对语义理解无益的冗余，大幅压缩体积。
var attrWhitelist = map[string]struct{}{
	"href": {}, "src": {}, "srcset": {}, "alt": {}, "title": {},
	"type": {}, "value": {}, "placeholder": {}, "name": {}, "target": {},
	"rel": {}, "headers": {}, "scope": {}, "colspan": {}, "rowspan": {},
	"datetime": {}, "abbr": {},
}

// semanticTags 是"自身即承载语义"的标签：只要存在（哪怕没文本）也予以保留，
// 因为它们是链接、图片、表单控件、代码等对 LLM 有用的最小语义单元。
var semanticTags = map[string]struct{}{
	"a": {}, "p": {}, "h1": {}, "h2": {}, "h3": {}, "h4": {}, "h5": {}, "h6": {},
	"li": {}, "ul": {}, "ol": {}, "dl": {}, "dt": {}, "dd": {},
	"table": {}, "thead": {}, "tbody": {}, "tfoot": {}, "tr": {}, "td": {}, "th": {},
	"caption": {}, "colgroup": {}, "code": {}, "pre": {}, "blockquote": {},
	"img": {}, "figure": {}, "figcaption": {}, "picture": {},
	"strong": {}, "em": {}, "b": {}, "i": {}, "u": {}, "small": {}, "mark": {},
	"label": {}, "button": {}, "input": {}, "select": {}, "textarea": {}, "option": {},
	"time": {}, "address": {}, "progress": {}, "meter": {}, "details": {}, "summary": {},
	// 补齐的表单/内联语义标签。
	"form": {}, "fieldset": {}, "legend": {}, "abbr": {}, "acronym": {},
	"cite": {}, "q": {}, "samp": {}, "var": {}, "dfn": {}, "kbd": {},
	"ins": {}, "del": {}, "sub": {}, "sup": {}, "main": {}, "article": {},
}

// isHiddenElement 依据静态属性/内联样式判断元素是否隐藏。
// 说明：不交给浏览器计算（开销大），仅做启发式判断，命中率已足够高。
func isHiddenElement(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	for _, a := range n.Attr {
		switch a.Key {
		case "hidden":
			return true
		case "aria-hidden":
			if a.Val == "true" {
				return true
			}
		case "style":
			norm := strings.ReplaceAll(strings.ToLower(a.Val), " ", "")
			if strings.Contains(norm, "display:none") || strings.Contains(norm, "visibility:hidden") {
				return true
			}
		}
	}
	return false
}

// isSkippable 判定一个节点是否应整棵从结果中剔除。
func isSkippable(n *html.Node) bool {
	if n.Type == html.ElementNode {
		if _, drop := nonContentTags[n.Data]; drop {
			return true
		}
		return isHiddenElement(n)
	}
	return false
}

// subtreeStats 统计 n 子树的可见文本长度与其中 <a> 链接锚文本长度。
// 返回 (文本长度, 链接长度)。跳过脚本/样式等已剔除的噪音标签。
func subtreeStats(n *html.Node) (textLen, linkLen int) {
	switch n.Type {
	case html.TextNode:
		t := len(strings.TrimSpace(n.Data))
		return t, 0
	case html.ElementNode:
		if _, drop := nonContentTags[n.Data]; drop {
			return 0, 0
		}
		isLink := n.Data == "a"
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			tt, ll := subtreeStats(c)
			textLen += tt
			if isLink {
				linkLen += tt
			} else {
				linkLen += ll
			}
		}
		return textLen, linkLen
	default:
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			tt, ll := subtreeStats(c)
			textLen += tt
			linkLen += ll
		}
		return textLen, linkLen
	}
}

// isLinkWall 依据链接密度判断一段修剪后的块是否属于"链接墙"（导航/标签云/目录）。
// 参考 justext 的链接密度启发式：当块内可见文本几乎全部来自链接锚文本、且体量
// 足够时，说明它更像一组导航链接而非正文，对 LLM 语义价值低，值得整体剔除。
// 阈值：至少 linkWallMinLen(40) 字符的文本，且链接文本占比 >= linkWallLinkRatio(0.7)。
const (
	linkWallMinLen    = 40
	linkWallLinkRatio = 0.7
)

func isLinkWall(n *html.Node) bool {
	textLen, linkLen := subtreeStats(n)
	if textLen < linkWallMinLen {
		return false
	}
	return float64(linkLen)/float64(textLen) >= linkWallLinkRatio
}

// collapseWS 将连续的空白（含换行、全角空格）压成单个空格，用于文本归一。
func collapseWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// prune 就地修剪 n 的子树，并返回该节点是否携带"有效内容"。
// 策略三层，自底向上：
//   - 文本节点：空白归一；非空文本才算有效内容。
//   - 元素节点：先剔除 Chrome/隐藏节点（黑名单见 nonContentTags），过滤出语义属性
//     （attrWhitelist），再递归修剪子节点；白名单标签（semanticTags，a/p/button/…
//     自身即语义）无论有无子内容都保留；其余外壳（div/span/section…）仅当子树含
//     有效内容且非链接墙（isLinkWall）才保留。
//   - 其余（document 等）：仅递归清理子节点并视为有效。
func prune(n *html.Node) bool {
	switch n.Type {
	case html.TextNode:
		n.Data = collapseWS(n.Data)
		return n.Data != ""
	case html.ElementNode:
		tag := n.Data
		if isSkippable(n) {
			return false
		}
		// 就地过滤属性：只回写 whitelist 内的属性（kept 索引 <= 当前索引，安全）。
		kept := n.Attr[:0]
		for _, a := range n.Attr {
			if _, ok := attrWhitelist[a.Key]; ok {
				kept = append(kept, a)
			}
		}
		n.Attr = kept

		var content bool
		for c := n.FirstChild; c != nil; {
			nxt := c.NextSibling
			if prune(c) {
				content = true
			} else {
				n.RemoveChild(c)
			}
			c = nxt
		}
		if _, sem := semanticTags[tag]; sem {
			return true
		}
		// 外壳若修剪后几乎全是链接（导航/标签云/目录墙），对正文理解价值低，整体剔除。
		if content && isLinkWall(n) {
			return false
		}
		return content
	default:
		// document / ruby / raw 等节点：仅递归清理子节点，不做自身取舍。
		for c := n.FirstChild; c != nil; {
			nxt := c.NextSibling
			if !prune(c) {
				n.RemoveChild(c)
			}
			c = nxt
		}
		return true
	}
}

// GetStructured 返回把当前页面转成**可读 Markdown** 的结构化文本：
// 先对整页做内容级剪枝（去 Chrome/空壳，见 prune），再用 html-to-markdown(v2) 转换为
// Markdown（表格/任务列表/删除线用 GFM 扩展）。相比原始 HTML 更省 token、对 LLM 更友好，
// 同时保留标题、列表、表格、链接等结构。
func (w *Web) getStructured(ctx context.Context, _ *action.GetStructured) (string, error) {
	p := w.Page.Context(ctx)
	raw, err := p.HTML()
	if err != nil {
		return "", err
	}
	doc, err := html.Parse(strings.NewReader(raw))
	if err != nil {
		return "", err
	}
	prune(doc)

	clean := &strings.Builder{}
	if err := html.Render(clean, doc); err != nil {
		return "", err
	}

	// domain 用于把相对链接（/path、./x）转成绝对链接。
	domain := ""
	if info, err := p.Info(); err == nil {
		domain = info.URL
	}

	conv := converter.NewConverter(
		converter.WithPlugins(
			base.NewBasePlugin(),
			commonmark.NewCommonmarkPlugin(),
			table.NewTablePlugin(),
			strikethrough.NewStrikethroughPlugin(),
		),
	)

	mdText, err := conv.ConvertString(clean.String(), converter.WithDomain(domain))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(mdText), nil
}

func (w *Web) GetStructured(ctx context.Context) (string, error) {
	return w.timeoutResultWrapper(ctx, &action.GetStructured{}, w.getStructured)
}
