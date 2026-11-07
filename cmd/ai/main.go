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
	"github.com/LouYuanbo1/spider-nest/provider"
	"github.com/LouYuanbo1/spider-nest/server"
	"github.com/LouYuanbo1/spider-nest/session"
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
	res, err := provider.NewNestFromConfig(nestConfig, func() (*spider.Weaver, error) {
		s, err := spider.NewWeaver(spider.WithConfig(appcfg.Spider))
		if err != nil {
			return nil, err
		}
		return s, nil
	})
	if err != nil {
		fmt.Printf("Error creating resource provider: %v", err)
		return
	}
	mgr, err := session.New(
		res,
		appcfg.Spider.OperationTimeout,
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
	// 每个会话持有自己的一张网；切到新会话 / 退出时调用 CloseSession 归还，由会话生命周期
	// 决定 web 的归还时机，而不是交给模型显式 close（模型忘记归还会长期占着池资源）。
	sessionID := newSessionID()
	fmt.Printf("[已创建新会话 %s]\n", sessionID)
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("\n>>> 请输入指令 (输入 n/new 新建对话, exit/quit/q 退出): ")
		line, err := reader.ReadString('\n')
		if err != nil {
			// EOF (Ctrl+D / Ctrl+Z) 也视为退出
			fmt.Println("\n[输入结束，退出]")
			modelAgent.CloseSession(sessionID)
			break
		}

		input := strings.TrimSpace(line)
		if input == "" {
			continue
		}
		switch strings.ToLower(input) {
		case "exit", "quit", "q":
			fmt.Println("[退出]")
			modelAgent.CloseSession(sessionID)
			return
		case "n", "new":
			// 结束旧会话（归还其浏览器网），切换到全新上下文
			modelAgent.CloseSession(sessionID)
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
