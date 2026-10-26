package action

import (
	"fmt"

	"github.com/go-rod/rod/lib/proto"
)

// 声明式动作的"配置文件无关"解析核心。
//
// 本文件把「格式无关的动作描述 -> []Action」的编译逻辑抽离出来，与任何配置文件
// 类型（YAML/JSON/...）解耦：不同前端格式各自实现 Spec，往同一个 ParseActions
// 投喂动作即可复用整套解析/校验规则。故：
//   - 不依赖 config 包（也不关心 HijackConfig 等配置结构）；
//   - 不依赖具体格式库（不必 import yaml / json，DTO 仅携带 yaml/json 结构标签）；
//   - 将来要支持 JSON，只需新增一个满足 Spec 的 jsonSpec 并复用 ParseActions。
//
// 对应的 YAML 前端（yamlSpec）可放在任意消费方（如声明式 YAML 的加载包）：
// 实现 Spec 的三个方法后，把动作描述喂给 ParseActions 即可复用整套解析/校验，
// 其余格式（JSON/...）前端可同样仿照实现。

// Spec 一条声明式动作的抽象、与格式无关的描述。
// 不同前端格式（YAML/JSON/...）各自实现该接口，往同一解析核心（ParseActions）
// 里投喂动作。核心只依赖本包与 go-rod 的 proto 数据类型，故对配置文件类型无关。
type Spec interface {
	// Kind 抽出动作的 type 字段；缺失或不可解析时报错。
	Kind() (string, error)
	// Decode 把整条动作解到指定 DTO；target 为 *xxxDTO（见本文件下方定义）。
	Decode(target any) error
	// Line 返回该动作在源文档中的行号，用于报错定位。
	Line() int
}

// ParseActions 把一组格式无关的动作描述编译为 []Action。
// 这是"配置文件无关"的解析核心——不依赖 config 类型，可被任意前端复用。
func ParseActions(specs []Spec) ([]Action, error) {
	acts := make([]Action, 0, len(specs))
	for i, spec := range specs {
		act, err := parseAction(spec, i)
		if err != nil {
			return nil, err
		}
		acts = append(acts, act)
	}
	return acts, nil
}

// locate 生成带下标与行号的定位前缀，便于在长任务里精准指出出错的动作。
func locate(s Spec, i int, kind string) string {
	return fmt.Sprintf("actions[%d](type=%q, line %d)", i, kind, s.Line())
}

// parseAction 先抽取 type 字段，再按类型把 Spec 解到对应 DTO 并编译为 Action。
func parseAction(s Spec, i int) (Action, error) {
	kind, err := s.Kind()
	if err != nil {
		return nil, fmt.Errorf("%s: 无法识别动作头: %w", locate(s, i, ""), err)
	}
	at := locate(s, i, kind)
	if kind == "" {
		return nil, fmt.Errorf("%s: 缺少必填的 type 字段", at)
	}

	switch kind {
	case "navigate":
		var dto navigateDTO
		if err := s.Decode(&dto); err != nil {
			return nil, err
		}
		if dto.URL == "" {
			return nil, fmt.Errorf("%s: url 必填", at)
		}
		return &Navigate{URL: dto.URL}, nil

	case "click":
		var dto clickDTO
		if err := s.Decode(&dto); err != nil {
			return nil, err
		}
		if dto.Selector == "" {
			return nil, fmt.Errorf("%s: selector 必填", at)
		}
		return &Click{Type: dto.resolve(), Selector: dto.Selector}, nil

	case "click_ax":
		var dto clickAXDTO
		if err := s.Decode(&dto); err != nil {
			return nil, err
		}
		return &ClickByAXTree{AXTreeNode: dto.toAX()}, nil

	case "input":
		var dto inputDTO
		if err := s.Decode(&dto); err != nil {
			return nil, err
		}
		if dto.Text == "" {
			return nil, fmt.Errorf("%s: text 必填", at)
		}
		return &Input{Type: dto.resolve(), Selector: dto.Selector, Text: dto.Text, Clear: dto.Clear}, nil

	case "input_ax":
		var dto inputAXDTO
		if err := s.Decode(&dto); err != nil {
			return nil, err
		}
		if dto.Text == "" {
			return nil, fmt.Errorf("%s: text 必填", at)
		}
		return &InputByAXTree{AXTreeNode: dto.toAX(), Text: dto.Text, Clear: dto.Clear}, nil

	case "scroll":
		var dto scrollDTO
		if err := s.Decode(&dto); err != nil {
			return nil, err
		}
		return &Scroll{ScrollY: dto.ScrollY}, nil

	case "js", "javascript":
		var dto jsDTO
		if err := s.Decode(&dto); err != nil {
			return nil, err
		}
		if dto.JS == "" {
			return nil, fmt.Errorf("%s: js 必填", at)
		}
		return &JavaScript{JS: dto.JS, Args: dto.Args}, nil

	case "get_html":
		return &GetHTML{}, nil

	case "get_ax", "get_ax_content":
		return &GetAXContent{}, nil

	case "get_structured":
		return &GetStructured{}, nil

	case "get_content":
		var dto contentDTO
		if err := s.Decode(&dto); err != nil {
			return nil, err
		}
		mode := DefaultContentMode(dto.Mode)
		switch mode {
		case ContentModeAX, ContentModeStructured, ContentModeFull:
			return &GetContent{Mode: mode}, nil
		default:
			return nil, fmt.Errorf("%s: 非法 mode=%q，可选 ax|structured|full", at, dto.Mode)
		}

	case "get_interactive", "get_interactives":
		return &GetInteractive{}, nil

	case "get_pdf", "pdf":
		var dto pdfDTO
		if err := s.Decode(&dto); err != nil {
			return nil, err
		}
		return dto.toAction(), nil

	default:
		return nil, fmt.Errorf("%s: 未知动作类型，支持 navigate/click/click_ax/input/input_ax/scroll/js"+
			"/get_html/get_ax/get_structured/get_content/get_interactive/get_pdf", at)
	}
}

