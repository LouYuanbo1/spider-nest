package agent

import (
	"context"
	"fmt"
	"io"
	"log"

	"github.com/LouYuanbo1/spider-nest/agent/mnemo/fs"
	"github.com/LouYuanbo1/spider-nest/agent/tools"
	"github.com/LouYuanbo1/spider-nest/web"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type SpiderAgent struct {
	Agent *adk.ChatModelAgent

	// memory 持久化会话上下文；为 nil 时不保存记忆，退化为一次性执行。
	memory *fs.Store[*schema.Message]
}

// SetMemoryStore 开启持久化记忆并指定会话存储目录。
func (c *SpiderAgent) SetMemoryStore(store *fs.Store[*schema.Message]) {
	c.memory = store
}

func NewSpiderAgent(ctx context.Context, agent *adk.ChatModelAgent) (*SpiderAgent, error) {
	if agent == nil {
		return nil, fmt.Errorf("agent is nil")
	}
	return &SpiderAgent{Agent: agent}, nil
}

func NewDefaultSpiderAgent(ctx context.Context, model model.ToolCallingChatModel, w *web.Web) (*SpiderAgent, error) {

	tools, err := newSpiderTools(w)
	if err != nil {
		return nil, err
	}

	instruction :=
		`
			## 角色定义
			你是一位专业的网页数据抓取智能体，基于真实浏览器环境自动化操作网页，能够打开任意URL、模拟用户交互、执行JavaScript、提取页面内容。你擅长处理JS动态渲染页面，精准获取用户所需的网页数据。

			## 核心能力
			1. **页面导航**：打开指定URL并等待页面完全加载（包括JS动态内容渲染完成）
			2. **可点击元素发现**：遍历页面可访问性树，采集当前页面上的链接与按钮及其 node_id，为精准点击提供依据
			3. **多种方式交互**：提供 select工具（CSS 或 XPath 选择器）与 AX工具（node_id，最精准）两类点击/输入，触发内容加载或页面跳转；node_id 优先，选择器兜底
			4. **页面滚动**：支持指定像素级的页面滚动，用于触发懒加载内容或浏览长页面
			5. **JS脚本执行**：在页面上下文中执行任意JavaScript代码，获取动态数据或操作DOM
			6. **内容提取**：支持三种取数模式——accessibility（基于可访问性树输出紧凑Markdown，自动剔除隐藏/装饰/脚本噪音，默认首选）、structured（将修剪后的正文转为可读Markdown，保留富文本内联格式）、full（完整原始HTML，需要精确DOM结构/选择器定位时使用）

			## 可用工具详解
			| 工具名 | 用途 | 适用场景 |
			|--------|------|----------|
			| navigate_tool | 导航到目标URL并等待页面加载完成 | 用户提供URL需要查看/抓取页面内容时，作为第一步操作 |
			| get_interactives_tool | 获取当前页面所有可交互元素（链接/按钮/文本输入框），均含 node_id | 需要点击元素或向输入框键入文本但不确定其选择器，或需要精准定位避免歧义时，先调用此工具 |
			| click_by_ax_tool | 通过 node_id 精准点击元素 | 已有 get_interactives_tool 返回的 node_id 时，为最推荐、最精准的点击方式；请优先使用 |
			| click_tool | 通过 CSS 选择器或 XPath 点击元素 | 明确知道目标元素的CSS选择器（如 button.load-more）或XPath（如 //div[@data-id="42"]/button）且无歧义时；通过参数 type 指定 css 或 xpath |
			| input_by_ax_tool | 通过 node_id 向输入框键入文本 | get_interactives_tool 的 inputs 列表中有目标输入框的 node_id 时，为最精准的输入方式；请优先使用 |
			| input_tool | 通过 CSS 选择器或 XPath 向输入框键入文本 | 明确知道目标输入框的CSS选择器（如 input.search-input）或XPath（如 //input[@placeholder="搜索"]）且无歧义时；通过参数 type 指定 css 或 xpath |
			| scroll_tool | 按指定像素数滚动页面 | 触发无限滚动加载、浏览长页面、等待懒加载内容出现时 |
			| javascript_tool | 在页面中执行JavaScript并获取返回值 | 仅在无现成工具可完成的复杂自定义场景时使用（详见下方的JS使用准则） |
			| get_content_tool | 以三种模式获取当前页面内容，参数 mode 取 ax（默认、首选）/structured/full：ax 基于浏览器可访问性树(AX)输出紧凑Markdown，天然剔除隐藏/装饰/脚本节点并还原标题/列表/表格与层级，可交互元素附 node_id 可直接点击/输入，最省 token；内容丰富的富文本阅读用 mode=structured（保留粗体/行内代码）；需精确代码/表格/链接/CSS 选择器或原始源码用 mode=full |
			| get_pdf_tool | 将当前页面打印为PDF并落盘到产物目录，返回落盘文件路径 | 需要保存页面快照或离线阅读时 |

			## 点击方式选择建议（重要）
			- **优先使用 click_by_ax_tool**：node_id 唯一且明确，可避免因页面中存在重复文本/相似选择器导致的误点，是最可靠的点击方式。
			- 点击前不确定目标元素结构时，先调用 get_interactives_tool 获取页面可交互元素列表，从中选取目标元素对应的 node_id 进行点击。
			- **注意**：node_id 在整页导航或大规模 DOM 重渲染后会失效，若点击失效请重新调用 get_interactives_tool 获取最新的 node_id。
			- 在无歧义且对结构非常确定时，也可直接使用 click_tool（type=css 或 type=xpath）。

			## 输入方式选择建议（重要）
			- **优先使用 input_by_ax_tool**：先调用 get_interactives_tool，其返回的 inputs 列表包含输入框的 node_id，据此精准定位输入，最可靠。
			- 无法拿到 node_id 时，再按需用 input_tool（type=css 或 type=xpath）。
			- **node_id 失效注意**：整页导航或大规模 DOM 重渲染后 node_id 会失效，请先重新调用 get_interactives_tool 刷新。
			- 向已有内容的输入框输入前，可设 clear=true 先清空再键入。

			## JS使用准则（重要，务必遵守）
			- **能用已有的专用工具（navigate/click/input/scroll/get_content）解决的问题，绝不使用 javascript_tool**，以减少出错几率。
			- 向输入框键入文本一律用 input_by_ax_tool 或 input_tool，点击用 click_by_ax_tool 或 click_tool，滚动用 scroll_tool，提取内容用 get_content_tool。
			- 仅当现有工具无法完成（如复杂数据处理、特定 DOM 操作、跨域拦截等）时才考虑 javascript_tool，并优先用 get_interactives/输入/点击工具组合替代。

			## 操作工作流
			典型的数据抓取流程：
			1. **导航页面** → 使用 navigate_tool 导航到目标URL
			2. **定位与交互** → 需要点击时优先用 get_interactives_tool 获取 node_id，再用 click_by_ax_tool 精准点击；需要搜索/填写时，同样先用 get_interactives_tool 拿到输入框的 node_id，再用 input_by_ax_tool 键入文本
			3. **滚动** → 根据需要使用 scroll_tool 触发懒加载内容或浏览长页面
			4. **内容提取** → 使用 get_content_tool 获取最终内容（默认 mode=ax 输出紧凑Markdown，最省token且自带可交互node_id；按需切换 mode）

			注意：
			- 每次调用工具后会返回页面的最新内容/摘要，请据此判断是否需要继续操作
			- get_content_tool 默认 mode=ax：基于浏览器可访问性树(AX)输出**紧凑 Markdown**，天然剔除隐藏/装饰/脚本节点并还原标题/列表/表格与层级，token 成本最低、且每条可交互元素附 node_id 可直接点击/输入，是取数首选。需要保留富文本内联格式（粗体/行内代码）时用 mode=structured；需要精确结构/属性（如代码、表格、CSS 选择器定位）或原始源码时用 mode=full（完整原始 HTML，保留动态渲染后的 DOM 结构与样式属性）。**禁止仅凭 Markdown 正文推测或编造结构信息，需要代码/表格/链接/CSS属性时必须取 mode=full**
			- 对于JS动态渲染页面，请确保 navigate_tool 完成加载后再进行后续操作
			- 输入文本/点击后，如需搜索功能生效，通常还需点击搜索按钮或用 get_interactives 找到对应按钮再点击

			## 行为准则
			- 所有操作基于用户明确指令，不主动访问用户未指定的URL
			- 对常规阅读/简要任务，mode=accessibility 已足够且最省 token（自带可交互 node_id）；对需要保留粗体/行内代码的富文本阅读，用 mode=structured；对需要代码、数据表格、结构或可验证细节的任务，必须使用 mode=full 获取足够的结构信息
			- 在点击或滚动后，如页面内容已满足需求，应及时调用 get_content_tool 提取内容
			- 遇到页面加载失败或元素未找到时，如实报告错误并尝试替代方案
			- 提取的正文内容保持原文结构，不添加个人解读
			- 不访问或提取涉及违法、侵权内容的页面

			## 输出格式规范
			完成抓取任务后，请按以下格式组织结果：

			【页面信息】URL、标题（如可获取）
			【抓取内容】以清晰的段落结构呈现提取的正文内容，保留原标题层级
			【操作摘要】简述执行了哪些工具调用及其结果
			【注意事项】（如适用）页面部分内容未加载、需登录、反爬限制等

			## 错误处理策略
			- 页面加载超时 → 报告超时并建议用户确认URL是否正确
			- 元素选择器未命中 → 尝试提供页面中实际存在的相似元素选择器建议
			- JS执行异常 → 报告具体错误信息，检查JS语法或页面是否已加载目标元素
			- 页面返回空内容 → 说明可能原因（反爬、需登录、纯Canvas渲染等），建议尝试 mode=full 获取完整源码，或切换 mode 重试

			## 交互示例
			用户：「在示例网站的搜索框搜"Go爬虫"并抓取第一条结果」
			助手操作流程：
			1. 调用 navigate_tool(url="https://example.com")
			2. 调用 get_interactives_tool 获取页面的 inputs（输入框）和可点击元素及其 node_id
			3. 用 input_by_ax_tool 向搜索框键入"Go爬虫"（node_id 取自 inputs 列表）
			4. 用 click_by_ax_tool 点击搜索按钮
			5. 调用 get_content_tool（默认 mode=ax）提取内容
		`
	searchAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "crawlerAgent",
		Description: "一个基于大模型的网页数据抓取智能体，支持浏览器自动化操作与智能内容提取",
		Instruction: instruction,
		Model:       model,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: tools,
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("Error creating search agent: %w", err)
	}
	return &SpiderAgent{Agent: searchAgent}, nil
}

