package direct

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LouYuanbo1/spider-nest/spider/hijack"
	"github.com/go-rod/rod/lib/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/ysmood/gson"
)

// TestRequestTranslate 验证 Request 把捕获的请求快照翻译成标准 http.Request：
// 方法 / URL / 请求头 / 请求体原样搬移。
func TestRequestTranslate(t *testing.T) {
	c := NewFetcher()
	req, err := c.Request(context.Background(), &hijack.RequestCapture{
		Method: http.MethodPost,
		URL:    "https://example.com/api/fetch?a=1",
		Headers: newNetworkHeaders(map[string]string{
			"X-Token":      "abc",
			"Content-Type": "application/json",
		}),
		Body: `{"q":"x"}`,
	})
	require.NoError(t, err)

	assert.Equal(t, http.MethodPost, req.Method)
	assert.Equal(t, "https://example.com/api/fetch?a=1", req.URL.String())
	assert.Equal(t, "abc", req.Header.Get("X-Token"))
	assert.Equal(t, "application/json", req.Header.Get("Content-Type"))

	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	assert.Equal(t, `{"q":"x"}`, string(body))

	// nil 捕获时应直接报错，避免静默生成空请求。
	_, err = c.Request(context.Background(), nil)
	assert.Error(t, err)
}

// newNetworkHeaders 用普通 map 构造一个 proto.NetworkHeaders(map[string]gson.JSON)。
func newNetworkHeaders(m map[string]string) proto.NetworkHeaders {
	h := make(proto.NetworkHeaders, len(m))
	for k, v := range m {
		h[k] = gson.New(v)
	}
	return h
}

// TestDoRoundTrip 验证 Do 走的是内部 *http.Client 的高级 Do，请求能真正命中并带回响应体。
func TestDoRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "get-header", r.Header.Get("X-Echo"))
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := NewFetcher()
	resp, err := c.Send(context.Background(), &hijack.RequestCapture{
		Method: http.MethodGet,
		URL:    srv.URL,
		Headers: newNetworkHeaders(map[string]string{
			"X-Echo": "get-header",
		}),
	})
	require.NoError(t, err)
	defer resp.Body.Close()

	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, `{"ok":true}`, string(got))
}

// TestRetry 验证出错时按 retries 重试：前两次触发传输层错误，第三次返回正常响应。
// 注意重试针对的是"连接/传输层失败"（net 层 error），而非服务端返回的 4xx/5xx 状态码。
func TestRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			// 劫持连接并直接断开，制造传输层 error，触发 Fetcher 重试。
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, _ := hj.Hijack()
				_ = conn.Close()
				return
			}
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := NewFetcher(WithRetries(2))
	resp, err := c.Do(mustRequest(t, "GET", srv.URL))
	require.NoError(t, err)
	defer resp.Body.Close()

	got, _ := io.ReadAll(resp.Body)
	assert.Equal(t, "ok", string(got))
	assert.Equal(t, int32(3), calls.Load(), "应恰好请求 3 次（1 次成功 + 2 次重试后成功）")
}

// TestTimeout 验证配置 timeout 后，超时请求会以失败返回而非悬挂。
func TestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()

	c := NewFetcher(WithTimeout(50 * time.Millisecond))
	start := time.Now()
	_, err := c.Do(mustRequest(t, "GET", srv.URL))
	assert.Error(t, err, "超时应返回错误")
	assert.Less(t, time.Since(start), time.Second, "应快速失败而非无限等待")
}

// TestReadBodyWithTimeout 回归验证：配置 timeout 后仍能正常读完响应体。
// 旧实现会在 client.Do 返回后立即 cancel 派生 ctx，导致 WithTimeout 下读 resp.Body 即报
// context canceled；修复后 timeout 落在 http.Client.Timeout，仅约束连接+读体总时长。
func TestReadBodyWithTimeout(t *testing.T) {
	payload := strings.Repeat("x", 64*1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("[" + payload + "]"))
	}))
	defer srv.Close()

	c := NewFetcher(WithTimeout(5 * time.Second))
	resp, err := c.Send(context.Background(), &hijack.RequestCapture{
		Method: http.MethodGet,
		URL:    srv.URL,
	})
	require.NoError(t, err)
	defer resp.Body.Close()

	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err, "读响应体不应因 timeout 被提前取消")
	assert.Equal(t, len(payload)+2, len(got))
}

// mustRequest 构造一个 GET 请求，供不关心 URL 的测试使用。
func mustRequest(t *testing.T, method, url string) *http.Request {
	req, err := http.NewRequestWithContext(context.Background(), method, url, nil)
	require.NoError(t, err)
	return req
}
