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

// SessionMeta provides summary info for the session list.
type SessionMeta struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
}

// Session holds the in-memory state for a single conversation.
type Session[M adk.MessageType] struct {
	ID        string
	CreatedAt time.Time

	filePath           string
	mu                 sync.Mutex
	messages           []M
	pendingInterruptID string // non-empty while the agent is paused awaiting human approval
	msgIdx             int    // A2UI component slot index at the point of last interrupt
}

// SetPendingInterruptID saves the interrupt ID so the approve endpoint can resume it.
func (s *Session[M]) SetPendingInterruptID(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingInterruptID = id
}

// GetPendingInterruptID returns the stored interrupt ID, or "" if none is pending.
func (s *Session[M]) GetPendingInterruptID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingInterruptID
}

// SetMsgIdx stores the A2UI component slot counter so a resume can continue from it.
func (s *Session[M]) SetMsgIdx(idx int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgIdx = idx
}

// GetMsgIdx returns the stored component slot counter.
func (s *Session[M]) GetMsgIdx() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.msgIdx
}

// Append adds a message to memory and persists it to disk.
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

// GetMessages returns a snapshot of all messages.
func (s *Session[M]) GetMessages() []M {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make([]M, len(s.messages))
	copy(result, s.messages)
	return result
}

// UserText returns text only for user-role messages.
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

// Title derives a display title from the first user message.
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
	return "New Session"
}