// newSpiderTools 集中注册 Spider 智能体所需的所有浏览器自动化工具。
// 这里用一张注册表描述“工具名 → 创建函数”，统一依次创建并聚合到工具列表，
// 避免在 NewDefaultSpiderAgent 中堆叠十余段重复的错误处理代码。
func newSpiderTools(w *web.Web) ([]tool.BaseTool, error) {
	constructors := []struct {
		name string
		new  func() (tool.InvokableTool, error)
	}{
		{name: "navigate_tool", new: func() (tool.InvokableTool, error) { return tools.NewNavigateTool(w) }},
		{name: "get_interactives_tool", new: func() (tool.InvokableTool, error) { return tools.NewGetInteractivesTool(w) }},
		{name: "click_tool", new: func() (tool.InvokableTool, error) { return tools.NewClickTool(w) }},
		{name: "click_by_ax_tool", new: func() (tool.InvokableTool, error) { return tools.NewClickByAXTool(w) }},
		{name: "input_tool", new: func() (tool.InvokableTool, error) { return tools.NewInputTool(w) }},
		{name: "input_by_ax_tool", new: func() (tool.InvokableTool, error) { return tools.NewInputByAXTool(w) }},
		{name: "scroll_tool", new: func() (tool.InvokableTool, error) { return tools.NewScrollTool(w) }},
		{name: "javascript_tool", new: func() (tool.InvokableTool, error) { return tools.NewJavaScriptTool(w) }},
		{name: "get_content_tool", new: func() (tool.InvokableTool, error) { return tools.NewGetContentTool(w) }},
		{name: "get_pdf_tool", new: func() (tool.InvokableTool, error) { return tools.NewGetPDFTool(w) }},
	}

	tools := make([]tool.BaseTool, 0, len(constructors))
	for _, c := range constructors {
		t, err := c.new()
		if err != nil {
			return nil, fmt.Errorf("创建工具 %s 失败: %w", c.name, err)
		}
		tools = append(tools, t)
	}
	return tools, nil
}

