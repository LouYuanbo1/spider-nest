package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

// ---- MCP HTTP 客户端 ----
// agent 的工具不再直接操纵本地浏览器，而是把每次调用作为 JSON-RPC 的 tools/call 请求发给
// 远端 MCP 服务（cmd/server 已把全部浏览器工具通过 Streamable HTTP 暴露出来）。这样 agent
// 与浏览器彻底解耦，工具的入参 schema 也统一由 MCP 服务端维护。

const (
	// defaultMCPBaseURL 内置的默认 MCP 端点；可用 NewMCPClient 传入其它值覆盖。
	defaultMCPBaseURL = "http://127.0.0.1:8080/mcp"
	// defaultCallTimeout 单次工具调用的总超时上限。
	defaultCallTimeout = time.Minute
)

// jsonRPCRequest / jsonRPCResponse 是 MCP 传输所需的最小 JSON-RPC 2.0 结构。
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCRawResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      int              `json:"id"`
	Result  *toolsCallResult `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// toolsCallResult 对应 MCP tools/call 的标准 result 结构。
type toolsCallResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError,omitempty"`
}

// MCPClient 封装到远端 MCP 服务的 Streamable HTTP 调用。
type MCPClient struct {
	baseURL string
	http    *http.Client
	nextID  int
}

// NewMCPClient 创建一个指向 baseURL（JSON-RPC 端点，如 "http://127.0.0.1:8080/mcp"）的客户端。
// baseURL 为空时回退到默认端点 defaultMCPBaseURL。
func NewMCPClient(baseURL string) *MCPClient {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultMCPBaseURL
	}
	return &MCPClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: defaultCallTimeout},
	}
}

// CallTool 以 JSON 参数调用远端名为 toolName 的 MCP 工具，返回其文本结果（即 ActionResult 的 JSON 串）。
// 本地工具封装（utils.InferTool）拿到该串后原样回传给模型。
func (c *MCPClient) CallTool(ctx context.Context, toolName string, arguments map[string]any) (string, error) {
	params, err := json.Marshal(map[string]any{
		"name":      toolName,
		"arguments": arguments,
	})
	if err != nil {
		return "", err
	}
	c.nextID++
	reqBody, err := json.Marshal(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      c.nextID,
		Method:  "tools/call",
		Params:  params,
	})
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	// Streamable 传输要求 Accept 同时声明 application/json 与 text/event-stream。
	httpReq.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("调用 MCP 工具 %s 失败: %w", toolName, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("MCP 服务返回 HTTP %d: %s", resp.StatusCode, truncate(string(raw)))
	}

	var rpc jsonRPCRawResponse
	if err := json.Unmarshal(raw, &rpc); err != nil {
		return "", fmt.Errorf("解析 MCP 响应失败: %v\n%s", err, truncate(string(raw)))
	}
	if rpc.Error != nil {
		return "", fmt.Errorf("MCP 工具 %s 调用错误(%d): %s", toolName, rpc.Error.Code, rpc.Error.Message)
	}
	if rpc.Result == nil || len(rpc.Result.Content) == 0 {
		return "", fmt.Errorf("MCP 工具 %s 未返回结果内容", toolName)
	}
	return rpc.Result.Content[0].Text, nil
}

func truncate(s string) string {
	const n = 400
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(截断)"
}

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
			if timeoutCtx.Err() != nil {
				log.Printf("[工具超时] %s\n  参数: %s\n  错误: %v", name, argJSON, err)
				return fmt.Sprintf("【%s 执行超时】超过 %v 仍未返回结果。请分析原因，调整参数或换一种方式重新尝试。", name, timeout), nil
			}
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

// newMCPTool 用参数类型 T 声明一个 eino 工具：参数经 JSON 序列化后作为 tools/call 的
// arguments 发往远端 MCP 服务，并把返回的文本结果交给模型。
// 每个 T 的字段标签使用 eino-contrib/jsonschema 的旧式 "description=..." 语法，
// 供 eino 的 utils.InferTool 反射出模型可见的入参 schema。
func newMCPTool[T any](c *MCPClient, name, desc string) (tool.InvokableTool, error) {
	return utils.InferTool(name, desc, debugTool(defaultCallTimeout, name, func(ctx context.Context, p *T) (string, error) {
		b, err := json.Marshal(p)
		if err != nil {
			return "", err
		}
		var arguments map[string]any
		if err := json.Unmarshal(b, &arguments); err != nil {
			return "", err
		}
		return c.CallTool(ctx, name, arguments)
	}))
}

