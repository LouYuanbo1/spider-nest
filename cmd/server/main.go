// server 是一款通过 HTTP 暴露 spider-nest MCP 浏览器工具的服务入口。
//
// 采用"无状态核心 + 显式状态句柄"模型：每个用户（对话）通过 open_web_tool 借一张独立的网，
// 拿到 web_id 句柄后，全部浏览器动作携带同一 web_id 路由到该网，结束由 close_web_tool 归还。
// 传输层无状态、会话持有活页，适合个人/单机使用。
//
// 启动后监听 http://127.0.0.1:8080，任何支持 MCP Streamable HTTP 的客户端都能连接。
package main

import (
	"flag"
	"log"

	"github.com/LouYuanbo1/spider-nest/config"
	"github.com/LouYuanbo1/spider-nest/provider"
	"github.com/LouYuanbo1/spider-nest/server"
	"github.com/LouYuanbo1/spider-nest/session"
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

	// 以 Nest(多浏览器池)为资源来源的会话管理器：
	// 并发上限=池内浏览器数,一用户一会话即独占一个浏览器实例；会话持有活页,
	// 随 Open/Close 借还,进程退出经 Shutdown 兜底归还。
	nestConfig := config.NestConfig{SpiderPoolLimit: 1}
	if appcfg.Nest != nil && appcfg.Nest.SpiderPoolLimit > 0 {
		nestConfig.SpiderPoolLimit = appcfg.Nest.SpiderPoolLimit
	}
	res, err := provider.NewNestFromConfig(nestConfig, func() (*spider.Weaver, error) {
		s, err := spider.NewWeaver(spider.WithConfig(appcfg.Spider))
		if err != nil {
			return nil, err
		}
		return s, nil
	})
	if err != nil {
		log.Fatalf("创建本地资源提供器失败: %v", err)
	}
	broker, err := session.New(res, appcfg.Spider.OperationTimeout)
	if err != nil {
		log.Fatalf("创建会话管理器失败: %v", err)
	}
	defer broker.Shutdown()

	impl := &mcp.Implementation{Name: "spider-nest", Version: "v1.0.0"}
	if err := server.RunHTTPServer(broker, *addr, impl, nil); err != nil {
		log.Fatalf("HTTP 服务退出: %v", err)
	}
}
