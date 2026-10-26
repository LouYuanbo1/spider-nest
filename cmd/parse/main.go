package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/LouYuanbo1/spider-nest/action"
	"github.com/LouYuanbo1/spider-nest/config"
	"github.com/LouYuanbo1/spider-nest/spider"
)

// main 演示从 YAML 加载 Spider 配置，并从另一份 YAML 解析声明式动作后执行抓取。
// 运行（在项目根目录下）：
//
//	go run ./cmd/demo
func main() {
	cfgPath := flag.String("config", "config/config.yaml", "spider 配置 yaml 路径")
	actionsPath := flag.String("actions", "cmd/parse/actions.yaml", "声明式动作 yaml 路径")
	flag.Parse()

	ctx := context.Background()

	// 1) 解析 Spider 配置（API Key 支持用环境变量 AI_SPIDER_API_KEY 覆盖，避免硬编码）。
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("解析配置失败: %v", err)
	}
	fmt.Printf("已加载配置: headless=%v user_mode=%v page_pool_limit=%d\n",
		cfg.Spider.Headless, cfg.Spider.UserMode, cfg.Spider.PagePoolLimit)

	// 2) 解析声明式动作：action 包内预设的 YAML 前端，复用 ParseActions 的整套校验。
	f, err := os.Open(*actionsPath)
	if err != nil {
		log.Fatalf("打开动作 yaml 失败: %v", err)
	}
	defer f.Close()
	actions, err := action.ParseYAML(f)
	if err != nil {
		log.Fatalf("解析声明式动作失败: %v", err)
	}
	fmt.Printf("从 YAML 解析出 %d 个动作\n", len(actions))

	// 3) 用配置文件拉起蜘蛛并执行动作序列。
	s, err := spider.NewWeaver(spider.WithConfig(cfg.Spider))
	if err != nil {
		log.Fatalf("创建蜘蛛失败: %v", err)
	}
	defer s.Close()

	results, err := s.Crawl(ctx, actions)
	if err != nil {
		log.Fatalf("抓取失败: %v", err)
	}

	// 4) 输出每步的结构化结果。
	for _, r := range results {
		fmt.Printf("[%s] title=%q url=%q effective=%v\n", r.Action, r.Title, r.URL, r.Effective)
		if r.Value != "" {
			fmt.Println("  value:", r.Value)
		}
	}
}
