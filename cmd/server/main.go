// cmd/server 演示如何把爬虫以"服务端"形式暴露出来，通过 JSON 远程调用。
//
// 这里的 main 只做「装配 + 启动」：真正的泛型 Server（包装 nest.Nest 的 HTTP 外观）
// 已抽到独立包 server，本文件构建一只 Portia 蜘蛛的创建工厂、把 Portia 独有的
// Fetch 直连端点注册进路由后，启动 HTTP 服务。
//
// 职责分离：
//   - server 包（泛型核心）：JSON 进出 + 调度，只依赖 spider.Spider 通用接口（Crawl）；
//   - main（具体装配）：提供建蜘蛛工厂、把 Portia 独有的 Fetch 直连端点注册上来。
package main

import (
	"flag"
	"log"
	"log/slog"
	"time"

	"github.com/LouYuanbo1/spider-nest/server"
	"github.com/LouYuanbo1/spider-nest/spider"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP 监听地址")
	size := flag.Int("spiders", 1, "巢内蜘蛛数（并发上限，即同时可以运行的浏览器进程数）")
	flag.Parse()

	if *size <= 0 {
		log.Fatalf("spiders 必须大于 0")
	}

	// 蜘蛛工厂：每只 Portia 都是「一只 Weaver + 聪明直连层」的独立组合，各自持有
	// 自己的浏览器进程。巢在需要时才惰性调用本工厂，用完复用、失效则销毁重建。
	// 因工厂签名是 func() (*spider.Portia, error)，NewServer 的 T/S 会被自动推断为
	// T=spider.Portia、S=*spider.Portia，即实例化为一座 Portia 巢。
	create := func() (*spider.Portia, error) {
		weaver, err := spider.NewWeaver(
			spider.WithOperationTimeout(30*time.Second),
			// 预热与偷头各借一张网（先后、不嵌套），池需 >= 2 才能各占一张、互不拖累。
			spider.WithPagePoolLimit(2),
			spider.WithHumanize(false),
		)
		if err != nil {
			return nil, err
		}
		return spider.NewPortia(weaver,
			spider.WithClientTimeout(15*time.Second),
			spider.WithTransportRetries(2),
			spider.WithRefreshRounds(3),
			// bilibili 类站点需先访问首页由反爬 JS 种下指纹 Cookie，API 直连才不被 403。
			spider.WithWarmup(spider.PreheatNavigate("https://www.bilibili.com/")),
			spider.WithRefreshStatuses(403, 429, 412),
			spider.WithLogger(slog.Default().With("component", "spider-server")),
		)
	}

	srv, err := server.NewServer(*size, create)
	if err != nil {
		log.Fatalf("创建爬虫服务端失败: %v", err)
	}
	defer func() { _ = srv.Close() }()

	// 泛型核心只提供通用端点；把 Portia 独有的直连端点补充注册上去后统一启动。
	mux := srv.Routes()
	mux.HandleFunc("/api/fetch", server.PortiaFetch(srv.Nest()))
	if err := srv.ServeHandler(*addr, mux); err != nil {
		log.Fatalf("HTTP 服务启动失败: %v", err)
	}
}
