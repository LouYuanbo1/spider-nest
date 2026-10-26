package action

import (
	"encoding/json"
	"errors"
	"io"

	"gopkg.in/yaml.v3"
)

// 本文件在 action 包内预设 YAML / JSON 两种格式前端：把声明式动作文档解析为 []Action。
// 这些前端实现 Spec 接口（见 parse.go），复用 ParseActions 的整套解析/校验规则，
// 因此对任何消费方（配置文件、命令入口、测试）都是开箱即用的。

// yamlSpec 以单条 YAML 节点承载动作描述。
type yamlSpec struct {
	node *yaml.Node
}

func (s yamlSpec) Kind() (string, error) {
	for i := 0; i+1 < len(s.node.Content); i += 2 {
		if s.node.Content[i].Value == "type" {
			return s.node.Content[i+1].Value, nil
		}
	}
	// 缺 type：返回空串而非错误，交由 ParseActions 统一给出"缺少必填的 type 字段"。
	return "", nil
}

func (s yamlSpec) Decode(target any) error { return s.node.Decode(target) }

func (s yamlSpec) Line() int {
	if s.node != nil {
		return s.node.Line
	}
	return 0
}

// jsonSpec 以单条 JSON 对象承载动作描述。
type jsonSpec struct {
	raw json.RawMessage
}

func (s jsonSpec) Kind() (string, error) {
	var header struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(s.raw, &header); err != nil {
		return "", err
	}
	return header.Type, nil // 缺 type 时为空串，交由 ParseActions 统一报错
}

func (s jsonSpec) Decode(target any) error { return json.Unmarshal(s.raw, target) }

func (s jsonSpec) Line() int { return 0 }

// errNoActions 表示文档中缺少 actions 序列。
var errNoActions = errors.New("未找到 actions 序列")

// ParseYAML 从流中读取 YAML 动作文档并解析为 []Action。
// 顶层结构形如：
//
//	actions:
//	  - type: navigate
//	    url: https://example.com
//	  - type: get_ax_content
func ParseYAML(r io.Reader) ([]Action, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	doc := root
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		doc = *root.Content[0]
	}

	var specs []Spec
	for i := 0; i+1 < len(doc.Content); i += 2 {
		if doc.Content[i].Value != "actions" {
			continue
		}
		for _, n := range doc.Content[i+1].Content {
			specs = append(specs, yamlSpec{node: n})
		}
	}
	if len(specs) == 0 {
		return nil, errNoActions
	}
	return ParseActions(specs)
}

// ParseJSON 从流中读取 JSON 动作文档并解析为 []Action。
// 顶层结构形如：
//
//	{"actions": [{"type": "navigate", "url": "https://example.com"}, {"type": "get_ax_content"}]}
func ParseJSON(r io.Reader) ([]Action, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var root struct {
		Actions []json.RawMessage `json:"actions"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	if len(root.Actions) == 0 {
		return nil, errNoActions
	}
	specs := make([]Spec, 0, len(root.Actions))
	for _, raw := range root.Actions {
		specs = append(specs, jsonSpec{raw: raw})
	}
	return ParseActions(specs)
}
