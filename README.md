# [spider-nest](https://github.com/LouYuanbo1/spider-nest)

> 声明式并发网页抓取库，内含 LLM（大模型）驱动的智能浏览器自动化，并支持本地与云端两种部署形态。

`spider-nest` 把「浏览器进程 / 页面 / 抓取任务」抽象成 **巢（Nest）→ 蜘蛛（Spider）→ 网（Web）** 三层资源池，
其上再叠加一条"冷热两态会话 + 可寻址活页指针"的编排链，最终以 **MCP（Model Context Protocol）** 暴露给智能体：

- **本地形态（Route A）**：一个进程内用多浏览器池兜底，会话管理器把空闲浏览器自动"冷提交"成检查点、用到时再"重水化"，从而在 LLM 思考期间不占浏览器、抬高并发密度；
- **云端形态（Route B）**：会话侧不做任何浏览器进程，只持节点连接；动作经 `net/rpc + gob`"拍"到云端节点就地执行。

无论哪种形态，底层都是 `DeepSeek + eino` 封装的**网页数据抓取智能体**：打开任意 URL、发现可点击元素、
按 CSS / XPath / 可访问性节点精准点击与输入、滚动懒加载、执行 JS、三模式提取内容，一气呵成。

---

## 目录

- [项目结构](#项目结构)
- [架构：从资源池到可变 HTTP/MCP](#架构从资源池到可变-hhttpmcp)
- [核心亮点](#核心亮点)
- [快速开始](#快速开始)
- [使用示例](#使用示例)
- [动作（Action）类型详解](#动作action类型详解)
- [配置说明](#配置说明)
- [注意事项 / 安全](#注意事项--安全)

---

## 项目结构

```
spider-nest/
├── config/                # 配置：SpiderConfig / NestConfig / ModelConfig / MCPConfig
│   ├── config.go          # 各配置结构体定义（rod、spider、nest、model、mcp）
│   ├── load.go            # YAML 加载；API Key 支持环境变量优先级
│   ├── config.yaml        # 本地实际配置（已被 .gitignore 忽略，勿提交真实 Key）
│   └── config_example.yaml # 配置模板（可复制为 config.yaml 使用）
│
├── action/                # 全网共享的动作协议层（类型定义，不含执行）
│   ├── action.go          # 动作类型：Navigate/Click/ClickByAXTree/Input/InputByAXTree/
│   │                      #   Scroll/JavaScript/GetContent/GetInteractive/GetPDF 等
│   ├── parse.go           # 声明式动作解析核心（Spec + ParseActions），格式无关
│   ├── specs.go           # YAML / JSON 两种格式前端，复用 ParseActions
│   ├── request.go         # 带句柄进/出的请求模型（供 MCP 工具层复用）
│   └── result.go          # 结构化 ActionResult（协议响应侧）
│
├── web/                   # 网：单张页面的封装与动作执行
│   ├── web.go             # Web 契约(interface)：RunAction/RunActions/CaptureCheckpoint/RestoreCheckpoint
│   ├── page.go            # Page 结构体（Web 契约的本地实现）+ 导航、超时与人类化延迟
│   ├── checkpoint.go      # Checkpoint/Cookie：可序列化的会话状态（冷提交/重水化）
│   ├── click.go input.go  # CSS/XPath/AX 节点定位的点击与输入
│   ├── scroll.go          # 分段式/带停顿的页面滚动
│   ├── javascript.go      # 页面内执行任意 JS 并序列化返回值
│   ├── interact.go        # 发现页面可交互元素（链接/按钮/输入框，附 node_id）
│   ├── get_ax_content.go  # 基于可访问性树(AX)提取 → 紧凑 Markdown
│   ├── axtree.go ax_render.go # AX 树获取/按 role/name 定位 node_id、AX→Markdown 渲染
│   ├── get_structured.go  # HTML 剪枝 + 转可读 Markdown
│   ├── gethtml.go         # 取完整原始 HTML
│   ├── pdf.go             # 页面打印 PDF（流式落盘）+ 多 PDF 合并
│   └── result.go          # 结果填充：补页面标题 / URL 等上下文
│
├── spider/                # 蜘蛛：单只浏览器的抓取单元
│   ├── spider.go          # Spider 契约（借/还网、Crawl/BorrowWeb/WithHijack、WebPoolStats）
│   ├── weaver.go          # Weaver：单浏览器实例 + 页面网池（Spider 的基础实现）
│   ├── portia.go          # Portia：聪明直连蜘蛛（浏览器当"头制造机"，纯 HTTP 直连 + 失效回退）
│   ├── option.go          # Chrome 启动器选项函数（headless/sandbox/UA/窗口 等）
│   ├── hijack.go          # Weaver 侧 hijack 编排：WithHijack(即用即撤窗口)
│   └── hijack/            # 叶包：HijackRule / 三种 Capture / 各工厂 handler（跨包共用）
│
├── nest/                  # 巢：一池蜘蛛（多浏览器并发）
│   ├── nest.go            # Nest / Colony / IdlePicker 契约（借-用-还一体化接口）
│   ├── grove.go           # Grove：朴素的 chan 信号量蜘蛛池
│   └── orchard.go         # Orchard：余量感知果园（FIFO 空闲队列 + 按余量挑蜘蛛）
│
├── provider/              # 会话底层资源"来源契约"（关键解耦层）
│   ├── provider.go        # WebProvider 接口 + WebHandle（Token + Web + Release）、错误分类
│   ├── nest.go            # NestProvider：把整座巢适配成 WebProvider（本地 Route A）
│   ├── spider.go          # SpiderProvider：把单只蜘蛛的网池适配成 WebProvider
│   └── remote.go          # RemoteProvider：云端编排后端，按节点余量挑节点借活页（Route B）
│
├── session/               # 会话核心：把一纸租约提升为"冷热两态"状态机
│   └── session.go         # session.Manager：Open/Resolve(重水化)/Release、hotTTL 自动冷提交、保活(pinned)
│
├── server/                # MCP over HTTP
│   ├── mcp.go             # NewMCPServer：浏览器操作工具 + 生命周期工具(open/close/list web)
│   └── http.go            # NewHTTPHandler / RunHTTPServer / Listen（Streamable HTTP，无状态可横向扩展）
│
├── cloud/                 # Route B 云端节点
│   ├── proto/             # proto.go：net/rpc+gob 线槽协议（动作/检查点注册、PoolStat 快照）
│   ├── agent/             # agent.go：云端节点 RPC 执行器（借/跑/检查点/还/续期 + 失活回收）
│   └── client/            # client.go：会话侧 Conn + RemotePage（把令牌包装成远端活页代理）
│
├── agent/                 # 智能体（LLM 驱动）
│   ├── agent.go           # SpiderAgent：eino/adk 结构，RAG + MCP 客户端驱动真实浏览器
│   ├── mcp/               # MCP 客户端：navigate/click/input/javascript/get_content/get_pdf…
│   └── mnemo/fs/          # 记忆持久化：Store / Session（JSONL 会话存储、标题推导）
│
├── cmd/                   # 可执行入口
│   ├── ai/                # 终端多轮对话 CLI：内嵌本地 MCP 服务 + 智能体 + 持久化记忆
│   ├── server/            # 把 spider-nest 以 MCP(HTTP) 暴露：go run ./cmd/server [-agents 节点]
│   ├── agent/             # 云端节点入口：go run ./cmd/agent -addr :9001（Route B 节点侧）
│   ├── portia/            # 聪明直连 demo：偷头 → 纯 HTTP 直连 + 失效回退
│   ├── manual/            # 程序化捕获 / Hijack 手动演示
│   └── parse/             # 声明式动作 demo：从 YAML（actions.yaml）解析动作序列并执行
│
├── user_data/              # 本地运行数据（已 ignore）：sessions/(对话历史) browser/(Chrome 数据)
├── go.mod / go.sum
├── .gitignore
└── LICENSE
```

**三层资源池模型**（并发扩展与隔离的核心底板）：

```
Nest ──蜘蛛池──  spider_pool_limit 只蜘蛛，每只 = 一个独立 Chrome 进程
 │
 └─ Spider ──网池── page_pool_limit 张网，每张网 = 一个标签页
     │
     └─ Web ──执行动作序列（导航/点击/输入/滚动/取数…），具体实现为 *web.Page
```

- 每只**蜘蛛**独占一个 `<*rod.Browser>`（独立 Chrome 进程）；
- 每张**网**持有并操作一张页面（`web.Page`，满足契约 `web.Web`），是真正执行任务的最小单位；
- 并发上限 = 巢内蜘蛛数 × 每只蜘蛛页面池容量。

---

## 架构：从资源池到可变 HTTP/MCP

资源池之上，`provider / session / server / cloud` 把"浏览器进程"进一步抽成两层职责，支撑本地与云端两种形态：

```
                      ┌─────────────────────────────────────────────────────┐
  MCP 客户端 / 智能体 │  navigate_tool / click_tool / input_tool / js / get_content … │
                      └──────────────────────────┬──────────────────────────┘
                                                 │ MCP (HTTP, Streamable)
        ┌────────────────────────────────────────┴───────────────────────────┐
        │             server（MCP over HTTP：open_web_tool 借网 → web_id 路由）        │
        └────────────────────────────────────────┬───────────────────────────┘
                                                 │ web_id 解析
        ┌────────────────────────────────────────┴───────────────────────────┐
        │        session（冷热两态状态机）：热=活页，冷=检查点，空闲自动冷提交/重水化         │
        └────────────────────────────────────────┬───────────────────────────┘
                                                 │ WebProvider 契约
    ┌──────────────────────────────┬──────────────────────────────┐
    │ provider.NestProvider 本地多浏览器池 │  provider.SpiderProvider 单机蜘蛛  │  provider.RemoteProvider 云端编排 │
    └───────────────┬──────────────┴──────────────┬──────────────┴───────────────┬──────────────┘
                    │ Nest(Grove/Orchard)    蜘蛛(Weaver)            节点(cloud/agent, net/rpc)                     │
                    │  → 蜘蛛(Weaver)          网(web.Page)          会话侧动作经 RPC 拍到节点就地执行               │
                    │  → 网(web.Page)
```

- `provider.WebProvider` 把"资源从哪来"与"资源怎么用"解耦：本地用 `NestProvider`/`SpiderProvider`，云端用 `RemoteProvider`，上层 `session` 只认该契约。
- `session.Manager` 把借来的活页包装成带 `web_id` 的会话：活跃操作用热页，空闲超 `hotTTL` 自动冷提交（归还浏览器、保留检查点），再次用到按检查点重水化。
- `server.NewMCPServer` 把浏览器操作注册成官方 `modelcontextprotocol/go-sdk` 工具，经 HTTP（Streamable、无状态）暴露，可水平扩展；任意客户端只凭 `web_id` 即可路由到对应活页。

---

## 核心亮点

- **LLM 驱动智能体**：基于 `DeepSeek + eino/adk`，模型经 MCP 客户端自主决策调用浏览器工具，完成「导航 → 定位 → 交互 → 提取」多步流程，流式打字输出与思路回放。
- **真实浏览器自动化**：基于 `go-rod`，非简单 HTTP 抓取，天然兼容 JS 动态渲染（SPA）、懒加载、需登录页面。
- **三层资源池 + 懒加载 + 失效重建**：浏览器按需惰性启动；复用实例避免反复启停 Chrome；检测到页面/浏览器失效时自动销毁并补建，杜绝"借了不还"死锁（`Borrow`/`BorrowWeb` 保证归还）。
- **可寻址活页指针（web_id）**：`provider.WebHandle` 携带跨 RPC 可寻址的令牌；会话不绑定具体进程内页面，只绑定"会话"，进程重启/滚动发布后仍能凭检查点重水化。
- **冷热两态会话**：热=活页（零重建成本），冷=检查点（不占浏览器）。空闲热页自动冷提交，LLM 思考/idle 期间不占浏览器，从而抬高并发密度（体现在 `spider_pool_limit` 的意义上）。
- **三种取数模式，token 级省显存**：
  - `ax`（默认，首选）：基于浏览器可访问性树输出紧凑 Markdown，天然剔除隐藏/装饰/脚本节点，还原标题/列表/表格与层级，可交互元素附 `node_id` 可直接点击/输入，最省 token；
  - `structured`：已剪枝 DOM 转可读 Markdown，保留粗体/行内代码等富文本内联格式；
  - `full`：完整原始 HTML，适合需要精确 DOM 结构 / CSS 属性 / 选择器定位的场景。
- **声明式动作（YAML/JSON）**：`action.ParseActions` 提供格式无关解析核心，内置 YAML / JSON 前端（`ParseYAML` / `ParseJSON`），把"步骤"写进配置即可交由爬虫/巢按序执行，也是智能体工具的同一套契约。
- **人类化行为模拟**：逐字打字 + 随机停顿 + 偶发打错后退格、点击前 hover 停留、分段滚动穿插"阅读"停顿，降低反爬特征。
- **执行可观测**：每步返回结构化 `ActionResult`（动作名 / 标题 / URL / 返回值 / 落盘产物路径 / 是否生效）。
- **两种部署形态**：本地一条命令（`cmd/ai` / `cmd/server`）可跑通；云端拆成"会话侧调度（`provider.remote` + `cloud/client`）"与"节点侧执行（`cloud/agent`）"，支撑多节点水平扩展与故障隔离。
- **健壮性设计**：单动作超时、工具调用超时后让模型反思重试、JS 偶发失败自动重试、远程租约心跳保活 + reaper 兜底回收、PDF 流式落盘 `O(1)` 内存。
- **会话持久化**：对话历史按会话 ID 追加写入 JSONL，重启后可回填上下文，多轮续聊不断档。

---

## 快速开始

1. **准备依赖**

   - 安装 Chrome（默认路径见 `config/config.yaml` 的 `spider.bin`，可改）；
   - 获取 DeepSeek API Key。

2. **配置**

   ```bash
   cp config/config_example.yaml config/config.yaml
   ```

   编辑 `config/config.yaml`，填写 `model.api_key`。推荐改用环境变量（优先级最高）：

   ```bash
   # Windows PowerShell
   $env:AI_SPIDER_API_KEY = "sk-xxxx"
   ```

3. **运行**

   ```bash
   go mod tidy
   go run ./cmd/ai          # 终端多轮对话 CLI（内嵌本地 MCP 服务 + 智能体 + 记忆）
   go run ./cmd/parse       # 声明式动作：解析 cmd/parse/actions.yaml 并执行
   go run ./cmd/portia      # 聪明直连：偷头 → 纯 HTTP 直连 + 失效回退
   go run ./cmd/manual      # 程序化捕获 / Hijack 手动演示
   go run ./cmd/server      # 把浏览器操作以 MCP(HTTP) 暴露，默认 :8080
   go run ./cmd/server -agents "127.0.0.1:9001"   # Route B：把动作拍给云端节点执行
   go run ./cmd/agent -addr :9001                 # 启动一台云端节点（路由 B 节点侧）
   ```

4. **终端交互（`cmd/ai`）**

   ```
   [已创建新会话 20261107-1a2b]
   >>> 请输入指令 (输入 n/new 新建对话, exit/quit/q 退出):
   ```

   - 输入指令如「打开 Bilibili，搜索「赌博默示录」，打开播放最高的视频」；
   - `n` / `new` 新建会话（新上下文）；`exit` / `quit` / `q` 退出。

---

## 使用示例

> 库提供**命令式 API**：`BorrowWeb` / `WithHijack` / `BorrowSpider`，一次「借-用-还」即用即撤，监听随函数窗口结束自动释放（下例 1/2/3）。

### 1. 以库方式抓取（单组动作）

```go
import (
    "context"
    "github.com/LouYuanbo1/spider-nest/action"
    "github.com/LouYuanbo1/spider-nest/config"
    "github.com/LouYuanbo1/spider-nest/spider"
)

func main() {
    ctx := context.Background()
    cfg, _ := config.Load("config/config.yaml")

    s, err := spider.NewWeaver(spider.WithConfig(cfg.Spider))
    if err != nil { panic(err) }
    defer s.Close()

    actions := []action.Action{
        &action.Navigate{URL: "https://news.ycombinator.com"},
        &action.GetContent{Mode: action.ContentModeAX},
    }

    results, err := s.Crawl(ctx, actions)
    if err != nil { panic(err) }
    // results: []action.ActionResult{...} 每步结构化结果
}
```

### 2. 并发抓取多组动作

```go
groups := [][]action.Action{
    {{&action.Navigate{URL: "https://a.com"}, &action.GetContent{}}},
    {{&action.Navigate{URL: "https://b.com"}, &action.GetContent{}}},
    {{&action.Navigate{URL: "https://c.com"}, &action.GetContent{}}},
}
// 每组独占一张网并发执行
allResult, err := s.CrawlConcurrent(ctx, groups)
```

### 3. 多浏览器并发（巢）

```go
import (
    "github.com/LouYuanbo1/spider-nest/nest"
    "github.com/LouYuanbo1/spider-nest/spider"
)

// 3 只蜘蛛（Chrome 进程），每只最多 2 张网。
// Grove：以有缓冲 chan 当信号量的朴素蜘蛛池；Orchard 提供"按余量挑蜘蛛"的进阶编排。
n, _ := nest.NewGrove(3, func() (*spider.Weaver, error) {
    return spider.NewWeaver(spider.WithConfig(cfg.Spider))
})
defer n.Close()

// Crawl：借一只蜘蛛 → 顺序跑动作 → 自动归还（简单场景一步到位）
results, err := n.Crawl(ctx, actions)
// 需要按"借-用-还"窗口挂 hijack 监听时，在变参传入 config.HijackConfig：
// n.Crawl(ctx, actions, hijackCfgA, hijackCfgB)

// BorrowSpider：借蜘蛛做更复杂编排时，明确定位拿蜘蛛本体
err = n.BorrowSpider(func(s *spider.Weaver) error {
    _, err := s.Crawl(ctx, actions)
    return err
})
```

### 4. 聪明直连（Portia）：把浏览器当「头制造机」复用

> 先用浏览器捕获一套带实时 Cookie/反爬签名的请求头，之后直接走纯 HTTP 直连复用，
> 快、不占页面；遇 403/429 再回退重新偷头（`cmd/portia/main.go` 有完整演示）。

```go
import "github.com/LouYuanbo1/spider-nest/spider"

// base 作为"头制造机"（一只普通 Weaver），Portia 借它偷头、走纯 HTTP 直连。
base, _ := spider.NewWeaver(spider.WithConfig(cfg.Spider))
cli, _ := spider.NewPortia(base,
    spider.WithTimeout(30*time.Second),
    spider.WithMaxRetries(2),
)
resp, err := cli.Fetch(ctx, spider.Target{Method: "GET", URL: "https://api.x.com/res"})
if err != nil { /* ... */ }
defer resp.Body.Close()
```

### 5. 用智能体做多轮对话

> 智能体（agent）基于 `eino/adk`，自带 ReAct 工具循环。它与浏览器的交互统一走 MCP：
> 一路内嵌本地 MCP 服务（`server.Listen`），agent 经 MCP 客户端自主借网、操作、取数、还网。
> 多轮对话通过 `mnemo/fs` 持久化上下文。

```go
import (
    "github.com/cloudwego/eino-ext/components/model/deepseek"
    "github.com/LouYuanbo1/spider-nest/agent"
    "github.com/LouYuanbo1/spider-nest/config"
    "github.com/LouYuanbo1/spider-nest/provider"
    "github.com/LouYuanbo1/spider-nest/server"
    "github.com/LouYuanbo1/spider-nest/session"
    "github.com/LouYuanbo1/spider-nest/spider"
    "github.com/modelcontextprotocol/go-sdk/mcp"
)

chatModel, _ := deepseek.NewChatModel(ctx, &deepseek.ChatModelConfig{
    APIKey: cfg.Model.APIKey,
    Model:  "deepseek-flash",
})

// 1) 资源底板：本地 Nest 池（多浏览器）
res, _ := provider.NewNestFromConfig(config.NestConfig{SpiderPoolLimit: 1},
    func() (*spider.Weaver, error) { return spider.NewWeaver(spider.WithConfig(cfg.Spider)) })

// 2) 冷热两态会话管理器（Provider 之上）
mgr, _ := session.New(res, cfg.Spider.OperationTimeout, 60*time.Second, 10*time.Minute)
defer mgr.Shutdown()

// 3) 内嵌本地 MCP 服务（HTTP），智能体经它驱动浏览器
mcpBaseURL, closeLocal, _ := server.Listen(mgr,
    &mcp.Implementation{Name: "spider-nest", Version: "v1.0.0"}, nil)
defer closeLocal()

// 4) 智能体通过 MCP 工具循环自主操作
modelAgent, _ := agent.NewDefaultSpiderAgent(ctx, chatModel, mcpBaseURL)
_ = modelAgent.OutputMessage(ctx, sessionID, "打开xx网站截图首页", func(s string) {
    fmt.Print(s) // 流式输出
})
```

### 6. 页面转 PDF 并合并

```go
path, err := w.GetPDF(ctx, &action.GetPDF{Artifact: action.Artifact{Dir: "./out"}})
merged, err := web.MergePDFs(ctx, action.Artifact{Dir: "./out", Name: "merged"}, pathA, pathB)
```

### 7. 声明式动作（YAML / JSON）

> 动作层提供格式无关的解析核心 `action.ParseActions`，并内置 YAML / JSON 前端
> （`ParseYAML` / `ParseJSON`），把"步骤"写成配置即可交给爬虫/巢按序执行。

```go
import "github.com/LouYuanbo1/spider-nest/action"

// 1) 从 YAML 加载（配置/命令入口/测试通用）：
actions, err := action.ParseYAML(strings.NewReader(`
actions:
  - type: navigate
    url: https://news.ycombinator.com
  - type: get_ax_content
  - type: scroll
    scroll_y: 400
`))

// 2) 从 JSON 加载：
actions, err = action.ParseJSON(strings.NewReader(
    `{"actions": [{"type": "navigate", "url": "https://a.com"}, {"type": "get_ax_content"}]}`,
))

// 3) 交给蜘蛛按序执行：
results, err := s.Crawl(ctx, actions)   // s 见例 1
```

命令入口示例见 `cmd/parse/main.go`（`actions.yaml` 位于 `cmd/parse/actions.yaml`）。
新增格式前端只需实现 `action.Spec` 接口的三个方法（`Kind` / `Decode` / `Line`）。

---

## 动作（Action）类型详解

> 动作定义在 `action/action.go` 中，全部实现 `action.Action` 接口。
> 它们既可被 `Spider.Crawl` 组合成**声明式动作序列**直接调用，也可作为**智能体 / MCP 工具**自主择机触发。
> 单步结果统一封装为 `action.ActionResult`（见 `action/result.go`），含**动作名 / 标题 / URL / 返回值 / 落盘产物路径 / 是否生效**。

### 导航
| 动作 | 说明 |
|------|------|
| `Navigate{URL}` | 导航到目标 URL 并等待页面加载完成。URL 会被去空格与首尾反引号清洗，是抓取流程的通常入口。 |

### 点击
| 动作 | 说明 |
|------|------|
| `Click{Type, Selector}` | 用 **CSS 选择器**或 **XPath** 点击元素（`Type` 取 `css` / `xpath`）。适合已知目标结构时。 |
| `ClickByAXTree{AXTreeNode}` | 通过**可访问性语义**（`role` / `name` + 可选 `nth`）或**后端 DOM 节点 ID**（`BackendDOMNodeID`）精确点击。node_id 唯一、可避免同名/相似选择器误点，是**最推荐**的方式。 |

### 输入
| 动作 | 说明 |
|------|------|
| `Input{Type, Selector, Text, Clear}` | 用 CSS / XPath 定位输入框并键入文本；`Clear=true` 先清空原内容。返回"是否真正写入成功"（读回 value 校验），而非仅调用成功。 |
| `InputByAXTree{AXTreeNode, Text, Clear}` | 通过 AX 语义或 node_id 定位输入框键入文本，定位精度更高。 |

### 滚动
| 动作 | 说明 |
|------|------|
| `Scroll{ScrollY}` | 按指定像素滚动页面（可为负向上滚）。开启 `humanize` 时分段滚动并在每段间随机"阅读"停留，可用于触发懒加载。 |

### 脚本执行
| 动作 | 说明 |
|------|------|
| `JavaScript{JS, Args}` | 在页面上下文执行**任意 JS 并返回序列化结果**。无参脚本会自动包装为 async 函数，偶发异常给一次重试。⚠️ 高危能力，见"安全"一节。 |

### 内容提取（三种取数模式）
| 动作 | 说明 | 推荐场景 |
|------|------|----------|
| `GetAXContent{}` | 基于**可访问性树(AX)**输出紧凑 Markdown：天然剔除隐藏/装饰/脚本节点，还原标题/列表/表格与层级，附 `node_id` 可直接交互。 | 默认首选，最省 token |
| `GetStructured{}` | 先对页面做 **HTML 剪枝**再转可读 Markdown，保留粗体/行内代码等富文本内联格式。 | 内容丰富的阅读场景 |
| `GetHTML{}` | 取完整原始 HTML，保留精确 DOM 结构 / CSS 属性。 | 需精确结构、选择器定位或源码 |
| `GetContent{Mode}` | 上面三种模式的**统一入口**，`Mode` 取 `ax`（默认）/ `structured` / `full`。 | 智能体 / MCP `get_content_tool` 使用 |

### 交互元素发现 & 产物
| 动作 | 说明 |
|------|------|
| `GetInteractive{}` | 遍历 AX 树采集页面上的**链接 / 按钮 / 输入框**及其 `node_id`、跳转 `href` 等，供精确点击与输入定位。 |
| `GetPDF{*proto.PagePrintToPDF, Artifact}` | 将当前页打印为 PDF 并**流式落盘**到 `Artifact.Dir`（默认系统下载目录），文件名由 `Artifact.Name` 指定或按 URL+时间戳自动生成，返回落盘路径。 |

### 声明式 `type` 取值
YAML / JSON 中 `type` 可选：`navigate` / `click` / `click_ax` / `input` / `input_ax` /
`scroll` / `js`(别名 `javascript`) / `get_html` / `get_ax`(别名 `get_ax_content`) /
`get_structured` / `get_content`(需 `mode: ax|structured|full`) / `get_interactives` /
`get_pdf`(别名 `pdf`)。

### 提示
- **node_id 会随整页导航或大规模 DOM 重渲染失效**，多步操作后建议重新用 `GetInteractive` / 重新提取内容刷新。
- 组合序列时，`RunActions` 依序执行；中途出错会返回出错前已成功的部分结果。

---

## 配置说明

`config/config.yaml` 关键字段（见 `config/config.go` 结构体）：

| 字段 | 说明 | 建议 |
|------|------|------|
| `spider.user_data_dir` | Chrome 用户数据目录 | `user_data/browser` |
| `spider.user_mode` | 用户模式（复用系统 profile） | `true` |
| `spider.headless` | 无头模式 | 调试可 `false` |
| `spider.humanize` | 是否模拟人类操作节奏 | `true` |
| `spider.operation_timeout` | 单动作超时 | `30s` |
| `spider.page_pool_limit` | 每只蜘蛛的页面池容量 | `3` |
| `spider.no_sandbox` | 是否关闭 Chrome 沙箱 | 受信环境 `true` |
| `spider.basic_remote_debugging_port` | CDP 远程调试端口 | **见下方安全注意** |
| `spider.trace` | 是否开启 CDP 通信追踪日志 | 调试用 |
| `nest.spider_pool_limit` | 巢内可同时活动的蜘蛛（浏览器）数，即最大并发浏览器数 | `1` |
| `model.api_key` | DeepSeek API Key | 推荐用 `AI_SPIDER_API_KEY` 环境变量 |
| `mcp.base_url` | agent 侧工具调用所连接的远端 MCP HTTP 端点 | 如 `http://127.0.0.1:8080/mcp` |

---

## 注意事项 / 安全

> 使用前请务必阅读本节，尤其涉及密钥与浏览器访问控制。

### 🔑 密钥管理
- **API Key 请通过环境变量 `AI_SPIDER_API_KEY` 注入**，它有最高优先级；`config/config.yaml` 的 `api_key` 仅作本地兜底。
- `config/config.yaml` 已被加入 `.gitignore`，**请不要提交真实 Key 到仓库**，公开时用 `config_example.yaml` 模板。

### 🚫 本地数据勿提交
- `user_data/` 保存了**浏览器登录态、Cookie 与会话对话记录**，含高度敏感信息，已被 `.gitignore` 排除。若你曾手动 `git add -f`，请立即从仓库移除。

### 🌐 远程调试端口（重点）
- `basic_remote_debugging_port: 9222` 会开启 Chrome DevTools（CDP）协议，**该协议本身无鉴权**：任何人只要能连通该端口，就能完全接管浏览器（读取 Cookie、执行任意 JS、控制页面）。
- 默认 launcher 仅在**本机**监听，请**切勿**将该端口映射到公网或局域网（如 Docker `-p 9222:9222`、内网穿透等）。
- 在不受信环境请关闭该端口（设为 `0`）或配合防火墙白名单。

### 🖥️ 沙箱降级
- `no_sandbox: true` 关闭了 Chrome 沙箱，会降低浏览器进程的隔离性。请仅在**受信、隔离**的抓取环境启用；尤其配合远程调试端口开放时风险更大。

### ⚠️ `javascript_tool` 与 prompt 注入
- agent 具持执行任意 JavaScript 的能力（`javascript_tool`）。若页面内容可控/含恶意脚本，配合不安全的 prompt 输入，理论上可被诱导执行危险操作。
- 请**仅向可信的受控输入**开放自主 agent，或自行裁剪工具集（编辑 `server/mcp.go` 中 `buildServer` 的注册表 / `agent/mcp` 客户端）以关闭 `javascript` 及其它高危工具。

### 🔎 `Portia` 直连蜘蛛（两点留意）
- **敏感请求头缓存无过期**：`Portia`（`spider.NewPortia`）会把偷到的请求头快照（含 **Cookie / Authorization** 等）按 URL 缓存在内存中，静默重放到后续直连，直到主动 `Clear(url)` / `Fresh(ctx, t)` 或命中状态码刷新策略。它**不会自动过期**，长期运行的健壮性程序应在会话切换后调用 `Clear(url)` / `Fresh(ctx, t)`，避免把旧会话的登录态错误地重放到新请求。
- **默认无直连超时**：`NewPortia` 默认不设置超时（依赖调用方 `ctx` 的 deadline），若 `ctx` 也无 deadline，单次直连可能无限阻塞。请务必显式 `spider.WithTimeout(...)`，或确保 `ctx` 携带超时。

### ℹ️ 其它
- `headless`、`user_mode`、`incognito` 等按部署环境取舍；非 headless 需要桌面环境。
- PDF 等产物默认写入系统 `Downloads` 目录（可通过 `Artifact.Dir` 指定落盘位置）。