// ---- 各工具的参数结构体（字段名与 action 包的一致，方便原样透传给 MCP 服务）----

type navigateParams struct {
	URL string `json:"url" jsonschema:"description=用于指定要导航的目标 URL"`
}

func NewNavigateTool(c *MCPClient) (tool.InvokableTool, error) {
	return newMCPTool[navigateParams](c, "navigate_tool",
		`Navigate the browser to a specified URL and wait for the page to fully load; 
		use this to visit any web page, e.g.: https://www.example.com`)
}

type clickParams struct {
	Type     SelectorType `json:"type" jsonschema:"description=用于指定要点击的元素类型,enum=css,enum=xpath"`
	Selector string       `json:"selector" jsonschema:"description=用于指定要点击的元素选择器"`
}

// SelectorType 复用 CSS / XPath 两种定位方式。
type SelectorType string

func NewClickTool(c *MCPClient) (tool.InvokableTool, error) {
	return newMCPTool[clickParams](c, "click_tool",
		`Click an element on the page using a specified CSS or XPath selector; 
		use this when you know the target element's selector, e.g. button.load-more`)
}

type clickByAXParams struct {
	BackendDOMNodeID int64  `json:"backendDOMNodeID,omitempty" jsonschema:"description=用于指定要操作的元素的 AX 节点ID,如果知晓节点ID优先使用,则无需指定角色和名称"`
	Role             string `json:"role,omitempty" jsonschema:"description=用于指定要操作的元素的 AX 角色,enum=button,enum=link,enum=searchbox,enum=input,enum=combobox,enum=textbox"`
	Name             string `json:"name,omitempty" jsonschema:"description=用于指定要操作的元素的 AX 名称"`
	Nth              int    `json:"nth,omitempty" jsonschema:"description=用于指定要操作的元素的 AX 节点索引,处理角色和名称相同的问题,默认0"`
}

func NewClickByAXTool(c *MCPClient) (tool.InvokableTool, error) {
	return newMCPTool[clickByAXParams](c, "click_by_ax_tool",
		`Click a clickable element by its node_id returned from get_interactives_tool; 
		this is the most accurate way to click and avoids ambiguity from duplicate text/selectors. 
		Prefer this over css/xpath when a fresh node_id is available. 
		NOTE: node_id becomes stale after a full page navigation or large DOM re-render,
		re-run get_interactives_tool to refresh it.`)
}

type inputParams struct {
	Type     SelectorType `json:"type" jsonschema:"description=用于指定要输入文本的输入框类型,enum=css,enum=xpath"`
	Selector string       `json:"selector" jsonschema:"description=用于指定要输入文本的输入框选择器"`
	Text     string       `json:"text" jsonschema:"description=要输入到输入框中的文本内容"`
	Clear    bool         `json:"clear,omitempty" jsonschema:"description=是否先清空输入框原有内容再输入,默认为false"`
}

func NewInputTool(c *MCPClient) (tool.InvokableTool, error) {
	return newMCPTool[inputParams](c, "input_tool",
		`Type text into an input field on the page using a specified CSS or XPath selector; 
		use this when you know the input field's selector,
		e.g. input.search-input or #keyword. Set clear=true to first erase existing text. 
		Prefer input_by_ax_tool when a fresh node_id is available.`)
}

