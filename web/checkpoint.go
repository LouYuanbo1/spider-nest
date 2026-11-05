package web

import (
	"context"
	"fmt"

	"github.com/LouYuanbo1/spider-nest/action"
	"github.com/go-rod/rod/lib/proto"
)

// Cookie 是检查点中可序列化的一份 cookie，镜像 proto.NetworkCookie 的持久化字段。
// SameSite 为字符串枚举类型（proto.NetworkCookieSameSite），故可被 JSON 直接序列化，
// 用于跨进程 / 落盘保存与恢复。
type Cookie struct {
	Name     string                      `json:"name"`
	Value    string                      `json:"value"`
	Domain   string                      `json:"domain,omitempty"`
	Path     string                      `json:"path,omitempty"`
	Secure   bool                        `json:"secure,omitempty"`
	HTTPOnly bool                        `json:"httpOnly,omitempty"`
	SameSite proto.NetworkCookieSameSite `json:"sameSite,omitempty"`
	Expires  float64                     `json:"expires,omitempty"`
}

// Checkpoint 冷恢复所需的可序列化会话状态：当前 URL + cookies + 可选的恢复脚本。
// 语义是"会话该在哪一页、以什么身份续上"。用于一张网被归还（进入冷态）后，
// 下层重新物化成一片新页时重放——把会话从"只占一个身份"升级回"占一片活页"。
//
// 设计取舍：rod 的 rod.Page 是 CDP 端点、无法直接序列化，因此检查点只能表达
// "可否廉价重建"的状态（url + cookies），而无法冻结任意 JS/DOM 中间态。这也正
// 是"冷态节省资源、但重水化有损"这一取舍的明证。
type Checkpoint struct {
	URL      string   `json:"url"`
	Cookies  []Cookie `json:"cookies,omitempty"`
	ResumeJS string   `json:"resume_js,omitempty"` // 可选：重水化后额外执行的恢复脚本
}

// CaptureCheckpoint 从网所持页面捕获当前可恢复状态（URL + cookies）。
// 供会话管理器在"热→冷"提交时调用；cookie 读取失败时降级为仅保留 URL。
func (w *Web) CaptureCheckpoint(ctx context.Context) (*Checkpoint, error) {
	if w == nil || w.page == nil {
		return nil, fmt.Errorf("web: 无可捕获状态的页")
	}
	info, err := w.page.Info()
	if err != nil {
		// 页面已失效：返回空检查点并上报，由调用方决定是否放弃冷态。
		return &Checkpoint{}, err
	}
	cp := &Checkpoint{URL: info.URL}

	cookies, err := w.page.Cookies(nil)
	if err != nil {
		return cp, nil // cookie 读取失败降级：仍保留 URL
	}
	for _, c := range cookies {
		cp.Cookies = append(cp.Cookies, Cookie{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     c.Path,
			Secure:   c.Secure,
			HTTPOnly: c.HTTPOnly,
			SameSite: c.SameSite,
			Expires:  float64(c.Expires),
		})
	}
	return cp, nil
}

// RestoreCheckpoint 在一片（重新物化得到的）新页上重放检查点：先种 cookies，
// 再导航到目标 URL，可选执行 ResumeJS。用于会话从冷态重新物化(hydrate)。
func (w *Web) RestoreCheckpoint(ctx context.Context, cp *Checkpoint) error {
	if w == nil || w.page == nil {
		return fmt.Errorf("web: 无可重放的页")
	}
	if cp == nil {
		return fmt.Errorf("web: 检查点为 nil")
	}

	if len(cp.Cookies) > 0 {
		params := make([]*proto.NetworkCookieParam, 0, len(cp.Cookies))
		for _, c := range cp.Cookies {
			params = append(params, &proto.NetworkCookieParam{
				Name:     c.Name,
				Value:    c.Value,
				Domain:   c.Domain,
				Path:     c.Path,
				Secure:   c.Secure,
				HTTPOnly: c.HTTPOnly,
				SameSite: c.SameSite,
				Expires:  proto.TimeSinceEpoch(c.Expires),
			})
		}
		if err := w.page.SetCookies(params); err != nil {
			return fmt.Errorf("web: 重放 cookie 失败: %w", err)
		}
	}

	if cp.URL != "" {
		if err := w.Navigate(ctx, &action.Navigate{URL: cp.URL}); err != nil {
			return fmt.Errorf("web: 重放导航失败: %w", err)
		}
	}

	if cp.ResumeJS != "" {
		if _, err := w.DoJavaScript(ctx, &action.JavaScript{JS: cp.ResumeJS}); err != nil {
			return fmt.Errorf("web: 重放恢复脚本失败: %w", err)
		}
	}
	return nil
}
