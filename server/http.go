package server

import (
	"log"
	"net"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewHTTPHandler 以"显式状态句柄"模式把 MCP 服务器经 HTTP（Streamable 传输）暴露为标准 http.Handler：
// 客户端先调 open_web_tool 借一张网拿 web_id，再用同一 web_id 做后续浏览器操作，结束调
// close_web_tool 归还。传输层保持无状态（Stateless），状态只收敛在 WebSessionManager 的句柄表里，
// 因而天然可水平扩展——请求落到任意实例，只要 web_id 解析得到。
//
// implementation / opts 与 NewMCPServer 含义一致；返回的 handler 可直接挂到既有路由器下，
// 或交由 http.ListenAndServe / RunHTTPServer 托管。
func NewHTTPHandler(m *WebSessionManager, implementation *mcp.Implementation, opts *mcp.ServerOptions) http.Handler {
	srv := NewMCPServer(m, implementation, opts)

	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return srv
	}, &mcp.StreamableHTTPOptions{
		// 无状态：每个请求用临时会话独立处理，无需客户端维护 Mcp-Session-Id，
		// 部署简单，GET / DELETE 返回 405。若需要跨请求会话请改为 false。
		Stateless:    true,
		JSONResponse: true,
	})
}

// RunHTTPServer 在 addr（如 ":8080"）上提供 MCP 工具服务。
// 只需要一条命令即可把浏览器工具通过 HTTP 暴露出去。
func RunHTTPServer(m *WebSessionManager, addr string, implementation *mcp.Implementation, opts *mcp.ServerOptions) error {
	srv := &http.Server{
		Addr:    addr,
		Handler: NewHTTPHandler(m, implementation, opts),
	}
	log.Printf("[MCP HTTP] 服务已启动: http://127.0.0.1%s （POST JSON-RPC，支持 Streamable HTTP 客户端）", addr)
	return srv.ListenAndServe()
}

// Listen 在本机随机端口上启动 MCP HTTP 服务（http://127.0.0.1:<随机端口>），返回实际地址与关闭函数。
// 供其它进程（如 cmd/ai）在内部直接嵌入一套本地 MCP 服务使用：无需预先手动启动独立 server 进程，
// agent 拿到返回的 baseURL 即可直接连。实现 / opts 含义同 NewMCPServer。
func Listen(m *WebSessionManager, implementation *mcp.Implementation, opts *mcp.ServerOptions) (string, func(), error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	srv := &http.Server{Handler: NewHTTPHandler(m, implementation, opts)}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[MCP HTTP] serve 退出: %v", err)
		}
	}()
	baseURL := "http://" + ln.Addr().String()
	log.Printf("[MCP HTTP] 本地服务已启动: %s", baseURL)
	return baseURL, func() { _ = srv.Close() }, nil
}
