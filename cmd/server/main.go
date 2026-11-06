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

	nestConfig := config.NestConfig{SpiderPoolLimit: 1}
	if appcfg.Nest != nil && appcfg.Nest.SpiderPoolLimit > 0 {
		nestConfig.SpiderPoolLimit = appcfg.Nest.SpiderPoolLimit
	}

	// 以 Nest（多浏览器池）为资源来源的冷热两态会话管理器：
	// 并发上限 = 池内浏览器数，一用户一会话即独占一个浏览器实例；活跃操作用热页，
	// 空闲超 hotTTL 自动冷提交（归还浏览器、留检查点），再次用到时按检查点重水化，
	// 从而在"LLM 思考/idle"期间不占浏览器、抬高并发密度；用完归还、冷态超时兜底回收。
	res, err := provider.NewNestFromConfig(nestConfig, func() (*spider.Weaver, error) {
		s, err := spider.NewWeaver(spider.WithConfig(appcfg.Spider))
		if err != nil {
			return nil, err
		}
		return s, nil
	})
	if err != nil {
		log.Fatalf("创建资源提供器失败: %v", err)
	}
	broker, err := session.New(
		res,
		appcfg.Spider.OperationTimeout,
		60*time.Second, // hotTTL：热页空闲多久后冷提交
		10*time.Minute, // idleTTL：冷态会话无使用多久后彻底回收
	)
	if err != nil {
		log.Fatalf("创建会话管理器失败: %v", err)
	}
	defer broker.Shutdown()

	impl := &mcp.Implementation{Name: "spider-nest", Version: "v1.0.0"}
	if err := server.RunHTTPServer(broker, *addr, impl, nil); err != nil {
		log.Fatalf("HTTP 服务退出: %v", err)
	}
}
