package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
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
// 服务端为"显式状态句柄"模型：浏览器工具需要 web_id。本客户端在首次浏览器动作前自动调用
// open_web_tool 借一张网并缓存 web_id，之后把该 web_id 注入每次工具调用的参数里，
// 对模型完全透明——模型的提示词与工具入参（url/selector 等）无需感知句柄的存在。
type Client struct {
	baseURL string
	http    *http.Client
	nextID  int

	mu    sync.Mutex
	webID string // 已自动借到的网句柄；为空表示尚未借网
}

// NewMCPClient 创建一个指向 baseURL（JSON-RPC 端点，如 "http://127.0.0.1:8080/mcp"）的客户端。
// baseURL 为空时回退到默认端点 defaultMCPBaseURL。
func NewMCPClient(baseURL string) *Client {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultMCPBaseURL
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: defaultCallTimeout},
	}
}

// CallTool 以 JSON 参数调用远端名为 toolName 的 MCP 工具，返回其文本结果（即 ActionResult 的 JSON 串）。
// 浏览器动作会先确保已借到一张网并向参数注入 web_id；生命周期工具（open/close/list）则原样放行。
// 本地工具封装（utils.InferTool）拿到该串后原样回传给模型。
func (c *Client) CallTool(ctx context.Context, toolName string, arguments map[string]any) (string, error) {
	// 浏览器动作统一携带 web_id；首次调用前先自动借一张网（见 ensureWeb）。
	if !isLifecycleTool(toolName) {
		if err := c.ensureWeb(ctx); err != nil {
			return "", err
		}
		arguments["web_id"] = c.webID
	}
	return c.callToolRaw(ctx, toolName, arguments)
}

// isLifecycleTool 判断是否为生命周期工具（不需要也不应注入 web_id 的类型）。
func isLifecycleTool(name string) bool {
	return name == "open_web_tool" || name == "close_web_tool" || name == "list_webs_tool"
}

// ensureWeb 惰性借一张网：首次浏览器动作前调用 open_web_tool 并缓存返回的 web_id。
// 加锁保证并发/重复触发时只借一次；进程退出时由其底层 WebSessionManager 兜底归还。
func (c *Client) ensureWeb(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.webID != "" {
		return nil
	}
	out, err := c.callToolRaw(ctx, "open_web_tool", map[string]any{})
	if err != nil {
		return fmt.Errorf("自动借网失败: %w", err)
	}
	id := strings.TrimSpace(strings.TrimPrefix(out, "web_id="))
	if id == "" {
		return fmt.Errorf("open_web_tool 未返回有效 web_id: %q", out)
	}
	c.webID = id
	log.Printf("[MCP] 已自动借到网 web_id=%s", id)
	return nil
}

func truncate(s string) string {
	const n = 400
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(截断)"
}

func (c *Client) callToolRaw(ctx context.Context, toolName string, arguments map[string]any) (string, error) {
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
