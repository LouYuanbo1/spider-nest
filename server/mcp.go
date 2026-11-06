package server

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/LouYuanbo1/spider-nest/action"
	"github.com/LouYuanbo1/spider-nest/session"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolRegistry 把"网上的浏览器操作"封装成官方 go-sdk 风格的 MCP 工具集合。
//
//   - In  是对应的带句柄请求模型（如 action.NavigateRequest，内嵌纯动作 + web_id）；
//   - Out 统一为 action.ActionResult（结构化执行结果）；
//   - m   负责按 web_id 解析出真正执行动作的那张网：浏览器工具统一要求携带 web_id，
//     该 web_id 由 open_web_tool 借网时分配（见 lifecycle 工具）。
//
// 注册时用 sdk.AddTool 把某个 *toolRegistry 方法绑定到 sdk.Tool 上即可，
// SDK 会自动根据 In / Out 类型生成输入、输出 JSON-Schema（依赖 jsonschema tag），
// 并把返回值填写到 CallToolResult 的 structuredContent / content 中。
type toolRegistry struct {
	// m 是底层会话管理器：既按 web_id 解析网，又承载借/还/列生命周期工具。
	m *session.Manager
}

// NewMCPServer 用"显式状态句柄"模式创建 MCP 服务器（统一入口）：
// 浏览器工具统一要求携带 web_id，另注册 open_web_tool / close_web_tool / list_webs_tool
// 生命周期工具供模型借还网。底层网来自 session.Manager，一个用户独占一张网。
func NewMCPServer(m *session.Manager, implementation *mcp.Implementation, opts *mcp.ServerOptions) *mcp.Server {
	tr := &toolRegistry{m: m}
	srv := buildServer(tr, implementation, opts)
	registerLifecycleTools(srv, tr)
	return srv
}

// buildServer 注册全部浏览器操作工具并返回 *mcp.Server。
func buildServer(tr *toolRegistry, implementation *mcp.Implementation, opts *mcp.ServerOptions) *mcp.Server {
	srv := mcp.NewServer(implementation, opts)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "navigate_tool",
		Description: "Navigate the browser to a specified URL and wait for the page to fully load; use this to visit any web page, e.g.: https://www.example.com. Requires a valid web_id obtained from open_web_tool.",
	}, tr.wrapTool("navigate_tool", tr.navigate))

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "click_tool",
		Description: "Click an element on the page using a specified CSS or XPath selector; use this when you know the target element's selector, e.g. button.load-more. Requires a valid web_id.",
	}, tr.wrapTool("click_tool", tr.click))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "click_by_ax_tool",
		Description: "Click a clickable element by its node_id returned from get_interactives_tool; this is the most accurate way to click and avoids ambiguity from duplicate text/selectors. " +
			"Prefer this over css/xpath when a fresh node_id is available. " +
			"NOTE: node_id becomes stale after a full page navigation or large DOM re-render, re-run get_interactives_tool to refresh it. Requires a valid web_id.",
	}, tr.wrapTool("click_by_ax_tool", tr.clickByAX))

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "input_tool",
		Description: "Type text into an input field on the page using a specified CSS or XPath selector; use this when you know the input field's selector, e.g. input.search-input or #keyword. Set clear=true to first erase existing text. Prefer input_by_ax_tool when a fresh node_id is available. Requires a valid web_id.",
	}, tr.wrapTool("input_tool", tr.input))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "input_by_ax_tool",
		Description: "Type text into an input field by its node_id listed under \"inputs\" from get_interactives_tool; this is the most accurate way to input and avoids ambiguity. " +
			"Prefer this over css/xpath when a fresh node_id is available. " +
			"NOTE: node_id becomes stale after a full page navigation or large DOM re-render, re-run get_interactives_tool to refresh it. Requires a valid web_id.",
	}, tr.wrapTool("input_by_ax_tool", tr.inputByAX))

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "scroll_tool",
		Description: "Scroll the current page by a specified amount of pixels; use this to reveal more content below the fold. Requires a valid web_id.",
	}, tr.wrapTool("scroll_tool", tr.scroll))

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "javascript_tool",
		Description: "Execute JavaScript code on the page and get its result. CONTRACT: a single expression (e.g. 1+1, document.title, document.querySelector('a').href) is automatically returned; for multiple statements or complex logic, write a self-contained function () => { ...; return x } or an explicit return, since only the returned value is visible. eg: document.querySelector('button').click(). Requires a valid web_id.",
	}, tr.wrapTool("javascript_tool", tr.execJavaScript))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_content_tool",
		Description: "Get the content of the page in one of three modes: ax (default, preferred), structured, full. " +
			"ax builds compact Markdown from the browser Accessibility/AX tree, which naturally excludes hidden/decorative/script nodes, restores headings/lists/tables and parent-child nesting, and attaches a node_id marker to every interactive element (links/buttons/inputs) so you can directly click or type at them without re-fetching the interactives list -- the most token-efficient interactive-friendly choice, use by default; " +
			"structured converts the pruned DOM to readable Markdown (headings/lists/tables/links with absolute URLs) and preserves rich inline formatting like bold/inline code, use when content is content-rich reading material; " +
			"full returns the complete raw HTML with precise DOM structure and CSS attributes (expensive, only when you need the original source, code snippets, data tables, or CSS-attribute/xpath-based selectors). Requires a valid web_id.",
	}, tr.wrapTool("get_content_tool", tr.getContent))

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_interactives_tool",
		Description: "Get all interactive elements on the current page, including clickable links, buttons and text input fields, each with its node_id. Requires a valid web_id.",
	}, tr.wrapTool("get_interactives_tool", tr.getInteractives))

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_pdf_tool",
		Description: "Get the PDF of the current page. Requires a valid web_id.",
	}, tr.wrapTool("get_pdf_tool", tr.getPDF))

	return srv
}

