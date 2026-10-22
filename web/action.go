package web

import "github.com/go-rod/rod/lib/proto"

// Go 语言实现密封类技巧
type Action interface {
	isAction()
}

type Navigate struct {
	URL string `json:"url" jsonschema:"description=用于指定要导航的目标 URL"`
}

func (n *Navigate) isAction() {
}

type SelectorType string

const (
	SelectorTypeCSS   SelectorType = "css"
	SelectorTypeXPath SelectorType = "xpath"
)

type Click struct {
	Type     SelectorType `json:"type" jsonschema:"description=用于指定要点击的元素类型"`
	Selector string       `json:"selector" jsonschema:"description=用于指定要点击的元素选择器"`
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
	BackendDOMNodeID proto.DOMBackendNodeID `json:"backendDOMNodeID,omitzero" jsonschema:"description=用于指定要操作的元素的 AX 节点ID,如果知晓节点ID优先使用,则无需指定角色和名称"`
	Role             RoleType               `json:"role" jsonschema:"description=用于指定要操作的元素的 AX 角色"`
	Name             string                 `json:"name" jsonschema:"description=用于指定要操作的元素的 AX 名称"`
	Nth              int                    `json:"nth" jsonschema:"description=用于指定要操作的元素的 AX 节点索引,处理角色和名称相同的问题,默认0"`
}

type ClickByAXTree struct {
	AXTreeNode
}

func (c *ClickByAXTree) isAction() {
}

type Input struct {
	Type     SelectorType `json:"type" jsonschema:"description=用于指定要输入文本的输入框类型"`
	Selector string       `json:"selector" jsonschema:"description=用于指定要输入文本的输入框选择器"`
	Text     string       `json:"text" jsonschema:"description=要输入到输入框中的文本内容"`
	Clear    bool         `json:"clear" jsonschema:"description=是否先清空输入框原有内容再输入,默认为false"`
}

func (c *Input) isAction() {
}

type InputByAXTree struct {
	AXTreeNode
	Text  string `json:"text" jsonschema:"description=要输入到输入框中的文本内容"`
	Clear bool   `json:"clear" jsonschema:"description=是否先清空输入框原有内容再输入，默认为false"`
}

func (c *InputByAXTree) isAction() {
}

type Scroll struct {
	ScrollY int `json:"scroll_y" jsonschema:"description=用于指定要滚动的像素数"`
}

func (s *Scroll) isAction() {
}

type JavaScript struct {
	JS   string `json:"js" jsonschema:"description=用于指定要执行的JavaScript代码"`
	Args []any  `json:"args" jsonschema:"description=用于指定JavaScript代码的参数"`
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
	Mode ContentMode `json:"mode,omitempty" jsonschema:"description=取数模式,enum=ax,enum=structured,enum=full,default=ax" jsonschema_description:"取数模式。默认ax(首选)：基于浏览器可访问性树AX输出紧凑Markdown，天然剔除隐藏装饰及脚本节点并还原标题列表表格与层级，每条可交互元素附node_id可点击或输入，最省token且图文兼备。structured=已剪枝页面转为可读Markdown，保留粗体行内代码等富文本内联格式，适合内容丰富的阅读。full=完整原始HTML，保留精确DOM结构与CSS属性，仅在需要原始源码代码数据表格或精确选择器定位时使用，token昂贵"` // accessibility(默认)|structured|full
}

func DefaultContentMode(mode ContentMode) ContentMode {
	if mode == "" {
		return ContentModeAX
	}
	return mode
}

type GetInteractive struct {
}

func (g *GetInteractive) isAction() {
}

// Artifact 描述一份落盘产物的保存位置与命名，供各类产物型动作/函数复用。
// Dir 为空时使用默认下载目录 defaultDownloadDir；Name 为空时由生成方按内容自动生成。
type Artifact struct {
	// Dir 指定产物落盘目录；为空时使用默认下载目录 defaultDownloadDir。
	Dir string `json:"artifact_dir,omitempty" jsonschema:"description=用于指定产物要保存的目录"`
	// Name 指定产物文件名（不含扩展名）；为空时按生成方逻辑自动生成。
	Name string `json:"artifact_name,omitempty" jsonschema:"description=用于指定产物文件名（不含扩展名）"`
}

type GetPDF struct {
	*proto.PagePrintToPDF
	Artifact
}

func (g *GetPDF) isAction() {
}
