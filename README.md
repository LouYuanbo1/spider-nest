# [spider-nest](https://github.com/LouYuanbo1/spider-nest)

> 声明式并发网页抓取库，内置 LLM（大模型）驱动的智能浏览器自动化能力。

`spider-nest` 把「浏览器进程 / 页面 / 抓取任务」抽象成 **巢（Nest）→ 蜘蛛（Spider）→ 网（Web）** 三层资源池，
并通过 `DeepSeek + eino` 封装出一个能够听指令、自己规划并操作真实浏览器的 **网页数据抓取智能体**：
打开任意 URL、发现可点击元素、按 CSS / XPath / 可访问性节点精准点击与输入、滚动懒加载、执行 JS、三模式提取内容，一气呵成。

---

## 目录

- [项目结构](#项目结构)
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
├── config/
│   ├── config.go            # 配置结构体定义（SpiderConfig / RodConfig /ModelConfig）
│   ├── load.go              # YAML 配置加载；API Key 支持环境变量优先级
│   ├── config.yaml          # 本地实际配置（已被 .gitignore 忽略，勿提交真实 Key）
│   └── config_example.yaml  # 配置模板（可复制为 config.yaml 使用）
│
├── nest/
│   └── nest.go              # 巢：蜘蛛池（多浏览器并发），Acquire/Release/Borrow
│
├── spider/
│   ├── spider.go            # Weaver：单浏览器实例 + 页面网池；SpiderAPI 契约
│   ├── option.go            # Chrome 启动器选项函数（headless/sandbox/UA/窗口 等）
│   ├── hijack.go            # Weaver 侧 hijack 编排：WithHijack(即用即撤窗口)
│   ├── hijack/              # 叶包：HijackRule / 三种 Capture / 各工厂 handler（跨包共用不背蜘蛛）
│   ├── direct/              # 叶包：聪明直连蜘蛛（浏览器只当"头制造机"，纯 HTTP 直连复用 + 失效回退）
│   ├── option_test.go / hijack_test.go  # 单元测试
│
├── action/                  # 全网共享的动作协议层（类型定义，不含执行）
│   ├── action.go            # 动作类型：Navigate/Click/Input/Scroll/JavaScript/
│   │                        #   GetHTML/GetAXContent/GetStructured/GetInteractive/GetPDF
│   ├── parse.go             # 声明式动作解析核心（Spec + ParseActions），格式无关
│   ├── specs.go             # YAML / JSON 两种格式前端，复用 ParseActions 开放给命令入口/测试
│   └── result.go            # 结构化 ActionResult（协议响应侧）
│
├── web/                     # 网：单张页面的封装与动作执行
│   ├── web.go               # Web 结构体、导航、RunAction/RunActions、超时与人类化延迟
│   ├── click.go / input.go  # CSS/XPath/AX 节点定位的点击与输入
│   ├── scroll.go            # 分段式/带停顿的页面滚动
│   ├── javascript.go        # 页面内执行任意 JS 并序列化返回值
│   ├── interact.go          # 发现页面可交互元素（链接/按钮/输入框，附 node_id）
│   ├── get_ax_content.go    # 基于可访问性树(AX)提取 → 紧凑 Markdown
│   ├── axtree.go            # AX 树获取与按 role/name 定位 node_id
│   ├── ax_render.go         # AX 树 → Markdown 渲染（去重/缩进/表格/噪音过滤）
│   ├── get_structured.go    # HTML 剪枝 + 转可读 Markdown
│   ├── gethtml.go           # 取完整原始 HTML
│   ├── pdf.go               # 页面打印 PDF（流式落盘）+ 多 PDF 合并
│   └── result.go            # 结果填充：补页面标题 / URL 等上下文
│
├── agent/
│   ├── agent.go             # 智能体：工具注册、指令提示词、流式输出、思考过程回放
│   ├── tools/
│   │   └── tools.go         # 面向模型的工具（navigate/click/input/scroll/js/html/pdf…）
│   └── mnemo/
│       └── fs/              # 记忆持久化（JSONL 会话存储）
│           ├── store.go     # Store / Session 元数据管理（GetOrCreate/List/Delete）
│           └── session.go   # Session 追加/读取/标题推导
│
├── cmd/                     # 可执行入口（内置 4 个 demo）
│   ├── ai/                  # 终端多轮对话 CLI（读取配置、拉起 agent、持久化会话）
│   ├── direct/              # 聪明直连 demo：浏览器当"头制造机"，纯 HTTP 直连 + 失效回退
│   ├── manual/              # 程序化捕获/Hijack 手动演示
│   └── parse/               # 声明式动作 demo：从 YAML（actions.yaml）解析动作序列并执行
│
├── user_data/               # 本地运行数据（已 ignore）：sessions/(对话历史) browser/(Chrome 数据)
├── go.mod / go.sum
├── .gitignore
└── LICENSE
```

**三层资源池模型**（并发扩展与隔离的核心）：

```
Nest ──蜘蛛池──  spiderSize 只蜘蛛，每只 = 一个独立 Chrome 进程
 │
 └─ Spider ──网池── pagesPerSpider 张网，每张网 = 一个标签页
     │
     └─ Web ──执行动作序列（导航/点击/输入/滚动/取数…）
```

- 每只**蜘蛛**独占一个 `<*rod.Browser>`（独立 Chrome 进程）；
- 每张**网**持有并操作一张页面，是真正执行任务的最小单位；
- 并发上限 = 巢内蜘蛛数 × 每只蜘蛛页面池容量。

---

## 核心亮点

- **LLM 驱动的爬虫智能体**：基于 `DeepSeek + eino/adk`，模型自主决策调用哪些浏览器工具，完成「导航 → 定位 → 交互 → 提取」多步流程，支持流式打字输出与思路回放。
- **真实浏览器自动化**：基于 `go-rod`，非简单 HTTP 抓取，天然兼容 JS 动态渲染（SPA）、懒加载、需登录页面。
- **三层资源池 + 懒加载 + 失效重建**：浏览器按需惰性启动；复用实例避免反复启停 Chrome；检测到页面/浏览器失效时自动销毁并补建，杜绝「借了不还」死锁（`Borrow` 保证归还）。
- **三种取数模式，token 级省显存**：
  - `ax`（默认，首选）：基于浏览器可访问性树输出紧凑 Markdown，天然剔除隐藏/装饰/脚本节点，还原标题/列表/表格与层级，每条可交互元素附 `node_id` 可直接点击/输入，最省 token；
  - `structured`：已剪枝 DOM 转可读 Markdown，保留粗体/行内代码等富文本内联格式；
  - `full`：完整原始 HTML，适合需要精确 DOM 结构 / CSS 属性 / 选择器定位的场景。
- **声明式动作（YAML/JSON）**：动作层提供格式无关的解析核心 `action.ParseActions`，并内置 YAML / JSON 两种前端（`ParseYAML` / `ParseJSON`）；把"步骤"写成配置文件即可交由爬虫/巢按序执行，也天然是智能体工具的同一套契约。
- **人类化行为模拟**：逐字打字 + 随机停顿 + 偶发打错后退格、点击前 hover 停留、分段滚动并穿插「阅读」停顿，降低反爬特征。
- **执行可观测**：每步动作返回结构化 `ActionResult`（动作名 / 标题 / URL / 返回值 / 落盘产物路径 / 是否生效）。
- **健壮性设计**：单动作超时控制、工具调用超时后让模型反思重试、JS 偶发失败自动重试、流式工具参数跨 chunk 正确累积、PDF 流式落盘 `O(1)` 内存不放大文件全量载入。
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
   go run ./cmd/ai         # 智能体多轮对话 CLI
   go run ./cmd/parse      # 声明式动作：解析 cmd/parse/actions.yaml 并执行
   go run ./cmd/direct     # 聪明直连：偷头 → 纯 HTTP 直连 + 失效回退
   go run ./cmd/manual     # 程序化捕获 / Hijack 手动演示（需 httbin 可访问）
   ```

4. **终端交互（`cmd/ai`）**

   ```
   [已创建新会话 20261023-1a2b]
   >>> 请输入指令 (输入 n/new 新建对话, exit/quit/q 退出):
   ```

   - 输入指令如「打开 Bilibili，搜索「赌博默示录」，打开播放最高的视频」；
   - `n` / `new` 新建会话（新上下文）；`exit` / `quit` / `q` 退出。

---

## 使用示例

> 库提供**命令式 API**：`Borrow` / `WithHijack`，一次「借-用-还」即用即撤，监听随函数窗口结束自动释放（下例 1/2/3）。

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

### 3. 多浏览器并发（Nest）

```go
import "github.com/LouYuanbo1/spider-nest/nest"
import "github.com/LouYuanbo1/spider-nest/spider"

// 3 只蜘蛛（Chrome 进程），每只最多 2 张网。
// Generic：巢的类型参数是蜘蛛的具体类型；create 闭包捕获配置创建蜘蛛，
// 巢本身不持有配置。
n, _ := nest.NewNest(3, func() (*spider.Weaver, error) {
    return spider.NewWeaver(spider.WithConfig(cfg.Spider))
})
defer n.Close()

// Crawl：借一只蜘蛛 → 顺序跑动作 → 自动归还（简单场景一步到位）
results, err := n.Crawl(ctx, actions)
// 需要监听时：n.Crawl(ctx, actions, cfgA, cfgB)（可选变参）
fmt.Println(results)

// BorrowSpider：需要借蜘蛛做更复杂编排时，明确定位拿蜘蛛本体
err := n.BorrowSpider(func(s spider.SpiderAPI) error {
    _, err := s.Crawl(ctx, actions)
    return err
})
```

### 4. 聪明直连（direct）：把浏览器当「头制造机」复用

> `spider/direct` 与浏览器解耦：先用浏览器捕获一套带实时 Cookie/反爬签名的请求头，
> 之后直接走纯 HTTP 直连复用，快、不占页面；遇 403/429 再回退重新偷头。

```go
fetcher := direct.NewFetcher(direct.WithTimeout(30 * time.Second))
src := direct.NewSpiderHeaderSource(spiderInstance) // SpiderAPI 即可
cli := direct.New(fetcher, src, direct.WithMaxRetries(2))
resp, err := cli.Fetch(ctx, direct.Target{Method: "GET", URL: "https://api.x.com/res"})
```

### 5. 用智能体做多轮对话

> 智能体（agent）自带 ReAct 工具循环，拿到一张网即可**自己开口下指令、自己上手操作**，
> 多轮对话通过 session 持久化上下文（cfg 为 config.Load 的返回值，见例 1）。

```go
import (
    "github.com/cloudwego/eino-ext/components/model/deepseek"
    "github.com/LouYuanbo1/spider-nest/agent"
    "github.com/LouYuanbo1/spider-nest/spider"
)

chatModel, _ := deepseek.NewChatModel(ctx, &deepseek.ChatModelConfig{
    APIKey: cfg.Model.APIKey,
    Model:  "deepseek-flash",
})

s, _ := spider.NewWeaver(spider.WithConfig(cfg.Spider)) // 一只蜘蛛
defer s.Close()
w, _ := s.Acquire(); defer s.Release(w)   // 借一张网
modelAgent, _ := agent.NewDefaultSpiderAgent(ctx, chatModel, w)

err := modelAgent.OutputMessage(ctx, sessionID, "打开xx网站截图首页", func(s string) {
    fmt.Print(s)   // 流式输出
})
```

### 6. 页面转 PDF 并合并

```go
path, err := w.GetPDF(ctx, &action.GetPDF{Artifact: action.Artifact{Dir: "./out"}})
merged, err := web.MergePDFs(ctx, action.Artifact{Dir: "./out", Name: "merged"}, pathA, pathB)
```

### 7. 声明式动作（YAML / JSON）

> 动作层提供格式无关的解析核心 `action.ParseActions`，并内置 YAML / JSON 两种前端
> （`ParseYAML` / `ParseJSON`），把"步骤"写成配置文件即可交给爬虫/巢按序执行。

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

命令入口示例见 `cmd/parse/main.go`（`actions.yaml` 位于 `cmd/parse/actions.yaml`），
新增格式前端只需实现 `action.Spec` 接口的三个方法（`Kind` / `Decode` / `Line`）。

---

## 动作（Action）类型详解

> 动作定义在 `action/action.go` 中，全部实现 `action.Action` 接口。
> 它们既可被 `Spider.Crawl` 组合成**声明式动作序列**直接调用，也可作为**智能体工具**由 LLM 自主择机触发。
> 单步结果统一封装为 `action.ActionResult`（见 `action/result.go`），含**动作名 / 标题 / URL / 返回值 / 落盘产物路径 / 是否生效**。

### 导航
| 动作 | 说明 |
|------|------|
| `Navigate{URL}` | 导航到目标 URL 并等待页面加载完成。URL 会被去空格与首尾反引号清洗，是抓取流程的通常入口。 |

### 点击
| 动作 | 说明 |
|------|------|
| `Click{Type, Selector}` | 用 **CSS 选择器**或 **XPath** 点击元素（`Type` 取 `css` / `xpath`）。适合已知目标结构时。 |
| `ClickByAXTree{AXTreeNode}` | 通过**可访问性语义**（`role` / `name` + 可选的 `nth`）或**后端 DOM 节点 ID**（`BackendDOMNodeID`）精确点击。node_id 唯一、可避免同名/相似选择器误点，是**最推荐**的方式。 |

### 输入
| 动作 | 说明 |
|------|------|
| `Input{Type, Selector, Text, Clear}` | 用 CSS / XPath 定位输入框并键入文本；`Clear=true` 先清空原内容。返回的是"是否真正写入成功"（读回 value 校验），而非仅调用成功。 |
| `InputByAXTree{AXTreeNode, Text, Clear}` | 通过 AX 语义或 node_id 定位输入框键入文本，定位精度更高。 |

### 滚动
| 动作 | 说明 |
|------|------|
| `Scroll{ScrollY}` | 按指定像素滚动页面（可为负向上滚）。开启 `humanize` 时分段滚动并在每段间随机"阅读"停留，配合等待网络空闲，可用于触发懒加载。 |

### 脚本执行
| 动作 | 说明 |
|------|------|
| `JavaScript{JS, Args}` | 在页面上下文执行**任意 JS 并返回序列化结果**。无参脚本会自动包装为 async 函数。偶发异常会给一次重试。⚠️ 高危能力，见"安全"一节。 |

### 内容提取（三种取数模式）
| 动作 | 说明 | 推荐场景 |
|------|------|----------|
| `GetAXContent{}` | 基于**可访问性树(AX)**输出紧凑 Markdown：天然剔除隐藏/装饰/脚本节点，还原标题/列表/表格与层级，附 `node_id` 可直接交互。 | 默认首选，最省 token |
| `GetStructured{}` | 先对页面做 **HTML 剪枝**再转可读 Markdown，保留粗体/行内代码等富文本内联格式。 | 内容丰富的阅读场景 |
| `GetHTML{}` | 取完整原始 HTML，保留精确 DOM 结构 / CSS 属性。 | 需精确结构、选择器定位或源代码 |
| `GetContent{Mode}` | 上面三种模式的**统一入口**，`Mode` 取 `ax`（默认）/ `structured` / `full`。 | 智能体 `get_content_tool` 使用 |

### 交互元素发现 & 产物
| 动作 | 说明 |
|------|------|
| `GetInteractive{}` | 遍历 AX 树采集页面上的**链接 / 按钮 / 输入框**及其 `node_id`、跳转 `href` 等，供精确点击与输入定位。 |
| `GetPDF{*proto.PagePrintToPDF, Artifact}` | 将当前页打印为 PDF 并**流式落盘**到 `Artifact.Dir`（默认系统下载目录），文件名由 `Artifact.Name` 指定或按 URL+时间戳自动生成，返回落盘路径。 |

### 提示
- **node_id 会随整页导航或大规模 DOM 重渲染失效**，多步操作后建议重新用 `GetInteractive` / 重新提取内容刷新。
- 组合序列时，`RunActions` 会依序执行；中途某步出错会返回出错前已成功的部分结果，便于定位失败点。

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
| `model.api_key` | DeepSeek API Key | 推荐用 `AI_SPIDER_API_KEY` 环境变量 |

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
- `no_sandbox: true` 关闭了 Chrome 沙箱，会降低浏览器进程的隔离性。请仅在**受信、隔离**的抓取环境启用；不要用它运行来源不可信的页面并不该发生，尤其配合远程调试端口开放时风险更大。

### ⚠️ `javascript_tool` 与 prompt 注入
- agent 具持执行任意 JavaScript 的能力（`javascript_tool`）。若页面内容可控/含恶意脚本，配合不安全的 prompt 输入，理论上可被诱导执行危险操作。
- 请**仅向可信的受控输入**开放自主 agent，或自行裁剪工具集（编辑 `agent/agent.go` 中 `newSpiderTools` 的注册表）以关闭 `javascript` / 其它高危工具。

### 🔎 `spider/direct` 直连蜘蛛（两点留意）
- **敏感请求头缓存无过期**：`direct.Client` 会把捕获的请求头快照（含 **Cookie / Authorization** 等）按 URL 缓存在内存中，静默重放到后续直连，直到主动 `Clear` / `Fresh` 或命中状态码刷新策略。它**不会自动过期**，长期运行的健壮性程序应在会话切换后调用 `Clear(url)` / `Fresh(...)`，避免把旧会话的登录态错误地重放到新请求。
- **默认无直连超时**：`direct.NewFetcher()` 默认持有「无超时」的 `http.Client`，若调用方 Context 也无 deadline，单次直连可能无限阻塞。请务必显式 `direct.WithTimeout(...)`，或确保 `ctx` 携带超时。

### ℹ️ 其它
- `headless`、`user_mode`、`incognito` 等按部署环境取舍；非 headless 需要桌面环境。
- PDF 等产物默认写入系统 `Downloads` 目录（可通过 `Artifact.Dir` 指定落盘位置）。
- 本工具用于合法、授权的数据抓取，请勿用于绕过访问控制、抓取受保护/侵权内容。

---