// registerLifecycleTools 注册"借/还/列"生命周期工具。统一句柄模式下始终注册。
func registerLifecycleTools(srv *mcp.Server, tr *toolRegistry) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "open_web_tool",
		Description: "Borrow a dedicated web page (a browser page) from the pool and return its web_id handle. This is the FIRST step of every conversation that needs a browser: " +
			"call it once, keep the returned web_id, and pass the SAME web_id to every later browser tool (navigate/click/input/get_content/...). " +
			"Every user owns one web; when the conversation ends, call close_web_tool to return it.",
	}, tr.wrapTool("open_web_tool", tr.openWeb))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "close_web_tool",
		Description: "Return the web_id's web page back to the idle pool. Call this when the conversation/task is finished to release the resource. " +
			"Idle webs are also recycled automatically after a timeout; always prefer an explicit close.",
	}, tr.wrapTool("close_web_tool", tr.closeWeb))

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_webs_tool",
		Description: "List all currently-held (not yet returned) web_id handles, so you can keep track of the resources you have opened.",
	}, tr.wrapTool("list_webs_tool", tr.listWebs))
}

// wrapTool 把执行函数包装成官方 HandlerFor 风格的处理器，并统一处理：
//   - 超时：每次工具调用按统一的 CallTimeout 限时，超时后取消 ctx 并回传明确提示；
//   - 日志：把入参、调用结果（截断过长的 Value）打印到终端，便于调试；
//   - 错误：把执行失败回传为 error，SDK 会自动写入 CallToolResult 的 content 并把 isError 置 true。
func (tr *toolRegistry) wrapTool[T any](name string, fn func(ctx context.Context, input T) (action.ActionResult, error)) mcp.ToolHandlerFor[T, action.ActionResult] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, input T) (*mcp.CallToolResult, action.ActionResult, error) {
		log.Printf("[工具调用] %s 参数: %+v", name, input)

		timeoutCtx, cancel := context.WithTimeout(ctx, tr.m.CallTimeout())
		defer cancel()

		result, err := fn(timeoutCtx, input)
		if err != nil && timeoutCtx.Err() != nil {
			// 在截止时刻被 ctx 取消，统一归因于超时。
			err = fmt.Errorf("【%s 执行超时】超过 %v 仍未返回结果。请分析原因，调整参数或换一种方式重新尝试。", name, tr.m.CallTimeout())
		}
		if err != nil {
			log.Printf("[工具失败] %s 参数: %+v 错误: %v", name, input, err)
			return nil, result, err
		}

		view := result.Value
		if len(view) > 500 {
			view = view[:500] + " ...(输出过长已截断)"
		}
		log.Printf("[工具完成] %s 返回: action=%s effective=%v value=%v", name, result.Action, result.Effective, view)
		return nil, result, nil
	}
}

