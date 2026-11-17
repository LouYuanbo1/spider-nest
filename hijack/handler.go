package hijack

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// HijackHandler 一次请求拦截的处理函数，签名与 go-rod 的 handler 对齐。
// 职责是"决定要不要拦 + 拦了做什么"（读请求/拍板放行或回喂假响应）。
//
// 注意：router 为每个命中请求在独立 goroutine 里调用本 handler，故 handler
// 必须自洽（要么放行、要么回喂），不能两者都不做导致请求永久悬挂。
type HijackHandler func(*rod.Hijack)

// HijackRule 一条拦截规则：URL 命中 Pattern（可选限定 Method）后交给 Handler。
// 与 config.HijackConfig 对应：配置是静态描述，这里是运行时 handler。
type HijackRule struct {
	Pattern string
	Method  string
	Handler HijackHandler
}

// HijackWrapper 包裹自定义 handler，做两件事：
//  1. 方法不命中时放行到真实网络（避免误伤同 URL 的其他方法）；
//  2. panic 兜底放行——浏览器级拦截若不放行会把整池页面的请求都挂起，
//     进而卡死 Spider 的网池。
//
// logger 为 nil 时在创建闭包前兜底为 slog.Default()（幂等，支持本函数被独立调用；
// 主路径 EnableRouter 已兜底）。闭包内与 defer 中不再做 nil 判断，避免在并发
// 触发的 panic 路径上写共享捕获变量而引入数据竞争。
func HijackWrapper(rule HijackRule, logger *slog.Logger) func(*rod.Hijack) {
	if logger == nil {
		logger = slog.Default()
	}
	return func(h *rod.Hijack) {
		defer func() {
			if r := recover(); r != nil {
				logger.Info("hijack handler 出错，已放行",
					slog.String("method", rule.Method),
					slog.String("pattern", rule.Pattern),
					slog.Any("error", r),
				)
				h.ContinueRequest(&proto.FetchContinueRequest{})
			}
		}()
		if rule.Method != "" && !strings.EqualFold(rule.Method, h.Request.Method()) {
			h.ContinueRequest(&proto.FetchContinueRequest{}) // 方法不匹配，放行
			return
		}
		rule.Handler(h)
	}
}

// replay 用注入的 client 把命中请求真实回代，把原始响应装进 h.Response 供快照。
// 失败（client 缺失 / 回代出错 / 取不到原始响应）一律回退为真实放行，避免请求悬挂。
// 返回是否可取到响应快照；成功后调用方仍需自己 ContinueRequest 放行浏览器原请求，
// 页面才会拿到真实响应（而非回代那份）。
func replay(h *rod.Hijack, c *http.Client) bool {
	if c == nil {
		h.ContinueRequest(&proto.FetchContinueRequest{})
		return false
	}
	if err := h.LoadResponse(c, true); err != nil {
		h.ContinueRequest(&proto.FetchContinueRequest{})
		return false
	}
	if h.Response.RawResponse == nil {
		h.ContinueRequest(&proto.FetchContinueRequest{})
		return false
	}
	return true
}

// call 统一的回调助手：fn 非空才调用，收敛各处的空指针判断，让 handler 保持简短。
func call[T any](fn func(*T), v *T) {
	if fn != nil {
		fn(v)
	}
}

// RequestHandler 捕获命中请求并回调 fn（方法/URL/请求头/请求体），再放行到真实网络。
// 与 MonitorHandler 的区别：结果不（仅）走日志，而是交给调用方 fn 自由消费，
// 便于把请求头和请求体接进自己的流程。
func RequestHandler(fn func(*RequestCapture)) HijackHandler {
	return func(h *rod.Hijack) {
		call(fn, &RequestCapture{
			Method:  h.Request.Method(),
			URL:     h.Request.URL().String(),
			Headers: h.Request.Headers(),
			Body:    h.Request.Body(),
		})
		h.ContinueRequest(&proto.FetchContinueRequest{})
	}
}

// ResponseHandler 用注入的 client 真实回代请求，取响应快照回调 fn，再放行浏览器原请求
// 让页面拿到真实响应。用于只需响应数据（保存/处理）的场景。
func ResponseHandler(c *http.Client, fn func(*ResponseCapture)) HijackHandler {
	return func(h *rod.Hijack) {
		if !replay(h, c) {
			return
		}
		call(fn, &ResponseCapture{
			StatusCode: h.Response.RawResponse.StatusCode,
			Status:     h.Response.RawResponse.Status,
			Headers:    h.Response.Headers(),
			Body:       h.Response.Body(),
		})
		h.ContinueRequest(&proto.FetchContinueRequest{})
	}
}

// RoundTripHandler 用注入的 client 真实回代请求，同一次回代同时取请求与响应快照，
// 作为完整往返回调 fn，再放行浏览器原请求。用于需同时消费请求数据与返回数据的场景。
func RoundTripHandler(c *http.Client, fn func(*RoundTripCapture)) HijackHandler {
	return func(h *rod.Hijack) {
		if !replay(h, c) {
			return
		}
		call(fn, &RoundTripCapture{
			Request: &RequestCapture{
				Method:  h.Request.Method(),
				URL:     h.Request.URL().String(),
				Headers: h.Request.Headers(),
				Body:    h.Request.Body(),
			},
			Response: &ResponseCapture{
				StatusCode: h.Response.RawResponse.StatusCode,
				Status:     h.Response.RawResponse.Status,
				Headers:    h.Response.Headers(),
				Body:       h.Response.Body(),
			},
		})
		h.ContinueRequest(&proto.FetchContinueRequest{})
	}
}

// MonitorHandler 返回一个"真实监听"处理函数：读取命中请求的方法/URL/请求头/请求体，
// 记录到 logger，然后调用 ContinueRequest 原样放行到真实网络，不改动页面行为。
// 始终全量采集请求头与请求体（分别经 h.Request.Headers() / h.Request.Body()），
// 无需开关——监听的意义就在于完整观测。
func MonitorHandler(logger *slog.Logger) HijackHandler {
	return func(h *rod.Hijack) {
		if logger == nil {
			logger = slog.Default()
		}
		logger.Info("请求被监听(真实放行)",
			slog.String("method", h.Request.Method()),
			slog.String("url", h.Request.URL().String()),
			slog.Any("headers", h.Request.Headers()),
			slog.String("body", h.Request.Body()),
		)
		// 真实监听：放行到真实网络，页面请求不被挂起。
		h.ContinueRequest(&proto.FetchContinueRequest{})
	}
}

type hijackable interface {
	HijackRequests() *rod.HijackRouter
}

func EnableRouter[T hijackable](source T, rules []HijackRule, logger *slog.Logger) (*rod.HijackRouter, error) {
	if logger == nil {
		logger = slog.Default()
	}
	router := source.HijackRequests()
	for _, r := range rules {
		if r.Pattern == "" {
			return nil, fmt.Errorf("hijack 规则无效：pattern 不能为空")
		}
		if r.Handler == nil {
			r.Handler = func(*rod.Hijack) {} // 兜底：命中也不填内容，回喂空 200
		}
		if err := router.Add(r.Pattern, proto.NetworkResourceType(""), HijackWrapper(r, logger)); err != nil {
			return nil, fmt.Errorf("注册 hijack 规则 %q(%s) 失败: %v", r.Pattern, r.Method, err)
		}
	}
	return router, nil
}
