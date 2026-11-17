// 演示：单只蜘蛛（Weaver）并发监听多张网页，且 **不使用** Spider 的浏览器级
// 路由（WithHijack / bgrouter），而是逐网页走 page 级劫持（WithHijack）。
//
// 对应你之前观察的现象——"只有聚焦页面能监听到、后台页面失效"：根因是 Chrome
// 对后台标签页的 Timer Throttling / Page Freezing。解决办法不是 Rod 的劫持逻辑，
// 而是用启动器那些"禁用后台节流/冻结"的开关把它们关掉。本示例用 WithConfig
// 一次把 4 个开关都打开，再用并发 goroutine 同时驱动多张网，每张网各自挂
// page 级 Fetch 域监听。
//
// 这里监听的是真实站点的真实 API：导航到站点主页后滚动触发后端接口，
// MonitorHandler 记录并真实放行，通过日志的 site 字段即可确认对应 API 被监听到。
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"time"

	"github.com/LouYuanbo1/spider-nest/action"
	"github.com/LouYuanbo1/spider-nest/config"
	"github.com/LouYuanbo1/spider-nest/hijack"
	"github.com/LouYuanbo1/spider-nest/spider"
	"github.com/LouYuanbo1/spider-nest/web"
)

// site 描述一个真实站点：主页 URL + 要监听的真实 API 请求 pattern。
type site struct {
	name string
	url  string
	api  string
}

func main() {
	ctx := context.Background()

	// 三个真实站点的首页 API（聚合列表 / 推荐接口）。
	sites := []site{
		{name: "cnblogs", url: "https://www.cnblogs.com/", api: "https://www.cnblogs.com/AggSite/AggSitePostList*"},
		//{name: "bili", url: "https://www.bilibili.com/", api: "https://api.bilibili.com/x/web-interface/index/ogv/rcmd*"},
		{name: "csdn", url: "https://www.csdn.net/", api: "https://cms-api.csdn.net/v1/web_home/select_content*"},
	}

	// 关键：把"后台节流/冻结"的开关全部打开。否则未聚焦标签页的定时器被降频、
	// 渲染进程被后台化甚至冻结，JS 里的真实请求根本发不出去，page 级劫持自然无货可劫。
	s, err := spider.NewWeaver(
		spider.WithConfig(&config.SpiderConfig{
			PagePoolLimit:                       len(sites),
			Humanize:                            true,
			DisableBackgroundTimerThrottling:    true,
			DisableBackgroundingOccludedWindows: true,
			DisableRendererBackgrounding:        true,
			OperationTimeout:                    2 * time.Minute,
		}),
	)
	if err != nil {
		log.Fatalf("创建蜘蛛失败: %v", err)
	}
	defer s.Close()

	done := make(chan struct{}, len(sites))
	for i := range sites {
		go func(idx int) {
			defer func() { done <- struct{}{} }()
			runPage(ctx, s, sites[idx])
		}(i)
	}
	for range sites {
		<-done
	}

	fmt.Println("\n>>> 并发真实 API 监听演示完成")
}

// runPage 借用一张网，导航到真实站点主页，并用 WithHijack 在 fn 作用域内挂一套
// page 级 Fetch 域监听——监听窗承载 fn 内任意多轮动作，而不像每调一次就重建 router。
// 滚动触发后端加载，真实 API 请求被 MonitorHandler 记录后放行。
func runPage(ctx context.Context, s spider.Spider, st site) {
	w, err := s.AcquireCtx(ctx)
	if err != nil {
		log.Printf("[%s] 取网失败: %v", st.name, err)
		return
	}
	defer s.Release(w)

	err = w.WithHijack(func(web.Web) error {
		// 导航到真实站点主页。
		if _, err := w.RunAction(ctx, &action.Navigate{URL: st.url}); err != nil {
			return err
		}
		// 滚动几次触发后端接口加载，让真实 API 实际发出。
		for range 5 {
			if _, err := w.RunAction(ctx, &action.Scroll{ScrollY: 1200}); err != nil {
				return err
			}
		}
		return nil
	}, // page 级规则：命中该站真实 API 就"真实监听"（记录后放行）。
		hijack.HijackRule{
			Pattern: st.api,
			// 真实 API 都用 GET 拉取列表，命中即记录完整请求。
			Handler: hijack.MonitorHandler(
				slog.Default().With(slog.String("site", st.name)),
			),
		},
	)
	if err != nil {
		log.Printf("[%s] WithHijack 失败: %v", st.name, err)
	}
}