// openWeb 从池借一张网并返回 web_id 句柄（显式状态句柄的第一步）。
func (tr *toolRegistry) openWeb(ctx context.Context, _ action.OpenWeb) (action.ActionResult, error) {
	id, err := tr.m.Open(ctx)
	if err != nil {
		return action.ActionResult{}, err
	}
	return action.ActionResult{Action: "open_web", Effective: true, Value: "web_id=" + id}, nil
}

// closeWeb 归还 web_id 对应的网（会话结束/资源释放）。
func (tr *toolRegistry) closeWeb(_ context.Context, in action.CloseWeb) (action.ActionResult, error) {
	if err := tr.m.Close(in.WebID); err != nil {
		return action.ActionResult{}, err
	}
	return action.ActionResult{Action: "close_web", Effective: true, Value: "已归还 web_id=" + in.WebID}, nil
}

// listWebs 列出现有仍被占用（未归还）的 web_id。
func (tr *toolRegistry) listWebs(_ context.Context, _ action.ListWebs) (action.ActionResult, error) {
	ids := tr.m.List()
	if len(ids) == 0 {
		return action.ActionResult{Action: "list_webs", Effective: true, Value: "当前没有已借出的网(web_id)"}, nil
	}
	return action.ActionResult{Action: "list_webs", Effective: true, Value: "已借出的 web_id: " + strings.Join(ids, ", ")}, nil
}

// ---- 浏览器工具（先按 web_id 解析网，再以纯动作执行） ----

// navigate 导航到指定 URL 并等待页面加载完成。
func (tr *toolRegistry) navigate(ctx context.Context, in action.NavigateRequest) (action.ActionResult, error) {
	w, err := tr.m.Resolve(ctx, in.WebID)
	if err != nil {
		return action.ActionResult{}, err
	}
	cleanURL := strings.TrimSpace(in.URL)
	// 移除首尾的反引号（解决 LLM 流式输出时带 Markdown 反引号的问题），再 Trim 一次消除残留空格
	cleanURL = strings.TrimLeft(strings.TrimRight(cleanURL, "`"), "`")
	cleanURL = strings.TrimSpace(cleanURL)

	if err := w.Navigate(ctx, &action.Navigate{URL: cleanURL}); err != nil {
		return action.ActionResult{}, err
	}
	// 导航后到达新页面，视为内容已变化；仅返回轻量确认，需内容时由 LLM 调用 get_content_tool
	return w.ActionResult(ctx, "navigate", "", "", true), nil
}

// click 通过 CSS / XPath 选择器点击页面元素。
func (tr *toolRegistry) click(ctx context.Context, in action.ClickRequest) (action.ActionResult, error) {
	w, err := tr.m.Resolve(ctx, in.WebID)
	if err != nil {
		return action.ActionResult{}, err
	}
	if err := w.DoClick(ctx, &in.Click); err != nil {
		return action.ActionResult{}, err
	}
	return w.ActionResult(ctx, "click", "", "", true), nil
}

// clickByAX 通过 get_interactives_tool 返回的 node_id 精准点击可交互元素。
func (tr *toolRegistry) clickByAX(ctx context.Context, in action.ClickByAXRequest) (action.ActionResult, error) {
	w, err := tr.m.Resolve(ctx, in.WebID)
	if err != nil {
		return action.ActionResult{}, err
	}
	if err := w.DoClickByAX(ctx, &in.ClickByAXTree); err != nil {
		return action.ActionResult{}, err
	}
	return w.ActionResult(ctx, "click_by_ax", "", "", true), nil
}

