// Package action 是 spider-nest 全网共享的动作协议层。
//
// 它只描述"做什么"（Action）与"做完返回什么"（ActionResult），不涉及任何执行细节，
// 是各模块之间传递任务与结果的统一中立契约：
//   - web   依赖本包，把动作翻译成具体浏览器操作（纯执行器）；
//   - nest / spider 把 []action.Action 下发给 web 执行，并把结果 []action.ActionResult 回传；
//   - server 把动作结构体映射为 MCP 工具（带显式句柄的请求模型见 request.go）；
//   - 解析核心（action.Spec + action.ParseActions，见 parse.go）对任意格式/配置类型无感知。
//
// 依赖方向始终指向本包，本包不反向依赖任何上层模块（仅依赖 go-rod 的 proto 数据类型）。
// 注意：本文件只声明"纯动作"，不携带 web_id 等路由信息——那些属于带句柄的请求模型（request.go）。
package action

import "github.com/go-rod/rod/lib/proto"

// Go 语言实现密封类技巧
type Action interface {
	isAction()
}

// Navigate 导航到指定 URL 并等待页面完整加载。
type Navigate struct {
	URL string `json:"url" jsonschema:"用于指定要导航的目标 URL"`
}

func (n *Navigate) isAction() {
}

type SelectorType string

const (
	SelectorTypeCSS   SelectorType = "css"
	SelectorTypeXPath SelectorType = "xpath"
)

type Click struct {
	Type     SelectorType `json:"type" jsonschema:"用于指定要点击的元素类型"`
	Selector string       `json:"selector" jsonschema:"用于指定要点击的元素选择器"`
}

func (c *Click) isAction() {
}

type RoleType string

const (
	RoleTypeButton    RoleType = "button"
	RoleTypeLink      RoleType = "link"
	RoleTypeSearchBox RoleType = "searchbox"
	RoleTypeInput     RoleType = "input"
	RoleTypeComboBox  RoleType = "combobox"
	RoleTypeTextBox   RoleType = "textbox"
)

type AXTreeNode struct {
	// BackendDOMNodeID 是 get_interactives_tool / get_content_tool(ax 模式) 返回的 node_id。
	// 它是首选定位方式：一旦提供，则无需（也不会）再按 role/name 去查找。
	BackendDOMNodeID proto.DOMBackendNodeID `json:"backendDOMNodeID,omitzero" jsonschema:"用于指定要操作的元素的 AX 节点ID,如果知晓节点ID优先使用,则无需指定角色和名称"`
	Role             RoleType               `json:"role,omitempty" jsonschema:"用于指定要操作的元素的 AX 角色,仅在未提供节点ID时使用"`
	Name             string                 `json:"name,omitempty" jsonschema:"用于指定要操作的元素的 AX 名称,仅在未提供节点ID时使用"`
	Nth              int                    `json:"nth,omitempty" jsonschema:"用于指定要操作的元素的 AX 节点索引,处理角色和名称出现多个结果时的消歧,默认0"`
}

type ClickByAXTree struct {
	AXTreeNode
}

func (c *ClickByAXTree) isAction() {
}

type Input struct {
	Type     SelectorType `json:"type" jsonschema:"用于指定要输入文本的输入框类型"`
	Selector string       `json:"selector" jsonschema:"用于指定要输入文本的输入框选择器"`
	Text     string       `json:"text" jsonschema:"要输入到输入框中的文本内容"`
	Clear    bool         `json:"clear,omitempty" jsonschema:"是否先清空输入框原有内容再输入,默认为false"`
}

func (in *Input) isAction() {
}

type InputByAXTree struct {
	AXTreeNode
	Text  string `json:"text" jsonschema:"要输入到输入框中的文本内容"`
	Clear bool   `json:"clear,omitempty" jsonschema:"是否先清空输入框原有内容再输入，默认为false"`
}

func (in *InputByAXTree) isAction() {
}

type Scroll struct {
	ScrollY int `json:"scroll_y" jsonschema:"用于指定要滚动的像素数"`
}

func (s *Scroll) isAction() {
}

type JavaScript struct {
	JS   string `json:"js" jsonschema:"用于指定要执行的JavaScript代码"`
	Args []any  `json:"args,omitempty" jsonschema:"用于指定JavaScript代码的参数"`
}

func (j *JavaScript) isAction() {
}

type GetHTML struct {
}

func (g *GetHTML) isAction() {
}

type GetAXContent struct {
}

func (g *GetAXContent) isAction() {
}

type GetStructured struct {
}

func (g *GetStructured) isAction() {
}

type ContentMode string

const (
	ContentModeAX         ContentMode = "ax"         // 默认，首选。基于浏览器可访问性树(AX)输出紧凑Markdown：天然剔除隐藏/装饰/脚本节点，还原标题/列表/表格与父子宫级，且每条可交互元素附 node_id 可直接点击/输入，最省 token 且图文并貌兼备
	ContentModeStructured ContentMode = "structured" // 把已剪枝页面转为可读 Markdown（含标题/列表/表格/链接），保留富文本内联格式（粗体/代码等），适合内容丰富的阅读场景
	ContentModeFull       ContentMode = "full"       // 完整原始HTML（体积大，token 昂贵，当需要精确 DOM 结构/CSS 属性/选择器定位或原始源码时使用）
)

type GetContent struct {
	Mode ContentMode `json:"mode,omitempty" jsonschema:"取数模式,取值:ax(默认,首选,基于可访问性树AX输出紧凑Markdown,每条可交互元素附node_id可点击或输入)/structured(转为可读Markdown,保留粗体行内代码等富文本内联格式,适合内容丰富的阅读)/full(完整原始HTML,保留精确DOM结构与CSS属性,token昂贵)"` // accessibility(默认)|structured|full
}

func DefaultContentMode(mode ContentMode) ContentMode {
	if mode == "" {
		return ContentModeAX
	}
	return mode
}

func (g *GetContent) isAction() {
}

type GetInteractive struct {
}

func (g *GetInteractive) isAction() {
}

// Artifact 描述一份落盘产物的保存位置与命名，供各类产物型动作/函数复用。
// Dir 为空时使用默认下载目录 defaultDownloadDir；Name 为空时由生成方按内容自动生成。
type Artifact struct {
	// Dir 指定产物落盘目录；为空时使用默认下载目录 defaultDownloadDir。
	Dir string `json:"artifact_dir,omitempty" jsonschema:"用于指定产物要保存的目录"`
	// Name 指定产物文件名（不含扩展名）；为空时按生成方逻辑自动生成。
	Name string `json:"artifact_name,omitempty" jsonschema:"用于指定产物文件名（不含扩展名）"`
}

type GetPDF struct {
	*proto.PagePrintToPDF
	Artifact
}

func (g *GetPDF) isAction() {
}
