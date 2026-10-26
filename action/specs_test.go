package action

import (
	"strings"
	"testing"
)

func TestParseYAML(t *testing.T) {
	doc := `actions:
  - type: navigate
    url: https://example.com
  - type: get_ax_content
  - type: scroll
    scroll_y: 400
`
	acts, err := ParseYAML(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("ParseYAML 失败: %v", err)
	}
	if len(acts) != 3 {
		t.Fatalf("期望 3 个动作，得到 %d", len(acts))
	}
	if _, ok := acts[0].(*Navigate); !ok {
		t.Fatalf("动作 0 期望 Navigate，得到 %T", acts[0])
	}
	if _, ok := acts[1].(*GetAXContent); !ok {
		t.Fatalf("动作 1 期望 GetAXContent，得到 %T", acts[1])
	}
	sc, ok := acts[2].(*Scroll)
	if !ok {
		t.Fatalf("动作 2 期望 Scroll，得到 %T", acts[2])
	} else if sc.ScrollY != 400 {
		t.Fatalf("scroll_y 期望 400，得到 %d", sc.ScrollY)
	}
}

func TestParseYAMLJSArgs(t *testing.T) {
	doc := `actions:
  - type: js
    js: "return document.title"
  - type: js
    js: "return arg1 + arg2"
    args: [1, 2]
`
	acts, err := ParseYAML(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("ParseYAML 失败: %v", err)
	}
	js := acts[1].(*JavaScript)
	if len(js.Args) != 2 {
		t.Fatalf("args 期望 2 个，得到 %d", len(js.Args))
	}
}

func TestParseJSON(t *testing.T) {
	doc := `{
		"actions": [
			{"type": "navigate", "url": "https://example.com"},
			{"type": "get_content", "mode": "structured"}
		]
	}`
	acts, err := ParseJSON(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("ParseJSON 失败: %v", err)
	}
	if len(acts) != 2 {
		t.Fatalf("期望 2 个动作，得到 %d", len(acts))
	}
	if _, ok := acts[0].(*Navigate); !ok {
		t.Fatalf("动作 0 期望 Navigate，得到 %T", acts[0])
	}
	gc, ok := acts[1].(*GetContent)
	if !ok {
		t.Fatalf("动作 1 期望 GetContent，得到 %T", acts[1])
	} else if gc.Mode != ContentModeStructured {
		t.Fatalf("mode 期望 structured，得到 %q", gc.Mode)
	}
}

func TestParseMissingType(t *testing.T) {
	docs := []struct {
		format string
		doc    string
	}{
		{"yaml", "actions:\n  - url: https://example.com\n"},
		{"json", `{"actions":[{"url":"https://example.com"}]}`},
	}
	for _, tc := range docs {
		var (
			acts []Action
			err  error
		)
		if tc.format == "yaml" {
			acts, err = ParseYAML(strings.NewReader(tc.doc))
		} else {
			acts, err = ParseJSON(strings.NewReader(tc.doc))
		}
		if err == nil {
			t.Fatalf("%s 缺少 type 应报错，但没有", tc.format)
		}
		if acts != nil {
			t.Fatalf("%s 出错时不应返回动作", tc.format)
		}
	}
}

func TestParseNoActions(t *testing.T) {
	if _, err := ParseYAML(strings.NewReader("foo: 1\n")); err == nil {
		t.Fatal("缺少 actions 序列应报错，但没有")
	}
	if _, err := ParseJSON(strings.NewReader(`{"foo": 1}`)); err == nil {
		t.Fatal("缺少 actions 序列应报错，但没有")
	}
}
