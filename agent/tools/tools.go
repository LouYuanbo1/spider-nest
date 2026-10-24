package tools

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/LouYuanbo1/spider-nest/web"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

// debugTool 包装工具执行函数：将调用参数与返回值打印到终端，便于调试。
// timeout 为每次工具调用的最大执行时长：到点仍未返回时，将 ctx 取消并回传超时错误，
// 让 LLM 反思后重试 / 调整参数 / 换方案，而不是无限卡死或中断整次运行。
// 不影响工具本身的行为，仅在执行前后输出日志。
func debugTool[T any](timeout time.Duration, name string, fn func(ctx context.Context, params *T) (string, error)) func(ctx context.Context, params *T) (string, error) {
	return func(ctx context.Context, params *T) (string, error) {
		argJSON, _ := json.Marshal(params)

		timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		out, err := fn(timeoutCtx, params)
		if err != nil {
			// 优先区分"超时取消"与"普通失败"：
			// 若 ctx 已到截止时刻，底层为何报错都归因于超时，返回超时提示；
			// 否则按普通失败处理。若成功返回（err==nil）则信任成功结果，
			// 避免在截止瞬间误把成功当超时。
			if timeoutCtx.Err() != nil {
				// 底层操作在截止时刻被 ctx 取消，视为超时失败。
				log.Printf("[工具超时] %s\n  参数: %s\n  错误: %v", name, argJSON, err)
				return fmt.Sprintf("【%s 执行超时】超过 %v 仍未返回结果。请分析原因，调整参数或换一种方式重新尝试。", name, timeout), nil
			}
			// 记录原始错误到终端，但不再向上抛 err。
			// 改为把错误作为"普通结果文本"回传给模型，
			// 让模型反思后重试 / 调整参数 / 换方案，而不是中断整次运行。
			log.Printf("[工具失败] %s\n  参数: %s\n  错误: %v", name, argJSON, err)
			return fmt.Sprintf("【%s 执行失败】%v\n请分析失败原因，调整参数或换一种方式重新尝试。", name, err), nil
		}
		view := out
		if len(view) > 500 {
			view = view[:500] + "\n  ...(输出过长已截断)"
		}
		log.Printf("[工具调用] %s\n  参数: %s\n  返回: %s", name, argJSON, view)
		return out, nil
	}
}

func NavigateFunc(w *web.Web) func(ctx context.Context, action *web.Navigate) (string, error) {
	return func(ctx context.Context, action *web.Navigate) (string, error) {
		cleanURL := strings.TrimSpace(action.URL)
		// 移除首尾的反引号（解决LLM流式输出时带Markdown反引号的问题）
		cleanURL = strings.Trim(cleanURL, "`")
		// 再次Trim掉反引号移除后可能残留的空格
		cleanURL = strings.TrimSpace(cleanURL)
		action.URL = cleanURL
		err := w.Navigate(ctx, action)
		if err != nil {
			return "", err
		}
		// 导航后到达新页面，视为内容已变化；仅返回轻量确认，需内容时由 LLM 调用 get_content_tool
		result := w.ActionResult(ctx, "navigate", "", "", true)
		resultJSON, err := json.Marshal(result)
		if err != nil {
			return "", err
		}
		return string(resultJSON), nil
	}
}

func NewNavigateTool(w *web.Web) (tool.InvokableTool, error) {
	navigateTool, err := utils.InferTool(
		"navigate_tool", // tool name
		`Navigate the browser to a specified URL and wait for the page to fully load;
		use this to visit any web page, e.g.: https://www.example.com`, // tool description
		debugTool(w.OpTimeout(), "navigate_tool", NavigateFunc(w)))
	if err != nil {
		return nil, err
	}
	return navigateTool, nil
}

func ClickFunc(w *web.Web) func(ctx context.Context, action *web.Click) (string, error) {
	return func(ctx context.Context, action *web.Click) (string, error) {
		err := w.DoClick(ctx, action)
		if err != nil {
			return "", err
		}
		result := w.ActionResult(ctx, "click", "", "", true)
		resultJSON, err := json.Marshal(result)
		if err != nil {
			return "", err
		}
		return string(resultJSON), nil
	}
}

func NewClickTool(w *web.Web) (tool.InvokableTool, error) {
	return utils.InferTool(
		"click_tool",
		`Click an element on the page using a specified CSS or XPath selector; 
		use this when you know the target element's selector, e.g. button.load-more`,
		debugTool(w.OpTimeout(), "click_tool", ClickFunc(w)))
}

