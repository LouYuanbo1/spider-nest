// 云端节点（agent）入口：在一台云端节点上起一个"本地资源池 + 薄 RPC 面"。
//
// 每个 agent 进程持有的就是一台云节点：内部用本地 Nest（多浏览器池）兜住资源，
// 对外经 net/rpc+gob 暴露借/跑/抓放检查点/还/续期等操作。会话侧（cmd/server 以
// -agents 指向本端点）把动作"拍"过来，节点本地就地执行——这就是 Route B 的节点侧。
//
// 启动示例：
//
//	go run ./cmd/agent -addr :9001 -config config/config.yaml
package main

import (
	"flag"
	"log"
	"net"

	"github.com/LouYuanbo1/spider-nest/cloud/agent"
	"github.com/LouYuanbo1/spider-nest/config"
	"github.com/LouYuanbo1/spider-nest/provider"
	"github.com/LouYuanbo1/spider-nest/spider"
)

func main() {
	configPath := flag.String("config", "config/config.yaml", "配置文件路径")
	addr := flag.String("addr", ":9001", "本节点 RPC 监听地址")
	flag.Parse()

	appcfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("解析配置失败: %v", err)
	}

	nestConfig := config.NestConfig{SpiderPoolLimit: 1}
	if appcfg.Nest != nil && appcfg.Nest.SpiderPoolLimit > 0 {
		nestConfig.SpiderPoolLimit = appcfg.Nest.SpiderPoolLimit
	}

	// 节点内部用本地 Nest 池兜住浏览器资源：一次借贷即从池里挑一只蜘蛛借一张活页。
	res, err := provider.NewNestFromConfig(nestConfig, func() (*spider.Weaver, error) {
		s, err := spider.NewWeaver(spider.WithConfig(appcfg.Spider))
		if err != nil {
			return nil, err
		}
		return s, nil
	})
	if err != nil {
		log.Fatalf("创建节点资源提供器失败: %v", err)
	}

	node := agent.New(res, agent.Config{})
	defer node.Close()

	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("监听 %s 失败: %v", *addr, err)
	}
	log.Printf("云端节点 listening on %s", lis.Addr())
	if err := node.Serve(lis); err != nil {
		log.Fatalf("RPC 服务退出: %v", err)
	}
}
