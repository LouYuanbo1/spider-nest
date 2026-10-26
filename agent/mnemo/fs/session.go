package fs

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

type Kind string

const (
	KindMessage Kind = "message"
	KindAgentic Kind = "agentic"
)

// SessionMeta 提供会话列表所需的摘要信息。
type SessionMeta struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
}

// Session 持有单个会话在内存中的状态。
type Session[M adk.MessageType] struct {
	ID        string
	CreatedAt time.Time

	filePath           string
	mu                 sync.Mutex
	messages           []M
	pendingInterruptID string // 智能体暂停等待人工审批期间非空
	msgIdx             int    // 上次中断点处的 A2UI 组件槽位索引
}

// SetPendingInterruptID 保存中断 ID，供审批接口后续恢复会话。
func (s *Session[M]) SetPendingInterruptID(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingInterruptID = id
}

// GetPendingInterruptID 返回已保存的中断 ID；无待审批时返回空串。
func (s *Session[M]) GetPendingInterruptID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingInterruptID
}

// SetMsgIdx 保存 A2UI 组件槽位计数，供恢复时继续。
func (s *Session[M]) SetMsgIdx(idx int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgIdx = idx
}

// GetMsgIdx 返回已保存的组件槽位计数。
func (s *Session[M]) GetMsgIdx() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.msgIdx
}

// Append 向内存追加一条消息并落盘持久化。
func (s *Session[M]) Append(msg M) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.messages = append(s.messages, msg)

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	f, err := os.OpenFile(s.filePath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = fmt.Fprintf(f, "%s\n", data)
	return err
}

// GetMessages 返回全部消息的快照。
func (s *Session[M]) GetMessages() []M {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make([]M, len(s.messages))
	copy(result, s.messages)
	return result
}

// UserText 仅提取用户角色的消息文本。
func UserText[M adk.MessageType](msg M) string {
	switch m := any(msg).(type) {
	case *schema.Message:
		if m == nil || m.Role != schema.User {
			return ""
		}
		return messageText(m)
	case *schema.AgenticMessage:
		if m == nil || m.Role != schema.AgenticRoleTypeUser {
			return ""
		}
		var parts []string
		for _, block := range m.ContentBlocks {
			if block != nil && block.Type == schema.ContentBlockTypeUserInputText && block.UserInputText != nil {
				parts = append(parts, block.UserInputText.Text)
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func messageText(msg *schema.Message) string {
	if msg == nil {
		return ""
	}
	if msg.Content != "" {
		return msg.Content
	}
	var parts []string
	for _, part := range msg.UserInputMultiContent {
		if part.Type == schema.ChatMessagePartTypeText && part.Text != "" {
			parts = append(parts, part.Text)
		}
	}
	for _, part := range msg.AssistantGenMultiContent {
		if part.Type == schema.ChatMessagePartTypeText && part.Text != "" {
			parts = append(parts, part.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// Title 从第一条用户消息推导展示标题。
func (s *Session[M]) Title() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, msg := range s.messages {
		if text := UserText(msg); text != "" {
			title := text
			if len([]rune(title)) > 60 {
				title = string([]rune(title)[:60]) + "..."
			}
			return title
		}
	}
	return "新会话"
}