type inputByAXParams struct {
	BackendDOMNodeID int64  `json:"backendDOMNodeID,omitempty" jsonschema:"description=用于指定要操作的元素的 AX 节点ID,如果知晓节点ID优先使用,则无需指定角色和名称"`
	Role             string `json:"role,omitempty" jsonschema:"description=用于指定要操作的元素的 AX 角色,enum=button,enum=link,enum=searchbox,enum=input,enum=combobox,enum=textbox"`
	Name             string `json:"name,omitempty" jsonschema:"description=用于指定要操作的元素的 AX 名称"`
	Nth              int    `json:"nth,omitempty" jsonschema:"description=用于指定要操作的元素的 AX 节点索引,处理角色和名称相同的问题,默认0"`
	Text             string `json:"text,omitempty" jsonschema:"description=要输入到输入框中的文本内容"`
	Clear            bool   `json:"clear,omitempty" jsonschema:"description=是否先清空输入框原有内容再输入,默认为false"`
}

func NewInputByAXTool(c *MCPClient) (tool.InvokableTool, error) {
	return newMCPTool[inputByAXParams](c, "input_by_ax_tool",
		`Type text into an input field by its node_id listed under "inputs" from get_interactives_tool;
		this is the most accurate way to input and avoids ambiguity. 
		Prefer this over css/xpath when a fresh node_id is available. 
		NOTE: node_id becomes stale after a full page navigation or large DOM re-render, 
		re-run get_interactives_tool to refresh it.`)
}

type scrollParams struct {
	ScrollY int `json:"scroll_y" jsonschema:"description=用于指定要滚动的像素数"`
}

func NewScrollTool(c *MCPClient) (tool.InvokableTool, error) {
	return newMCPTool[scrollParams](c, "scroll_tool",
		`Web browser are used to scroll the page; 
		they can be used to scroll the page by a specified amount of pixels;`)
}

type javascriptParams struct {
	JS   string `json:"js" jsonschema:"description=用于指定要执行的JavaScript代码"`
	Args []any  `json:"args,omitempty" jsonschema:"description=用于指定JavaScript代码的参数，数组元素作为 JS 函数实参，可为空数组"`
}

func NewJavaScriptTool(c *MCPClient) (tool.InvokableTool, error) {
	return utils.InferTool("javascript_tool",
		`Execute JavaScript code on the page and get its result. 
		CONTRACT: if you pass a single expression (e.g. 1+1, document.title, document.querySelector('a').href), its evaluation result is AUTOMATICALLY returned;
		if you pass multiple statements or complex logic, write a self-contained function () => { ...; return x } or use an explicit return, since only the returned value is visible.
		eg: document.querySelector('button').click()`,
		debugTool(defaultCallTimeout, "javascript_tool", func(ctx context.Context, p *javascriptParams) (string, error) {
			if p.Args == nil {
				p.Args = []any{}
			}
			b, err := json.Marshal(p)
			if err != nil {
				return "", err
			}
			var arguments map[string]any
			if err := json.Unmarshal(b, &arguments); err != nil {
				return "", err
			}
			return c.CallTool(ctx, "javascript_tool", arguments)
		}))
}

type getContentParams struct {
	Mode string `json:"mode,omitempty" jsonschema:"description=取数模式,enum=ax,enum=structured,enum=full,default=ax"`
}

func NewGetContentTool(c *MCPClient) (tool.InvokableTool, error) {
	return newMCPTool[getContentParams](c, "get_content_tool",
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
		 full returns the complete raw HTML with precise DOM structure and CSS attributes (expensive, only when you need the original source, code snippets, data tables, or CSS-attribute/xpath-based selectors).`)
}

// getInteractiveParams 无入参：直接返回当前页全部可交互元素的 AX 节点列表。
type getInteractiveParams struct{}

func NewGetInteractivesTool(c *MCPClient) (tool.InvokableTool, error) {
	return newMCPTool[getInteractiveParams](c, "get_interactives_tool",
		`Web browser are used to get all interactive elements on this page, including clickable links, buttons and text input fields, each with its node_id`)
}

type getPDFParams struct {
	ArtifactDir  string `json:"artifact_dir,omitempty" jsonschema:"description=产物保存目录,为空使用默认下载目录"`
	ArtifactName string `json:"artifact_name,omitempty" jsonschema:"description=产物文件名(不含扩展名),为空按内容自动生成"`
}

func NewGetPDFTool(c *MCPClient) (tool.InvokableTool, error) {
	return newMCPTool[getPDFParams](c, "get_pdf_tool",
		`Web browser are used to get the PDF of the current page`)
}
