package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"time"

	"github.com/LouYuanbo1/spider-nest/action"
	"github.com/LouYuanbo1/spider-nest/spider"
	"github.com/LouYuanbo1/spider-nest/spider/hijack"
	"github.com/LouYuanbo1/spider-nest/web"
)

func main() {
	ctx := context.Background()

	s, err := spider.NewWeaver(
		spider.WithOperationTimeout(30*time.Second),
		spider.WithPagePoolLimit(2),
		spider.WithHumanize(false),
	)
	if err != nil {
		log.Fatalf("创建蜘蛛失败: %v", err)
	}
	defer s.Close()

	// 程序化捕获：把请求头/请求体交回调自行消费（不依赖日志）。
	capSink := []hijack.RequestCapture{}

	// 两条规则仅在一次"借-用-还"窗口内生效，用 WithHijack 挂起、结束即撤。
	err = s.WithHijack(func(sp spider.Spider) error {
		return sp.BorrowWeb(func(w *web.Web) error {
			if _, err := w.RunAction(ctx, &action.Navigate{
				URL: "data:text/html,<title>hijack-demo</title><body>hijack demo</body>",
			}); err != nil {
				return fmt.Errorf("导航失败: %v", err)
			}

			// 演示 1：真实监听 -> 记录请求头/体后放行，页面拿到 httpbin 的真实回显。
			res, err := w.RunAction(ctx, &action.JavaScript{
				JS: "const r = await fetch('https://httpbin.org/anything?a=1', {method:'POST', body:'hi-from-page'}); return r.text();",
			})
			if err != nil {
				return fmt.Errorf("执行 fetch httpbin 失败: %v", err)
			}
			fmt.Println(">>> [监听+真实放行] 页面从真实服务器拿到的响应透传并读取成功:")
			fmt.Println(res.Value)

			// 演示 2：程序化捕获 -> 请求头/请求体已进 capSink，放行后（mock.local 无真实服务）页面侧失败属预期。
			_, _ = w.RunAction(ctx, &action.JavaScript{
				JS: "try { await fetch('https://mock.local/api/echo?x=1', {method:'POST', body:'secret-body'}); } catch(e) {}",
			})
			fmt.Printf(">>> [程序化捕获] 捕获到的请求共 %d 条:\n", len(capSink))
			for _, c := range capSink {
				fmt.Printf("  Method=%s URL=%s\n", c.Method, c.URL)
				fmt.Printf("  Headers=%v\n", c.Headers)
				fmt.Printf("  Body=%q\n", c.Body)
			}
			return nil
		})
	}, // 回调 fn 在前，规则为可选变参：两条规则仅在本次"借-用-还"窗口内生效
		// 1) 真实监听：命中者记录方法/URL/请求头/请求体后放行到真实网络。
		//    本例指向 httpbin.org（真实的"回声"服务），可同时看到"拦截记录"与"页面仍拿到真实响应"。
		hijack.HijackRule{
			Pattern: "*/anything*",
			Method:  "POST",
			Handler: hijack.MonitorHandler(slog.Default()),
		},
		// 2) 程序化捕获：把请求头/请求体交回调自行消费（不依赖日志）。
		hijack.HijackRule{
			Pattern: "*/api/echo*",
			Method:  "POST",
			Handler: hijack.RequestHandler(func(c *hijack.RequestCapture) {
				capSink = append(capSink, *c)
			}),
		})
	if err != nil {
		log.Fatalf("演示失败: %v", err)
	}

	fmt.Println("hijack 演示完成")
}