func ClickByAXFunc(w *web.Web) func(ctx context.Context, action *web.ClickByAXTree) (string, error) {
	return func(ctx context.Context, action *web.ClickByAXTree) (string, error) {
		err := w.DoClickByAX(ctx, action)
		if err != nil {
			return "", err
		}
		result := w.ActionResult(ctx, "click_by_ax", "", "", true)
		resultJSON, err := json.Marshal(result)
		if err != nil {
			return "", err
		}
		return string(resultJSON), nil
	}
}

func NewClickByAXTool(w *web.Web) (tool.InvokableTool, error) {
	return utils.InferTool(
		"click_by_ax_tool",
		`Click a clickable element by its node_id returned from get_interactives_tool; 
		this is the most accurate way to click and avoids ambiguity from duplicate text/selectors. 
		Prefer this over css/xpath when a fresh node_id is available. 
		NOTE: node_id becomes stale after a full page navigation or large DOM re-render,
		re-run get_interactives_tool to refresh it.`,
		debugTool(w.OpTimeout(), "click_by_ax_tool", ClickByAXFunc(w)))
}

func InputFunc(w *web.Web) func(ctx context.Context, action *web.Input) (string, error) {
	return func(ctx context.Context, action *web.Input) (string, error) {
		effective, err := w.DoInput(ctx, action)
		if err != nil {
			return "", err
		}
		result := w.ActionResult(ctx, "input", "", "", effective)
		resultJSON, err := json.Marshal(result)
		if err != nil {
			return "", err
		}
		return string(resultJSON), nil
	}
}

func NewInputTool(w *web.Web) (tool.InvokableTool, error) {
	return utils.InferTool(
		"input_tool",
		`Type text into an input field on the page using a specified CSS or XPath selector; 
		use this when you know the input field's selector,
		e.g. input.search-input or #keyword. Set clear=true to first erase existing text. 
		Prefer input_by_ax_tool when a fresh node_id is available.`,
		debugTool(w.OpTimeout(), "input_tool", InputFunc(w)))
}

func InputByAXFunc(w *web.Web) func(ctx context.Context, action *web.InputByAXTree) (string, error) {
	return func(ctx context.Context, action *web.InputByAXTree) (string, error) {
		effective, err := w.DoInputByAX(ctx, action)
		if err != nil {
			return "", err
		}
		result := w.ActionResult(ctx, "input_by_ax", "", "", effective)
		resultJSON, err := json.Marshal(result)
		if err != nil {
			return "", err
		}
		return string(resultJSON), nil
	}
}

func NewInputByAXTool(w *web.Web) (tool.InvokableTool, error) {
	return utils.InferTool(
		"input_by_ax_tool",
		`Type text into an input field by its node_id listed under "inputs" from get_interactives_tool;
		this is the most accurate way to input and avoids ambiguity. 
		Prefer this over css/xpath when a fresh node_id is available. 
		NOTE: node_id becomes stale after a full page navigation or large DOM re-render, 
		re-run get_interactives_tool to refresh it.`,
		debugTool(w.OpTimeout(), "input_by_ax_tool", InputByAXFunc(w)))
}

func ScrollFunc(w *web.Web) func(ctx context.Context, action *web.Scroll) (string, error) {
	return func(ctx context.Context, action *web.Scroll) (string, error) {
		err := w.DoScroll(ctx, action)
		if err != nil {
			return "", err
		}
		result := w.ActionResult(ctx, "scroll", "", "", true)
		resultJSON, err := json.Marshal(result)
		if err != nil {
			return "", err
		}
		return string(resultJSON), nil
	}
}

func NewScrollTool(w *web.Web) (tool.InvokableTool, error) {
	scrollTool, err := utils.InferTool(
		"scroll_tool", // tool name
		`Web browser are used to scroll the page; 
		they can be used to scroll the page by a specified amount of pixels;`, // tool description
		debugTool(w.OpTimeout(), "scroll_tool", ScrollFunc(w)))
	if err != nil {
		return nil, err
	}
	return scrollTool, nil
}

func JavaScriptFunc(w *web.Web) func(ctx context.Context, action *web.JavaScript) (string, error) {
	return func(ctx context.Context, action *web.JavaScript) (string, error) {
		value, err := w.DoJavaScript(ctx, action)
		if err != nil {
			// 把 JS 语法/执行报错原文回传，让模型据此修正脚本；
			// 而不是吞掉错误、只返回 effective:false，导致模型无从下手。
			return fmt.Sprintf("【javascript 执行失败】%v", err), nil
		}
		effective := value != "" && value != "undefined"
		result := w.ActionResult(ctx, "javascript", value, "", effective)
		resultJSON, err := json.Marshal(result)
		if err != nil {
			return "", err
		}
		return string(resultJSON), nil
	}
}

