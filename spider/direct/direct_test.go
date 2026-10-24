package direct

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/LouYuanbo1/spider-nest/spider/hijack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeHeaderSource 假装"从浏览器偷到头"：每调用一次就返回一条携带测试服务 URL 的请求头快照。
type fakeHeaderSource struct {
	calls atomic.Int32
	url   string
}

func (f *fakeHeaderSource) Capture(_ context.Context, t Target) (*hijack.RequestCapture, error) {
	f.calls.Add(1)
	return &hijack.RequestCapture{
		Method:  t.method(),
		URL:     t.URL,
		Headers: nil,
	}, nil
}

// statusServer 前 n 次请求返回 want 状态码，之后返回 200。
func statusServer(n int32, want int) (string, *atomic.Int32) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) <= n {
			w.WriteHeader(want)
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
	}))
	url := srv.URL
	return url, &hits
}

func TestFetch_DirectSuccessWithSeededHeader(t *testing.T) {
	url, _ := statusServer(0, 403)
	acq := &fakeHeaderSource{url: url}
	client := New(NewFetcher(), acq)

	target := Target{URL: url}
	// 已由外部"监听一次"拿到头，seed 进去，走直连应一次成功，且无需回退偷头。
	client.Seed(target, &hijack.RequestCapture{Method: "GET", URL: url})

	resp, err := client.Fetch(context.Background(), target)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, int32(0), acq.calls.Load(), "命中缓存时应不触发回退偷头")
}

func TestFetch_RefreshOn403ThenSuccess(t *testing.T) {
	url, _ := statusServer(1, 403) // 第一次返回 403，之后 200
	acq := &fakeHeaderSource{url: url}
	client := New(NewFetcher(), acq, WithMaxRetries(1))

	resp, err := client.Fetch(context.Background(), Target{URL: url})
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, int32(2), acq.calls.Load(), "首次 403 应触发一次回退偷新头")
}

func TestFetch_ExhaustRetriesReturnsError(t *testing.T) {
	url, hits := statusServer(1<<30, 403) // 一直 403
	acq := &fakeHeaderSource{url: url}
	client := New(NewFetcher(), acq, WithMaxRetries(1))

	_, err := client.Fetch(context.Background(), Target{URL: url})
	require.Error(t, err)
	// attempt0 + attempt1（maxRetries=1）共两次偷头与直连。
	assert.Equal(t, int32(2), acq.calls.Load())
	assert.Equal(t, int32(2), hits.Load())
}

func TestFetch_NoSourceAndNoCacheReturnsError(t *testing.T) {
	client := New(NewFetcher(), nil)
	_, err := client.Fetch(context.Background(), Target{URL: "http://127.0.0.1:1/x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "未配置 HeaderSource")
}

func TestFetch_StatusRefreshDisabledLeaves403(t *testing.T) {
	url, hits := statusServer(1<<30, 403)
	acq := &fakeHeaderSource{url: url}
	client := New(NewFetcher(), acq, WithRefreshStatuses()) // 关闭状态码刷新

	resp, err := client.Fetch(context.Background(), Target{URL: url})
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 403, resp.StatusCode, "关闭刷新后 403 应原样返回")
	assert.Equal(t, int32(1), hits.Load(), "只直连一次即返回")
}
