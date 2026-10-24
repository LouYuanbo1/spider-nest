package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"time"

	"github.com/LouYuanbo1/spider-nest/config"
	"github.com/LouYuanbo1/spider-nest/spider"
	"github.com/LouYuanbo1/spider-nest/spider/direct"
)

func main() {
	url := flag.String("url", "https://httpbin.org/anything?from=direct", "要直连抓取的目标 URL")
	flag.Parse()

	ctx := context.Background()

	// 1) 一只基础织网蜘蛛：这里是"头制造机"，SpiderHeaderSource 借它临时监听，
	//    偷一套浏览器真实发出的请求头（含 cookie/反爬签名等），供直连复用。
	cfg := &config.SpiderConfig{
		OperationTimeout: 30 * time.Second,
		PagePoolLimit:    1,
		Humanize:         false,
	}
	weaver, err := spider.NewWeaver(cfg)
	if err != nil {
		log.Fatalf("创建蜘蛛失败: %v", err)
	}
	defer func() { _ = weaver.Close() }()

	// 2) 纯 HTTP 直连引擎：负责把偷来的请求头快照翻译成真实请求发出。
	//    装配超时与传输层重试，让直连更稳。
	fetcher := direct.NewFetcher(
		direct.WithTimeout(15*time.Second),
		direct.WithRetries(2),
	)

	// 3) 请求头来源：基于蜘蛛实现 HeaderSource 接口，触发真实请求偷头。
	headerSource := direct.NewSpiderHeaderSource(weaver)

	// 4) 聪明的直连蜘蛛（核心状态机）：
	//    - 优先用缓存里"最后一次有效"的请求头走纯 HTTP 直连（快、不占浏览器）；
	//    - 失败或命中刷新状态码（默认 403/429）时，借 HeaderSource 偷新头再直连重试；
	//    - 最多允许 3 轮"偷头+重连"。
	client := direct.New(
		fetcher,
		headerSource,
		direct.WithMaxRetries(3),
		direct.WithRefreshStatuses(403, 429),
		direct.WithLogger(slog.Default().With("component", "direct-demo")),
	)

	// 5) 抓取：第一次大概率走"偷头 -> 直连"完整链路，之后命中缓存走纯直连。
	target := direct.Target{URL: *url}
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

	// 6) 再次 Fetch 复用缓存头走纯直连，应明显更快、不再借浏览器偷头。
	again, err := client.Fetch(ctx, target)
	if err != nil {
		log.Printf("再次直连失败（预期外）: %v", err)
	} else {
		fmt.Printf(">>> 二次 Fetch(命中缓存): 状态码=%d\n", again.StatusCode)
		_ = again.Body.Close()
	}

	// 7) Fresh 强制偷新头后直连，无视缓存。
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