func NewJavaScriptTool(w *web.Web) (tool.InvokableTool, error) {
	jsTool, err := utils.InferTool(
		"javascript_tool", // tool name
		`Web browser are used to execute JavaScript code on the page; 
		they can be used to execute JavaScript code on the page, eg: document.querySelector('button').click()`, // tool description
		debugTool(w.OpTimeout(), "javascript_tool", JavaScriptFunc(w)))
	if err != nil {
		return nil, err
	}
	return jsTool, nil
}

func GetContentFunc(w *web.Web) func(ctx context.Context, action *web.GetContent) (string, error) {
	return func(ctx context.Context, action *web.GetContent) (string, error) {
		mode := web.DefaultContentMode(action.Mode)
		var value string
		var err error
		switch mode {
		case web.ContentModeStructured:
			value, err = w.GetStructured(ctx)
		case web.ContentModeFull:
			value, err = w.GetHTML(ctx)
		case web.ContentModeAX:
			value, err = w.GetAXContent(ctx)
		default:
			return "", fmt.Errorf("未知的 mode=%q,可选:ax|structured|full", mode)
		}
		effective := err == nil
		result := w.ActionResult(ctx, "get_content", value, "", effective)
		resultJSON, err := json.Marshal(result)
		if err != nil {
			return "", err
		}
		return string(resultJSON), nil
	}
}

func NewGetContentTool(w *web.Web) (tool.InvokableTool, error) {
	getContentTool, err := utils.InferTool(
		"get_content_tool", // tool name
		`Get the content of the current page in one of three modes: 
		ax (default, preferred), structured, full. 
		ax builds compact Markdown from the browser Accessibility/AX tree, 
		which naturally excludes hidden/decorative/script nodes, 
		restores headings/lists/tables and parent-child nesting, 
		and attaches a node_id marker to every interactive element (links/buttons/inputs) 
		so you can directly click or type at them without re-fetching the interactives list 
		-- this is the most token-efficient and interactive-friendly choice, use by default;
		 structured converts the pruned DOM to readable Markdown 
		 (headings/lists/tables/links with absolute URLs) and preserves rich inline formatting 
		 like bold/inline code, use when content is content-rich reading material; 
		 full returns the complete raw HTML with precise DOM structure and CSS attributes (expensive, only when you need the original source, code snippets, data tables, or CSS-attribute/xpath-based selectors).`, // tool description
		debugTool(w.OpTimeout(), "get_content_tool", GetContentFunc(w)))
	if err != nil {
		return nil, err
	}
	return getContentTool, nil
}

func GetInteractivesFunc(w *web.Web) func(ctx context.Context, action *web.GetInteractive) (string, error) {
	return func(ctx context.Context, action *web.GetInteractive) (string, error) {
		interactives, err := w.GetInteractives(ctx)
		effective := err == nil
		result := w.ActionResult(ctx, "get_interactives", interactives, "", effective)
		resultJSON, err := json.Marshal(result)
		if err != nil {
			return "", err
		}
		return string(resultJSON), nil
	}
}

func NewGetInteractivesTool(w *web.Web) (tool.InvokableTool, error) {
	interactivesTool, err := utils.InferTool(
		"get_interactives_tool", // tool name
		`Web browser are used to get all interactive elements on this page, including clickable links, buttons and text input fields, each with its node_id`, // tool description
		debugTool(w.OpTimeout(), "get_interactives_tool", GetInteractivesFunc(w)))
	if err != nil {
		return nil, err
	}
	return interactivesTool, nil
}

func GetPDFFunc(w *web.Web) func(ctx context.Context, action *web.GetPDF) (string, error) {
	return func(ctx context.Context, action *web.GetPDF) (string, error) {
		artifactPath, err := w.GetPDF(ctx, action)
		effective := err == nil
		result := w.ActionResult(ctx, "get_pdf", "", artifactPath, effective)
		resultJSON, err := json.Marshal(result)
		if err != nil {
			return "", err
		}
		return string(resultJSON), nil
	}
}

func NewGetPDFTool(w *web.Web) (tool.InvokableTool, error) {
	getPDFTool, err := utils.InferTool(
		"get_pdf_tool", // tool name
		`Web browser are used to get the PDF of the current page`, // tool description
		debugTool(w.OpTimeout(), "get_pdf_tool", GetPDFFunc(w)))
	if err != nil {
		return nil, err
	}
	return getPDFTool, nil
}