func (c *SpiderAgent) Run(ctx context.Context, input *adk.AgentInput, options ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	return c.Agent.Run(ctx, input, options...)
}

// OutputMessage 执行一轮对话：把流式输出写到 streamFunc，
// 并在启用了持久化记忆时，将本轮消息按会话 sessionID 追加保存，
// 同时把历史消息作为上下文重新注入本轮。
func (c *SpiderAgent) OutputMessage(ctx context.Context, sessionID, input string, streamFunc func(string), options ...adk.AgentRunOption) error {
	var sess *fs.Session[*schema.Message]
	var history []*schema.Message
	if c.memory != nil {
		s, err := c.memory.GetOrCreate(sessionID)
		if err != nil {
			return fmt.Errorf("get session %q: %w", sessionID, err)
		}
		sess = s
		history = s.GetMessages()
	}

	// 历史 + 本轮用户消息，作为完整输入提交给 agent
	msgs := make([]*schema.Message, 0, len(history)+1)
	msgs = append(msgs, history...)
	msgs = append(msgs, schema.UserMessage(input))

	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: c.Agent, EnableStreaming: true})
	iter := runner.Run(ctx, msgs, options...)

	produced, err := stream(iter, streamFunc)

	// 无论 stream 是否出错，只要已有会话且产生了内容，都应尽量持久化，
	// 避免一次消息错误就导致整段对话丢失。
	if sess != nil {
		sess.Append(schema.UserMessage(input))
		for _, m := range produced {
			// 只持久化输入/回答/工具调用/工具结果，不保存思考过程。
			if m.Role == schema.Assistant {
				m.ReasoningContent = ""
			}
			sess.Append(m)
		}
	}
	return err
}

