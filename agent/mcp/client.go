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
// 服务端为"显式状态句柄"模型：浏览器工具需要 web_id。本客户端按"会话"维度自管理借网：
// 每个会话在首次浏览器动作前自动调用 open_web_tool 借到自己的那张网，之后把该 web_id
// 注入该会话每次工具调用的参数里，对模型完全透明——模型的提示词与工具入参
// （url/selector 等）无需感知句柄的存在。会话结束时应调用 CloseSession 显式归还。
type Client struct {
	baseURL string
	http    *http.Client

	mu   sync.Mutex
	webs map[string]string // sessionID → web_id；各自独立隔离

	idMu sync.Mutex
	id   int
}

// sessionKey 是 ctx 中传递会话 id 的私有键类型，避免字符串键被意外碰撞。
type sessionKey struct{}

// WithSessionID 把会话 id 注入 ctx，供其后对 Client 的浏览器工具调用按会话路由到对应那张网。
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionKey{}, sessionID)
}

// SessionIDFromContext 从 ctx 取出会话 id；未注入时返回空串。
func SessionIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(sessionKey{}).(string); ok {
		return v
	}
	return ""
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
		webs:    make(map[string]string),
	}
}

// CallTool 以 JSON 参数调用远端名为 toolName 的 MCP 工具，返回其文本结果（即 ActionResult 的 JSON 串）。
// 浏览器动作会先确保当前会话（来自 ctx）已借到一张网并向参数注入其 web_id；
// 生命周期工具（open/close/list）则原样放行。
// 本地工具封装（utils.InferTool）拿到该串后原样回传给模型。
func (c *Client) CallTool(ctx context.Context, toolName string, arguments map[string]any) (string, error) {
	// 浏览器动作按当前会话注入其独立 web_id；首次调用前先为该会话自动借一张网（见 ensureWeb）。
	if !isLifecycleTool(toolName) {
		sid := SessionIDFromContext(ctx)
		webID, err := c.ensureWeb(ctx, sid)
		if err != nil {
			return "", err
		}
		arguments["web_id"] = webID
	}
	return c.callToolRaw(ctx, toolName, arguments)
}

// isLifecycleTool 判断是否为生命周期工具（不需要也不应注入 web_id 的类型）。
func isLifecycleTool(name string) bool {
	return name == "open_web_tool" || name == "close_web_tool"
}

// ensureWeb 按会话惰性借一张网：某会话首次浏览器动作前调用 open_web_tool 并缓存其 web_id，
// 之后该会话复用同一张网；不同会话各自持有一张独立的网。
// 加锁保证同一会话并发/重复触发时只借一次。
func (c *Client) ensureWeb(ctx context.Context, sessionID string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if id, ok := c.webs[sessionID]; ok {
		return id, nil
	}
	out, err := c.callToolRaw(ctx, "open_web_tool", map[string]any{})
	if err != nil {
		return "", fmt.Errorf("自动借网失败: %w", err)
	}
	id, err := extractWebID(out)
	if err != nil {
		return "", err
	}
	c.webs[sessionID] = id
	log.Printf("[MCP] 会话 %q 已借到网 web_id=%s", sessionID, id)
	return id, nil
}

// extractWebID 从 open_web_tool 的返回文本中提取 web_id。
// open_web_tool 返回的是 ActionResult JSON，web_id 在其中的 value 字段（形如
// "web_id=xxxx"）。这里只做两件确定的事：反序列化结构化 value 字段、剥掉固定的
// "web_id=" 前缀——全程不依赖正则或任何对句柄形态的假设，随服务端契约而行。
func extractWebID(out string) (string, error) {
	var r struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		return "", fmt.Errorf("open_web_tool 返回无法解析: %v: %q", err, out)
	}
	id := strings.TrimSpace(strings.TrimPrefix(r.Value, "web_id="))
	if id == "" {
		return "", fmt.Errorf("open_web_tool 未返回有效 web_id: %q", out)
	}
	return id, nil
}

// CloseSession 结束一个会话：把该会话持有的网归还给服务端并清除本地缓存。
// 会话未借过网时为无操作（幂等）。由会话生命周期边界（新建/退出）调用，
// 由它决定 web 的归还时机，而不依赖模型显式调用 close_web_tool。
func (c *Client) CloseSession(ctx context.Context, sessionID string) {
	c.mu.Lock()
	webID, ok := c.webs[sessionID]
	if !ok {
		c.mu.Unlock()
		return
	}
	delete(c.webs, sessionID)
	c.mu.Unlock()

	if _, err := c.callToolRaw(ctx, "close_web_tool", map[string]any{"web_id": webID}); err != nil {
		log.Printf("[MCP] 归还会话 %q 的网失败: %v", sessionID, err)
		return
	}
	log.Printf("[MCP] 会话 %q 已归还网 web_id=%s", sessionID, webID)
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

	c.idMu.Lock()
	c.id++
	reqID := c.id
	c.idMu.Unlock()

	reqBody, err := json.Marshal(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      reqID,
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
