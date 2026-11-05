package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/LouYuanbo1/spider-nest/agent"
	"github.com/LouYuanbo1/spider-nest/agent/mnemo/fs"
	"github.com/LouYuanbo1/spider-nest/config"
	"github.com/LouYuanbo1/spider-nest/server"
	"github.com/LouYuanbo1/spider-nest/spider"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/cloudwego/eino-ext/components/model/deepseek"
	"github.com/cloudwego/eino/schema"
)

// newSessionID 生成按时间排序的随机会话 id。
// 选用 YYYYMMDDHHMMSS 时间前缀（保证同一天内按创建时间可排序），
// 再拼接随机后缀，避免同一时刻创建多个会话时 id 冲突。
func newSessionID() string {
	return fmt.Sprintf("%s-%04x", time.Now().Format("20060102150405"), rand.Intn(0x10000))
}

// userDataDir 是统一的本地数据根目录，存放会话记录与浏览器用户数据。
//
//	user_data/
//	├── sessions/        # 会话历史（jsonl，由 mnemo/fs 管理）
//	└── browser/         # Chrome 用户数据（Cookies/登录态，已被 .gitignore 忽略）
const userDataDir = "user_data"

func main() {
	appcfg, err := config.Load("config/config.yaml")
	if err != nil {
		log.Fatalf("解析配置失败: %v", err)
	}
	ctx := context.Background()

	chatModel, err := deepseek.NewChatModel(ctx, &deepseek.ChatModelConfig{
		APIKey: appcfg.Model.APIKey,
		Model:  "deepseek-flash",
	})
	if err != nil {
		fmt.Printf("Error creating chat model: %v", err)
		return
	}

	// 内嵌一套本地 MCP 服务：agent 通过 HTTP 调用它，再由它驱动真实浏览器。
	// 无需预先手动启动独立 server 进程，一条命令即可跑通整条链路。
	nestConfig := config.NestConfig{SpiderPoolLimit: 1}
	if appcfg.Nest != nil && appcfg.Nest.SpiderPoolLimit > 0 {
		nestConfig.SpiderPoolLimit = appcfg.Nest.SpiderPoolLimit
	}

	// 统一"显式状态句柄"模型：agent 侧 MCP 客户端首次动作前自动借一张网并缓存 web_id，
	// 之后所有浏览器操作都携带同一 web_id。底层为 Nest（多浏览器池），一用户一网独占
	// 一个浏览器实例，会话结束 / 进程退出时由 manager 兜底归还。
	mgr, err := server.NewWebSessionManager(
		nestConfig,
		func() (*spider.Weaver, error) {
			s, err := spider.NewWeaver(spider.WithConfig(appcfg.Spider))
			if err != nil {
				return nil, err
			}
			return s.(*spider.Weaver), nil
		},
		appcfg.Spider.OperationTimeout,
		60*time.Second, // hotTTL：热页空闲多久后冷提交
		10*time.Minute, // idleTTL：冷态会话无使用多久后彻底回收
	)
	if err != nil {
		fmt.Printf("Error creating session manager: %v", err)
		return
	}
	defer mgr.Shutdown()

	impl := &mcp.Implementation{Name: "spider-nest", Version: "v1.0.0"}
	mcpBaseURL, closeLocal, err := server.Listen(mgr, impl, nil)
	if err != nil {
		fmt.Printf("Error starting local MCP server: %v", err)
		return
	}
	defer closeLocal()

	modelAgent, err := agent.NewDefaultSpiderAgent(ctx, chatModel, mcpBaseURL)
	if err != nil {
		fmt.Printf("Error creating spider agent: %v", err)
		return
	}

	// 开启持久化记忆：会话历史保存到统一本地数据目录下的 sessions 子目录
	memStore, err := fs.NewStore[*schema.Message](filepath.Join(userDataDir, "sessions"))
	if err != nil {
		fmt.Printf("Error creating memory store: %v", err)
		return
	}
	modelAgent.SetMemoryStore(memStore)

	// 终端多轮对话模式：for 循环读取用户输入，逐轮调用 agent。
	// 默认开启一个新会话，历史记忆自动持久化并回填。
	sessionID := newSessionID()
	fmt.Printf("[已创建新会话 %s]\n", sessionID)
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("\n>>> 请输入指令 (输入 n/new 新建对话, exit/quit/q 退出): ")
		line, err := reader.ReadString('\n')
		if err != nil {
			// EOF (Ctrl+D / Ctrl+Z) 也视为退出
			fmt.Println("\n[输入结束，退出]")
			break
		}

		input := strings.TrimSpace(line)
		if input == "" {
			continue
		}
		switch strings.ToLower(input) {
		case "exit", "quit", "q":
			fmt.Println("[退出]")
			return
		case "n", "new":
			// 新建一个按时间排序的随机会话 id，切换到全新上下文
			sessionID = newSessionID()
			fmt.Printf("\n[已新建会话 %s]\n", sessionID)
			continue
		}

		if err := modelAgent.OutputMessage(ctx, sessionID, input, func(s string) {
			fmt.Print(s)
		}); err != nil {
			fmt.Printf("\n[执行出错: %v]\n", err)
		}
		fmt.Println()
	}
}
