package fs

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// Store 管理基于 JSONL 文件持久化的会话集合。
//
// 文件格式：
//
//	{"type":"session","id":"...","created_at":"...","message_kind":"agentic"}   ← 首行头部
//	{"role":"user","content_blocks":[...]}                                      ← 后续消息行
type Store[M adk.MessageType] struct {
	dir   string
	kind  Kind
	mu    sync.Mutex
	cache map[string]*Session[M]
}

func KindOf[M adk.MessageType]() Kind {
	var zero M
	switch any(zero).(type) {
	case *schema.AgenticMessage:
		return KindAgentic
	default:
		return KindMessage
	}
}

func ValidateKind(stored, target Kind, legacyMessageOK bool) error {
	if stored == "" && target == KindMessage && legacyMessageOK {
		return nil
	}
	if stored == "" {
		return fmt.Errorf("session file has no message_kind; current MESSAGE_KIND=%s", target)
	}
	if stored != target {
		return fmt.Errorf("session file uses message_kind=%s; current MESSAGE_KIND=%s", stored, target)
	}
	return nil
}

// NewStore 创建基于指定目录的 Store（目录不存在则自动创建）。
func NewStore[M adk.MessageType](dir string) (*Store[M], error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create session dir: %w", err)
	}
	return &Store[M]{
		dir:   dir,
		kind:  KindOf[M](),
		cache: make(map[string]*Session[M]),
	}, nil
}

// GetOrCreate 返回 id 对应的会话，不存在则创建。
func (s *Store[M]) GetOrCreate(id string) (*Session[M], error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if sess, ok := s.cache[id]; ok {
		return sess, nil
	}

	filePath := filepath.Join(s.dir, id+".jsonl")

	var (
		sess *Session[M]
		err  error
	)
	if _, statErr := os.Stat(filePath); os.IsNotExist(statErr) {
		sess, err = createSession[M](id, filePath)
	} else {
		sess, err = loadSession[M](filePath)
	}
	if err != nil {
		return nil, err
	}

	s.cache[id] = sess
	return sess, nil
}

// List 返回所有已知会话的元信息。
func (s *Store[M]) List() ([]SessionMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}

	var metas []SessionMeta
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".jsonl")

		if sess, ok := s.cache[id]; ok {
			metas = append(metas, SessionMeta{ID: id, Title: sess.Title(), CreatedAt: sess.CreatedAt})
			continue
		}

		sess, loadErr := loadSession[M](filepath.Join(s.dir, e.Name()))
		if loadErr != nil {
			continue
		}
		metas = append(metas, SessionMeta{ID: id, Title: sess.Title(), CreatedAt: sess.CreatedAt})
	}
	return metas, nil
}

// Delete 删除会话文件并从缓存中移除。
func (s *Store[M]) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	filePath := filepath.Join(s.dir, id+".jsonl")
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return err
	}
	delete(s.cache, id)
	return nil
}

// sessionHeader 是每个会话文件的第一行 JSONL 内容。
type sessionHeader struct {
	Type        string    `json:"type"`
	ID          string    `json:"id"`
	CreatedAt   time.Time `json:"created_at"`
	MessageKind Kind      `json:"message_kind,omitempty"`
}

func createSession[M adk.MessageType](id, filePath string) (*Session[M], error) {
	header := sessionHeader{
		Type:        "session",
		ID:          id,
		CreatedAt:   time.Now().UTC(),
		MessageKind: KindOf[M](),
	}
	data, err := json.Marshal(header)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filePath, append(data, '\n'), 0o644); err != nil {
		return nil, err
	}
	return &Session[M]{
		ID:        id,
		CreatedAt: header.CreatedAt,
		filePath:  filePath,
		messages:  make([]M, 0),
	}, nil
}

func loadSession[M adk.MessageType](filePath string) (*Session[M], error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// 单条消息可能携带超大的工具结果，默认 64KB 单行上限会因
	// bufio.ErrTooLong 让整个会话文件从此无法打开（#9）。
	// 这里把初始缓冲提到 256KB、单行上限放宽到 64MB。
	scanner.Buffer(make([]byte, 256*1024), 64*1024*1024)

	// 首行：会话头部
	if !scanner.Scan() {
		return nil, fmt.Errorf("empty session file: %s", filePath)
	}
	var header sessionHeader
	if err := json.Unmarshal(scanner.Bytes(), &header); err != nil {
		return nil, fmt.Errorf("bad session header in %s: %w", filePath, err)
	}
	if err := ValidateKind(header.MessageKind, KindOf[M](), true); err != nil {
		return nil, fmt.Errorf("cannot load session %s: %w", filePath, err)
	}

	sess := &Session[M]{
		ID:        header.ID,
		CreatedAt: header.CreatedAt,
		filePath:  filePath,
		messages:  make([]M, 0),
	}

	// 其余行：会话消息
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var msg M
		err := json.Unmarshal([]byte(line), &msg)
		if err != nil {
			continue // skip malformed lines
		}
		sess.messages = append(sess.messages, msg)
	}

	return sess, scanner.Err()
}
