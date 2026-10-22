package web

import (
	"context"
	"fmt"
	"time"

	"github.com/go-rod/rod/lib/proto"
)

// DoJavaScript 在网持有的页面上执行JS并返回序列化结果
func (w *Web) DoJavaScript(ctx context.Context, action *JavaScript) (string, error) {
	page := w.Page.Context(ctx)
	// 无参时用 async 函数表达式包裹（而非立即执行的 IIFE）：
	// rod 的 Eval 会以 .apply(this, arguments) 调用该函数表达式并 awaitPromise，
	// 若写成 IIFE 则会得到一个 Promise，再被 .apply 调用时报 "apply is not a function"。
	var source string
	if len(action.Args) > 0 {
		source = action.JS
	} else {
		source = "async () => { " + action.JS + "\n}"
	}

	var result *proto.RuntimeRemoteObject
	var err error
	// 模型生成的 JS 在 SPA 页面（如 B 站）上偶发失败，最多重试一次，
	// 避免一次偶发异常就中断整次抓取。
	for attempt := range 2 {
		result, err = page.Eval(source, action.Args...)
		if err == nil {
			break
		}
		fmt.Println("JS执行失败，重试中", "js", action.JS, "err", err)
		if attempt == 0 {
			time.Sleep(300 * time.Millisecond)
		}
	}
	if err != nil {
		fmt.Println("JS执行失败", "js", action.JS, "err", err)
		return "", fmt.Errorf("JS执行失败: %v", err)
	}

	// 脚本未 return / 返回 undefined 时给一个明确结果，而不是后续序列化崩溃。
	if result == nil || result.Type == proto.RuntimeRemoteObjectTypeUndefined || result.Value.Nil() {
		return "undefined", nil
	}

	data, err := result.Value.MarshalJSON()
	if err != nil {
		return "", fmt.Errorf("JSON序列化失败: %v", err)
	}

	return string(data), nil
}