// stream 消费 agent 事件流，把文本输出交给 streamFunc，
// 并收集本轮产生的完整消息（含助手消息、工具调用与工具结果），用于持久化。
func stream(iter *adk.AsyncIterator[*adk.AgentEvent], streamFunc func(string)) ([]*schema.Message, error) {
	var produced []*schema.Message
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}

		if event.Err != nil {
			log.Printf("Runner 错误: %v", event.Err)
			return produced, event.Err
		}

		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		mo := event.Output.MessageOutput

		// 流式消息：边输出文本边累积 chunk，结束后拼接为完整消息
		if mo.MessageStream != nil {
			msg, err := streamMessage(mo.MessageStream, streamFunc)
			if err != nil {
				return produced, err
			}
			if msg != nil {
				produced = append(produced, msg)
			}
			continue
		}

		// 非流式消息（通常是工具结果）：直接使用完整消息
		if mo.Message != nil {
			produced = append(produced, mo.Message)
		}
	}
	return produced, nil
}

func streamMessage(s adk.MessageStream, streamFunc func(string)) (*schema.Message, error) {
	// 【关键修复 2】：防御性判空，防止外部意外传入 nil 导致 panic
	if s == nil {
		return nil, nil
	}
	defer s.Close()

	chunks := make([]*schema.Message, 0, 8)

	// 工具调用信息缓存：在流式模式下，工具调用参数会跨多个 chunk 到达，需要累积
	accToolCalls := make(map[int]*schema.ToolCall)

	for {
		chunk, err := s.Recv()
		if err != nil {
			if err == io.EOF {
				break
			}
			log.Printf("Stream Recv 错误: %v", err)
			return nil, err
		}

		chunks = append(chunks, chunk)

		// 处理思考过程 (如果开启了 DeepSeek-R1 等支持 Reasoning 的模型)
		if chunk.ReasoningContent != "" {
			flushToolCalls(accToolCalls, streamFunc)
			streamFunc(chunk.ReasoningContent)
			continue
		}

		// 处理工具调用：累积各 chunk 中的参数片段
		if len(chunk.ToolCalls) > 0 {
			accumulateToolCalls(accToolCalls, chunk.ToolCalls)
			continue
		}

		// 处理文本内容：先刷新累积的工具调用，再输出文本
		if chunk.Content != "" {
			flushToolCalls(accToolCalls, streamFunc)
			streamFunc(chunk.Content)
		}
	}

	// 流结束时刷新残留的工具调用
	flushToolCalls(accToolCalls, streamFunc)

	if len(chunks) == 0 {
		return nil, nil
	}
	return schema.ConcatMessages(chunks)
}

// accumulateToolCalls 将流式 chunk 中的工具调用信息累积到 acc 中
func accumulateToolCalls(acc map[int]*schema.ToolCall, toolCalls []schema.ToolCall) {
	for _, tc := range toolCalls {
		idx := 0
		if tc.Index != nil {
			idx = *tc.Index
		}

		if existing, ok := acc[idx]; ok {
			if tc.ID != "" {
				existing.ID = tc.ID
			}
			if tc.Type != "" {
				existing.Type = tc.Type
			}
			if tc.Function.Name != "" {
				existing.Function.Name = tc.Function.Name
			}
			existing.Function.Arguments += tc.Function.Arguments
		} else {
			cp := tc
			acc[idx] = &cp
		}
	}
}

// flushToolCalls 输出所有累积的工具调用信息并清空缓存
func flushToolCalls(acc map[int]*schema.ToolCall, streamFunc func(string)) {
	for idx := 0; idx < len(acc); idx++ {
		tc, ok := acc[idx]
		if !ok {
			continue
		}
		line := "\n[工具调用] " + tc.Function.Name
		if len(tc.Function.Arguments) > 0 {
			line += ", 参数: " + tc.Function.Arguments
		}
		streamFunc(line + "\n")
		delete(acc, idx)
	}
}
