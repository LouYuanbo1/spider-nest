package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/LouYuanbo1/spider-nest/action"
	"github.com/LouYuanbo1/spider-nest/nest"
	"github.com/LouYuanbo1/spider-nest/spider"
)

// maxBodySize 限制直连抓回响应体的大小（按字节），防止超大响应把内存撑爆。
const maxBodySize = 1 << 20 // 1 MiB

// apiResponse 统一响应信封：任何端点都返回 {code, message, data}，
// 便于调用方用 code==0 判断成功。code!=0 时 message 携带错误说明。
type apiResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
	Data    any    `json:"data,omitempty"`
}

// fetchRequest /api/fetch 的请求体：与直连保持一致（默认 GET，空 Body 即无请求体）。
type fetchRequest struct {
	Method string `json:"method,omitempty"`
	URL    string `json:"url"`
	Body   string `json:"body,omitempty"`
}

// fetchData /api/fetch 的响应数据：状态码 + 关键请求头 + 响应体。
type fetchData struct {
	StatusCode int               `json:"status_code"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
	Length     int               `json:"length"`
	Truncated  bool              `json:"truncated"`
}

// crawlRequest /api/crawl 的请求体：actions 是动作序列，结构与
// action.ParseJSON 约定的 {"actions": [...]} 一致。
type crawlRequest struct {
	Actions json.RawMessage `json:"actions"`
}

// crawlData /api/crawl 的响应数据：逐条动作的执行结果。
type crawlData struct {
	Results []action.ActionResult `json:"results"`
}

// Server 是一座蜘蛛巢的 HTTP 外观（facade），本身为泛型类型：
//
//	type Server[T any, S nest.NestSpider[T]]    // S = 被托管的指针蜘蛛
//	nest: *nest.Nest[T, S]                       // 泛型地持有这座巢
//
// 泛型约束直接复用 nest 包导出的 NestSpider（指针 *T 且实现 spider.Spider），
// 不重复自定义。泛型带来的复用：把 T/S 实例化为 *spider.Weaver、*spider.Portia 等
// 都可用同一套 Server 代码。凡是 spider.Spider 通用契约（这里指 Crawl）提供的抓取
// 能力，都沉淀在 Server 上；而某类蜘蛛额外的方法（如 Portia.Fetch，不在接口里）作为
// 具体扩展，由消费方在 main 里对具体实例注册，避免用强转/断言污染泛型核心。
type Server[T any, S nest.NestSpider[T]] struct {
	nest *nest.Nest[T, S]
}

// NewServer 创建一座待命中的爬虫服务端。
//   - size：巢的并发上限（可同时活动的蜘蛛数）；
//   - create：巢惰性建蜘蛛的工厂，每次调用产出一只全新的 S（内含它自己的浏览器）。
//     配置归属沿用 nest 设计：巢不持有配置，完全由工厂闭包捕获。
func NewServer[T any, S nest.NestSpider[T]](size int, create func() (S, error)) (*Server[T, S], error) {
	n, err := nest.NewNest(size, create)
	if err != nil {
		return nil, err
	}
	return &Server[T, S]{nest: n}, nil
}

// Nest 暴露底层的巢，供调用方在此基础上补充类型特化的端点（如 Portia 的 Fetch）。
func (s *Server[T, S]) Nest() *nest.Nest[T, S] { return s.nest }

// Routes 注册通用端点：/api/crawl（任意蜘蛛都有的 Crawl）与 /healthz。
// 返回可扩展的 mux：消费方可追加类型特化端点（见 main 里追加 /api/fetch）。
func (s *Server[T, S]) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/crawl", s.handleCrawl)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, apiResponse{Code: 0, Message: "alive"})
	})
	return mux
}

// Serve 用默认路由（通用端点）阻塞监听 addr。返回时代表服务已退出或出错。
func (s *Server[T, S]) Serve(addr string) error { return s.ServeHandler(addr, s.Routes()) }

// ServeHandler 用调用方装配好的 mux 阻塞监听 addr，供 main 扩展后统一启动。
func (s *Server[T, S]) ServeHandler(addr string, h http.Handler) error {
	log.Printf("爬虫服务已启动，监听 %s\n", addr)
	log.Printf("端点: POST /api/fetch 直连抓取 | POST /api/crawl 动作抓取 | GET /healthz 健康检查")
	return http.ListenAndServe(addr, h)
}

// Close 关闭整座巢：逐个关闭池中的蜘蛛（连带各自的浏览器/页面）。
func (s *Server[T, S]) Close() error {
	if s == nil || s.nest == nil {
		return nil
	}
	return s.nest.Close()
}

// writeJSON 统一输出 JSON，并处理序列化失败。
func writeJSON(w http.ResponseWriter, code int, resp apiResponse) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("写响应失败: %v", err)
	}
}

// handleCrawl 动作序列抓取：把请求里的动作序列交给巢内蜘蛛 Crawl 织一遍，
// 逐条结果原样返回。动作结构与 action.ParseJSON 的格式一致。巢的 Crawl 内部
// 就是 BorrowSpider + s.Crawl——借一只蜘蛛、跑完自动归还，天然免「借了不还」。
// 它只依赖 spider.Spider 通用契约，因此对任意 T/S 的巢都成立，沉淀在泛型 Server 上。
func (s *Server[T, S]) handleCrawl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Code: 405, Message: "仅支持 POST"})
		return
	}
	var req crawlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 400, Message: "请求体解析失败: " + err.Error()})
		return
	}
	actions, err := action.ParseJSON(bytes.NewReader(req.Actions))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Code: 400, Message: "动作序列解析失败: " + err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	results, err := s.nest.Crawl(ctx, actions)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, apiResponse{Code: 502, Message: "动作执行失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Code: 0, Message: "ok", Data: crawlData{Results: results}})
}

// PortiaFetch 构造 /api/fetch 的处理函数：这是 Portia 独有直连能力的「具体扩展」。
// 由于 Fetch 不在 spider.Spider 接口内，无法放进泛型 Server 的方法里，故作为本包
// 导出的自由函数，接收「已实例化为 *spider.Portia 的巢」并返回对应 handler；
// 巢的 Acquire 返回具体类型 *spider.Portia，正好能直接调 Fetch，无需类型断言。
func PortiaFetch(nt *nest.Nest[spider.Portia, *spider.Portia]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Code: 405, Message: "仅支持 POST"})
			return
		}
		var req fetchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, apiResponse{Code: 400, Message: "请求体解析失败: " + err.Error()})
			return
		}
		if req.URL == "" {
			writeJSON(w, http.StatusBadRequest, apiResponse{Code: 400, Message: "url 不能为空"})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()

		p, err := nt.Acquire()
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, apiResponse{Code: 503, Message: "借用蜘蛛失败: " + err.Error()})
			return
		}
		defer func() { _ = nt.Release(p) }() // 借-用-还：无论成败都要把蜘蛛放回巢

		resp, err := p.Fetch(ctx, spider.Target{
			Method: req.Method,
			URL:    req.URL,
			Body:   req.Body,
		})
		if err != nil {
			writeJSON(w, http.StatusBadGateway, apiResponse{Code: 502, Message: "直连抓取失败: " + err.Error()})
			return
		}
		defer resp.Body.Close()

		var body strings.Builder
		data := fetchData{
			StatusCode: resp.StatusCode,
			Headers:    map[string]string{},
		}
		v := resp.Header.Get("Content-Type")
		if v == "" {
			v = "unknown"
		}
		data.Headers["Content-Type"] = v
		for _, k := range []string{"Content-Length", "Server"} {
			if val := resp.Header.Get(k); val != "" {
				data.Headers[k] = val
			}
		}

		if _, err := io.Copy(&body, io.LimitReader(resp.Body, maxBodySize)); err != nil {
			writeJSON(w, http.StatusBadGateway, apiResponse{Code: 502, Message: "读取响应体失败: " + err.Error()})
			return
		}
		data.Body = body.String()
		data.Length = body.Len()
		data.Truncated = body.Len() == maxBodySize && resp.ContentLength > maxBodySize

		writeJSON(w, http.StatusOK, apiResponse{Code: 0, Message: "ok", Data: data})
	}
}
