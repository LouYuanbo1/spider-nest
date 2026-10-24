package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/LouYuanbo1/spider-nest/config"
	"github.com/LouYuanbo1/spider-nest/nest"
	"github.com/LouYuanbo1/spider-nest/spider"
	"github.com/LouYuanbo1/spider-nest/web"
)

// dsl/demo：显式借用 DSL 简化版走查。
// 用 Nest 建巢，借一只蜘蛛，声明一套"蜘蛛级"监听，然后用 Go() 自动借网执行：
//   - 页面（网）的借与还不再手动控制——每次 Go() 自动借一张网 → 跑动作 → 归还。
//   - 监听归属蜘蛛，而非单张页面——借期内声明一次，多次 Go() 都落在同一套监听下，
//     直到 Release() 才撤。
//
// 演示：第一、二次 Go 各自发一条会被 echo 截获的 POST（参数 k 不同），
// 证明同一套蜘蛛级监听跨多次 Go 持续生效；最后 Release 归还蜘蛛。

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := &config.SpiderConfig{
		OperationTimeout: 30 * time.Second,
		PagePoolLimit:    2,
		Humanize:         false,
	}

	n, err := nest.NewNest(1, func() (*spider.Weaver, error) { // 并发上限 1，够演示；配置由 create 闭包注入
		return spider.NewWeaver(cfg)
	})
	if err != nil {
		log.Fatalf("建巢失败: %v", err)
	}
	defer func() { _ = n.Close() }()

	sp := n.AcquireSpider()
	// 监听属于蜘蛛：写一次，之后所有 Go() 自动复用。
	sniff := func(k int) []web.Action {
		// 脚本被包成 async 函数且 Eval 会 awaitPromise：用 await 等 fetch 落地再读回标题。
		js := "const r=await fetch('https://mock.local/api/sniff?k=%d',{method:'POST',body:'hello'});" +
			"const t=await r.text();document.title=t;return document.title"
		return []web.Action{
			&web.JavaScript{JS: fmt.Sprintf(js, k)},
		}
	}
	err = sp.WithContext(ctx).
		Hijack( // 蜘蛛级监听：借用期常驻
			config.HijackConfig{Name: "sniff", Pattern: "*/api/sniff*", Method: "POST", Handler: config.HijackHandlerEcho},
		).
		Go( // Go 1：自动借网 → 导航 + sniff(1) + 读回标题 → 自动还网
			&web.Navigate{URL: "data:text/html,<title>net-1</title><body>first web</body>"},
		).
		Go(sniff(1)...).
		Go(sniff(2)...).
		Release()

	fmt.Println("==== DSL 简化版演示结果 ====")
	fmt.Printf("err=%v\n", err)
	for gi, grp := range sp.Results() { // 每次 Go 一组
		for i, r := range grp {
			v := fmt.Sprintf("%v", r.Value)
			if len(v) > 100 {
				v = v[:100] + "...(截断)"
			}
			fmt.Printf("  Go[%d] action[%d] Value=%q\n", gi, i, v)
		}
	}
}
