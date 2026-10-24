package direct

import (
	"context"

	"github.com/LouYuanbo1/spider-nest/spider/hijack"
)

// HeaderSource 请求头的来源抽象："从浏览器这头弄到一套新鲜请求头"的提供方。
// 每次 Capture 都应触发一次真实请求，以便拿到此刻仍有效的 cookie / 反爬签名等，
// 再交给直连复用。实现通常借助 rod 劫持（hijack）监听并捕获匹配请求的请求头快照。
type HeaderSource interface {
	// Capture 触发一次真实请求，捕获并返回匹配请求的请求头快照。
	Capture(ctx context.Context, t Target) (*hijack.RequestCapture, error)
}