// 以下为各动作的 YAML/JSON 视图（DTO）。与动作结构体一一对应，仅负责声明式收参。
// DTO 携带 yaml: 与 json: 双标签，使同一解析核心可被 YAML/JSON 等前端复用；
// 具体由哪个库解码，取决于前端 Spec 的 Decode 实现，核心本身不依赖任何格式库。

type navigateDTO struct {
	URL string `yaml:"url" json:"url"`
}

// selectorDTO 同时服务 click/input：selector_type 用独立键，避免与动作类型 type 键冲突。
type selectorDTO struct {
	Type     SelectorType `yaml:"selector_type" json:"selector_type"`
	Selector string       `yaml:"selector" json:"selector"`
}

// resolve 把空的 selector_type 规范化为默认 css。
func (s *selectorDTO) resolve() SelectorType {
	if s.Type == "" {
		return SelectorTypeCSS
	}
	return s.Type
}

type clickDTO struct {
	selectorDTO `yaml:",inline" json:",inline"`
}

type inputDTO struct {
	selectorDTO `yaml:",inline" json:",inline"`
	Text        string `yaml:"text" json:"text"`
	Clear       bool   `yaml:"clear" json:"clear"`
}

// axDTO 是 AX 语义定位动作的共同字段：node_id 只要非 0 即优先，否则用 role+name(+nth) 兜底。
type axDTO struct {
	NodeID int      `yaml:"node_id" json:"node_id"`
	Role   RoleType `yaml:"role" json:"role"`
	Name   string   `yaml:"name" json:"name"`
	Nth    int      `yaml:"nth" json:"nth"`
}

func (a axDTO) toAX() AXTreeNode {
	return AXTreeNode{
		BackendDOMNodeID: proto.DOMBackendNodeID(a.NodeID),
		Role:             a.Role,
		Name:             a.Name,
		Nth:              a.Nth,
	}
}

type clickAXDTO struct {
	axDTO `yaml:",inline" json:",inline"`
}

type inputAXDTO struct {
	axDTO `yaml:",inline" json:",inline"`
	Text  string `yaml:"text" json:"text"`
	Clear bool   `yaml:"clear" json:"clear"`
}

type scrollDTO struct {
	ScrollY int `yaml:"scroll_y" json:"scroll_y"`
}

type jsDTO struct {
	JS   string `yaml:"js" json:"js"`
	Args []any  `yaml:"args" json:"args"`
}

type contentDTO struct {
	Mode ContentMode `yaml:"mode" json:"mode"`
}

// pdfDTO 抽取 PagePrintToPDF 的常用打印参数 + Artifact 落盘配置。
// 打印参数使用指针接收以区分「未设置」与「显式 0」，未设置时交给浏览器默认值。
type pdfDTO struct {
	ArtifactDir         string   `yaml:"artifact_dir" json:"artifact_dir"`
	ArtifactName        string   `yaml:"artifact_name" json:"artifact_name"`
	Landscape           bool     `yaml:"landscape" json:"landscape"`
	DisplayHeaderFooter bool     `yaml:"display_header_footer" json:"display_header_footer"`
	PrintBackground     bool     `yaml:"print_background" json:"print_background"`
	Scale               *float64 `yaml:"scale" json:"scale"`
	PaperWidth          *float64 `yaml:"paper_width" json:"paper_width"`
	PaperHeight         *float64 `yaml:"paper_height" json:"paper_height"`
	MarginTop           *float64 `yaml:"margin_top" json:"margin_top"`
	MarginRight         *float64 `yaml:"margin_right" json:"margin_right"`
	MarginBottom        *float64 `yaml:"margin_bottom" json:"margin_bottom"`
	MarginLeft          *float64 `yaml:"margin_left" json:"margin_left"`
	PageRanges          string   `yaml:"page_ranges" json:"page_ranges"`
}

// toAction 把 DTO 编译为 GetPDF；未提供（nil）的打印参数不写入，保持浏览器默认。
func (d pdfDTO) toAction() *GetPDF {
	p := &proto.PagePrintToPDF{
		Landscape:           d.Landscape,
		DisplayHeaderFooter: d.DisplayHeaderFooter,
		PrintBackground:     d.PrintBackground,
		PageRanges:          d.PageRanges,
	}
	if d.Scale != nil {
		p.Scale = d.Scale
	}
	if d.PaperWidth != nil {
		p.PaperWidth = d.PaperWidth
	}
	if d.PaperHeight != nil {
		p.PaperHeight = d.PaperHeight
	}
	if d.MarginTop != nil {
		p.MarginTop = d.MarginTop
	}
	if d.MarginRight != nil {
		p.MarginRight = d.MarginRight
	}
	if d.MarginBottom != nil {
		p.MarginBottom = d.MarginBottom
	}
	if d.MarginLeft != nil {
		p.MarginLeft = d.MarginLeft
	}
	return &GetPDF{
		PagePrintToPDF: p,
		Dir:            d.ArtifactDir,
		Name:           d.ArtifactName,
	}
}
