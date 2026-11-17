package hijack

import (
	"net/http"

	"github.com/go-rod/rod/lib/proto"
)

// RequestCapture 一次被监听到的请求的有效载荷：方法 / URL / 请求头 / 请求体。
// 请求头取自 devtools 的 Fetch 域数据；请求体仅字符串形式（二进制体 CDP 不采集）。
// 这是直连重放（Portia）与"偷头"组件共享的数据契约。
type RequestCapture struct {
	Method  string
	URL     string
	Headers proto.NetworkHeaders
	Body    string
}

// ResponseCapture 一次被监听到的响应的有效载荷：状态码 / 状态文本 / 响应头 / 响应体。
// 响应取自 LoadResponse 真实回代的结果（h.Response.RawResponse）；响应体仅字符串形式。
type ResponseCapture struct {
	StatusCode int
	Status     string
	Headers    http.Header
	Body       string
}

// RoundTripCapture 一次完整的"请求 → 响应"往返快照，由请求侧与响应侧两块组成：
// 请求来自被监听请求（h.Request），响应来自 LoadResponse 回代拿到的真实响应
// （h.Response）。一次回调即可同时消费请求数据与返回数据。
type RoundTripCapture struct {
	Request  *RequestCapture  // 请求侧：方法 / URL / 请求头 / 请求体
	Response *ResponseCapture // 响应侧：状态码 / 状态文本 / 响应头 / 响应体
}