// input 通过 CSS / XPath 选择器向输入框键入文本，clear 决定是否先清空。
func (tr *toolRegistry) input(ctx context.Context, in action.InputRequest) (action.ActionResult, error) {
	w, err := tr.m.Resolve(ctx, in.WebID)
	if err != nil {
		return action.ActionResult{}, err
	}
	effective, err := w.DoInput(ctx, &in.Input)
	if err != nil {
		return action.ActionResult{}, err
	}
	return w.ActionResult(ctx, "input", "", "", effective), nil
}

// inputByAX 通过 node_id（或 role/name+nth）定位输入框并键入文本。
func (tr *toolRegistry) inputByAX(ctx context.Context, in action.InputByAXRequest) (action.ActionResult, error) {
	w, err := tr.m.Resolve(ctx, in.WebID)
	if err != nil {
		return action.ActionResult{}, err
	}
	effective, err := w.DoInputByAX(ctx, &in.InputByAXTree)
	if err != nil {
		return action.ActionResult{}, err
	}
	return w.ActionResult(ctx, "input_by_ax", "", "", effective), nil
}

// scroll 按指定像素数滚动当前页面。
func (tr *toolRegistry) scroll(ctx context.Context, in action.ScrollRequest) (action.ActionResult, error) {
	w, err := tr.m.Resolve(ctx, in.WebID)
	if err != nil {
		return action.ActionResult{}, err
	}
	if err := w.DoScroll(ctx, &in.Scroll); err != nil {
		return action.ActionResult{}, err
	}
	return w.ActionResult(ctx, "scroll", "", "", true), nil
}

// execJavaScript 执行 JavaScript 代码并返回执行结果。
func (tr *toolRegistry) execJavaScript(ctx context.Context, in action.JavaScriptRequest) (action.ActionResult, error) {
	w, err := tr.m.Resolve(ctx, in.WebID)
	if err != nil {
		return action.ActionResult{}, err
	}
	value, err := w.DoJavaScript(ctx, &in.JavaScript)
	if err != nil {
		return action.ActionResult{}, fmt.Errorf("javascript 执行失败: %w", err)
	}
	effective := value != "" && value != "undefined"
	return w.ActionResult(ctx, "javascript", value, "", effective), nil
}

// getContent 按指定模式取回页面内容：ax（默认）/ structured / full。
func (tr *toolRegistry) getContent(ctx context.Context, in action.GetContentRequest) (action.ActionResult, error) {
	w, err := tr.m.Resolve(ctx, in.WebID)
	if err != nil {
		return action.ActionResult{}, err
	}
	mode := action.DefaultContentMode(in.Mode)
	var value string
	switch mode {
	case action.ContentModeStructured:
		value, err = w.GetStructured(ctx)
	case action.ContentModeFull:
		value, err = w.GetHTML(ctx)
	case action.ContentModeAX:
		value, err = w.GetAXContent(ctx)
	default:
		return action.ActionResult{}, fmt.Errorf("未知的 mode=%q,可选:ax|structured|full", mode)
	}
	effective := err == nil
	return w.ActionResult(ctx, "get_content", value, "", effective), nil
}

// getInteractives 取回页面所有可交互元素（链接/按钮/输入框）及其 node_id。
func (tr *toolRegistry) getInteractives(ctx context.Context, in action.GetInteractiveRequest) (action.ActionResult, error) {
	w, err := tr.m.Resolve(ctx, in.WebID)
	if err != nil {
		return action.ActionResult{}, err
	}
	interactives, err := w.GetInteractives(ctx)
	effective := err == nil
	return w.ActionResult(ctx, "get_interactives", interactives, "", effective), nil
}

// getPDF 把当前页面导出为 PDF，产物路径写入 ArtifactPath。
func (tr *toolRegistry) getPDF(ctx context.Context, in action.GetPDFRequest) (action.ActionResult, error) {
	w, err := tr.m.Resolve(ctx, in.WebID)
	if err != nil {
		return action.ActionResult{}, err
	}
	artifactPath, err := w.GetPDF(ctx, &in.GetPDF)
	effective := err == nil
	return w.ActionResult(ctx, "get_pdf", "", artifactPath, effective), nil
}
