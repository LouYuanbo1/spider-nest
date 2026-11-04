package action

// 本文件定义"带显式状态句柄的请求模型"与"网生命周期动作"，仅供 server（尤其 MCP 工具入参）使用。
//
// 命名约定：action.go 里的 *Action 是"做什么"的纯动作协议；本文件的 *Request 则是"一次带路由入参的请求"——
// 是在纯动作之上再系上一个 web_id 句柄、用于路由到目标网。二者对仗：Action（做什么）vs Request（一次请求/入参）。
//
// 设计动机：action.go 里的 *Action 是纯动作协议，会（且应）被 nest 下发、worker 执行、parse 解析等
// 非 LLM / 非 MCP 链路复用，不应沾染"该动作作用于哪张网"这类路由信息。
// 因此这里把动作"内嵌"进带 web_id 的请求模型：字段经 MCP SDK 的 schema 反射会扁平提升到顶层，
// 模型端看到的是 { web_id, ...原动作字段 } 的平整入参；同时底层执行时依旧只消费内嵌的纯动作。
//
// 依循"无状态核心 + 显式状态句柄"：浏览器状态不再藏在传输层会话里，而是作为明确的 web_id
// 由模型在动作之间显式传递。web_id 由 open_web_tool 借网时分配，单个用户（会话）独占一张网。

// WebIDDesc 是所有请求模型 web_id 字段的统一说明（struct tag 无法拼接变量，故在各类中内联展开）。
const WebIDDesc = "浏览会话句柄 web_id,由 open_web_tool 借网时返回;每个用户独占一张网,使用前必须先调用 open_web_tool 获取"

// 一个用户（会话）独占一张网：此后所有浏览器动作携带同一 web_id 即可路由到这张网。

type NavigateRequest struct {
	WebID string `json:"web_id" jsonschema:"浏览会话句柄 web_id,由 open_web_tool 借网时返回;每个用户独占一张网,使用前必须先调用 open_web_tool 获取"`
	Navigate
}

type ClickRequest struct {
	WebID string `json:"web_id" jsonschema:"浏览会话句柄 web_id,由 open_web_tool 借网时返回;每个用户独占一张网,使用前必须先调用 open_web_tool 获取"`
	Click
}

type ClickByAXRequest struct {
	WebID string `json:"web_id" jsonschema:"浏览会话句柄 web_id,由 open_web_tool 借网时返回;每个用户独占一张网,使用前必须先调用 open_web_tool 获取"`
	ClickByAXTree
}

type InputRequest struct {
	WebID string `json:"web_id" jsonschema:"浏览会话句柄 web_id,由 open_web_tool 借网时返回;每个用户独占一张网,使用前必须先调用 open_web_tool 获取"`
	Input
}

type InputByAXRequest struct {
	WebID string `json:"web_id" jsonschema:"浏览会话句柄 web_id,由 open_web_tool 借网时返回;每个用户独占一张网,使用前必须先调用 open_web_tool 获取"`
	InputByAXTree
}

type ScrollRequest struct {
	WebID string `json:"web_id" jsonschema:"浏览会话句柄 web_id,由 open_web_tool 借网时返回;每个用户独占一张网,使用前必须先调用 open_web_tool 获取"`
	Scroll
}

type JavaScriptRequest struct {
	WebID string `json:"web_id" jsonschema:"浏览会话句柄 web_id,由 open_web_tool 借网时返回;每个用户独占一张网,使用前必须先调用 open_web_tool 获取"`
	JavaScript
}

type GetHTMLRequest struct {
	WebID string `json:"web_id" jsonschema:"浏览会话句柄 web_id,由 open_web_tool 借网时返回;每个用户独占一张网,使用前必须先调用 open_web_tool 获取"`
	GetHTML
}

type GetAXContentRequest struct {
	WebID string `json:"web_id" jsonschema:"浏览会话句柄 web_id,由 open_web_tool 借网时返回;每个用户独占一张网,使用前必须先调用 open_web_tool 获取"`
	GetAXContent
}

type GetStructuredRequest struct {
	WebID string `json:"web_id" jsonschema:"浏览会话句柄 web_id,由 open_web_tool 借网时返回;每个用户独占一张网,使用前必须先调用 open_web_tool 获取"`
	GetStructured
}

type GetContentRequest struct {
	WebID string `json:"web_id" jsonschema:"浏览会话句柄 web_id,由 open_web_tool 借网时返回;每个用户独占一张网,使用前必须先调用 open_web_tool 获取"`
	GetContent
}

type GetInteractiveRequest struct {
	WebID string `json:"web_id" jsonschema:"浏览会话句柄 web_id,由 open_web_tool 借网时返回;每个用户独占一张网,使用前必须先调用 open_web_tool 获取"`
	GetInteractive
}

type GetPDFRequest struct {
	WebID string `json:"web_id" jsonschema:"浏览会话句柄 web_id,由 open_web_tool 借网时返回;每个用户独占一张网,使用前必须先调用 open_web_tool 获取"`
	GetPDF
}

// ---- 网生命周期动作（非浏览器操作，专供 server 的句柄借还） ----

// OpenWeb 从空闲池借一张网，由服务端分配 web_id 句柄并返回。
// 会话开始时应首先调用它取句柄，之后所有浏览器动作均携带同一 web_id。
type OpenWeb struct{}

// CloseWeb 用完把 web_id 对应的网归还空闲池。对话结束时调用；也允许随时归还。
type CloseWeb struct {
	WebID string `json:"web_id" jsonschema:"要归还的浏览会话句柄,由 open_web_tool 返回"`
}

// ListWebs 列出当前所有仍被占用（未归还）的 web_id 句柄，便于模型管理自己打开的资源。
type ListWebs struct{}
