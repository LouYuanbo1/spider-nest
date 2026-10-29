package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"time"

	"github.com/LouYuanbo1/spider-nest/spider"
)

func main() {
	// 默认用 `nav`：在演示层面机制最稳的接口——能稳定走通"偷头 -> 直连"拿到 HTTP 200 +
	// 合法 JSON。注意它面向登录账号，未登录会返回 code:-101(账号未登录)，但足以验证链路。
	// popular 反爬时序严、易在偷头环节卡住，不设默认。
	url := flag.String("url", "https://api.bilibili.com/x/web-interface/nav", "要直连抓取的目标 URL")
	flag.Parse()

	ctx := context.Background()

	// 1) 一只基础织网蜘蛛：这里是"头制造机"，Portia 借它临时监听、偷一套浏览器
	//    真实发出的请求头（含 cookie/反爬签名等），供直连复用。
	weaver, err := spider.NewWeaver(
		spider.WithOperationTimeout(30*time.Second),
		// 预热与偷头各借一张网（先后、不嵌套），池需 >= 2 才能各占一张、互不拖累。
		spider.WithPagePoolLimit(2),
		spider.WithHumanize(false),
	)
	if err != nil {
		log.Fatalf("创建蜘蛛失败: %v", err)
	}

	// 2) 聪明的直连蜘蛛（核心状态机）：包装基础蜘蛛，叠加"偷头 -> 纯 HTTP 直连"。
	//    - 优先用缓存里"最后一次有效"的请求头走纯 HTTP 直连（快、不占浏览器）；
	//    - 失败或命中刷新状态码（默认 403/429，本演示追加 412）时，借内部基础蜘蛛偷新头再直连重试；
	//    - 最多允许 3 轮"偷头+重连"。412 是 bilibili 的典型风控拦截码，一并纳入刷新。
	client, err := spider.NewPortia(weaver,
		spider.WithClientTimeout(15*time.Second),
		spider.WithTransportRetries(2),
		spider.WithRefreshRounds(3),
		// bilibili 需要先访问站点由反爬 JS 种下 buvid 指纹 Cookie，API 直连才不被 403。
		// 预热由注入的策略执行一次，成功后偷到的头就带上了这些 Cookie。
		spider.WithWarmup(spider.PreheatNavigate("https://www.bilibili.com/")),
		spider.WithRefreshStatuses(403, 429, 412),
		spider.WithLogger(slog.Default().With("component", "portia-demo")),
	)
	if err != nil {
		log.Fatalf("创建 Portia 失败: %v", err)
	}
	defer func() { _ = client.Close() }()

	// 3) 抓取：第一次大概率走"偷头 -> 直连"完整链路，之后命中缓存走纯直连。
	//    注意：偷头阶段浏览器页面保持空白是正常的——只在后台 fetch 真实请求来抄头，不导航。
	target := spider.Target{URL: *url}
	resp, err := client.Fetch(ctx, target)
	if err != nil {
		log.Fatalf("直连抓取失败: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatalf("读取响应体失败: %v", err)
	}

	fmt.Printf(">>> 首次 Fetch: 状态码=%d 长度=%d\n", resp.StatusCode, len(body))
	fmt.Printf(">>> 响应体(截断 300 字):\n%s\n", truncate(string(body), 300))

	// 4) 再次 Fetch 复用缓存头走纯直连，应明显更快、不再借浏览器偷头。
	again, err := client.Fetch(ctx, target)
	if err != nil {
		log.Printf("再次直连失败（预期外）: %v", err)
	} else {
		fmt.Printf(">>> 二次 Fetch(命中缓存): 状态码=%d\n", again.StatusCode)
		_ = again.Body.Close()
	}

	// 5) Fresh 强制偷新头后直连，无视缓存。
	fresh, err := client.Fresh(ctx, target)
	if err != nil {
		log.Printf("Fresh 直连失败: %v", err)
	} else {
		fmt.Printf(">>> Fresh(强制重新偷头): 状态码=%d\n", fresh.StatusCode)
		_ = fresh.Body.Close()
	}

	fmt.Println("direct 演示完成")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(截断)"
}
