// server 是一款通过 HTTP 暴露 spider-nest MCP 浏览器工具的服务入口。
//
// 采用"无状态核心 + 显式状态句柄"模型：每个用户（对话）通过 open_web_tool 借一张独立的网，
// 拿到 web_id 句柄后，全部浏览器动作携带同一 web_id 路由到该网，结束由 close_web_tool 归还、
// 空闲超时兜底回收。传输层无状态，服务可水平扩展。
//
// 启动后监听 http://127.0.0.1:8080，任何支持 MCP Streamable HTTP 的客户端都能连接。
package main

import (
	"flag"
	"log"
	"time"

	"github.com/LouYuanbo1/spider-nest/config"
	"github.com/LouYuanbo1/spider-nest/server"
	"github.com/LouYuanbo1/spider-nest/spider"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	configPath := flag.String("config", "config/config.yaml", "配置文件路径")
	addr := flag.String("addr", ":8080", "HTTP 监听地址")
	flag.Parse()

	appcfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("解析配置失败: %v", err)
	}

	spiderInstance, err := spider.NewWeaver(spider.WithConfig(appcfg.Spider))
	if err != nil {
		log.Fatalf("创建爬虫失败: %v", err)
	}
	defer spiderInstance.Close()

	// 以"网(web)"为基本单位的显式句柄 broker：一个用户借一张网，用完归还，空闲超时兜底回收。
	broker := server.NewWebBroker(spiderInstance, appcfg.Spider.OperationTimeout, 10*time.Minute)
	defer broker.Shutdown()

	impl := &mcp.Implementation{Name: "spider-nest", Version: "v1.0.0"}
	if err := server.RunBrokerHTTPServer(broker, *addr, impl, nil); err != nil {
		log.Fatalf("HTTP 服务退出: %v", err)
	}
}
