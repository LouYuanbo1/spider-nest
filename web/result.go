package web

import (
	"context"
)

// ActionResult 单个动作在指定页面上执行后的结构化结果。
// Effective 统一表示"动作执行成功，且预期结果可用"：
//   - navigate：成功导航并完成加载 => 生效
//   - input*：读回输入框当前值确认已包含目标文本 => 生效（否则 false）
//   - click* / scroll：无异常完成 => 生效
//   - js：执行成功且返回值非空、非 "undefined" => 生效
//   - to_html：成功取回 HTML => 生效
//   - to_pdf：成功落盘（产物路径见 ArtifactPath）=> 生效
//
// Value：动作返回的"内容"，如 HTML、JS 执行结果；
// ArtifactPath：落盘产物的文件路径（如 PDF）。与 Value 分离，
// 避免把"文件位置"和"返回内容"混在一个字段里。
type ActionResult struct {
	Action       string `yaml:"action" json:"action"`
	Title        string `yaml:"title" json:"title"`
	URL          string `yaml:"url" json:"url"`
	Value        string `yaml:"value" json:"value"`               // 动作返回的主要内容：HTML/JS 执行结果（可能为空）
	ArtifactPath string `yaml:"artifactPath" json:"artifactPath"` // 落盘产物路径（如 PDF），为空表示无产物
	Effective    bool   `yaml:"effective" json:"effective"`
}

func (w *Web) ActionResult(ctx context.Context, action, value, artifactPath string, effective bool) ActionResult {
	//page := w.Page.Context(ctx)
	info, _ := w.Page.Info()
	title, url := "", ""
	if info != nil {
		title, url = info.Title, info.URL
	}
	return ActionResult{
		Action:       action,
		Title:        title,
		URL:          url,
		Effective:    effective,
		Value:        value,
		ArtifactPath: artifactPath,
	}
}
