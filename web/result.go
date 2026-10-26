package web

import (
	"context"

	"github.com/LouYuanbo1/spider-nest/action"
)

// ActionResult 把单个动作的执行为打包成结构化结果（类型定义见 action.ActionResult）。
// actionName 由调用方给出（正态为用户给 LLM 看到的动作简短名），
// 返回值补充当前页面的标题/URL，以便上层决定「这一步是否推动了我要的页面状态」。
func (w *Web) ActionResult(ctx context.Context, actionName, value, artifactPath string, effective bool) action.ActionResult {
	info, _ := w.Page.Info()
	title, url := "", ""
	if info != nil {
		title, url = info.Title, info.URL
	}
	return action.ActionResult{
		Action:       actionName,
		Title:        title,
		URL:          url,
		Effective:    effective,
		Value:        value,
		ArtifactPath: artifactPath,
	}
}
