package hijack

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRoundTripHandler 验证"请求+响应"同一次回放完整采集：命中请求经注入的 client
// 真实回代，回调 fn 同时拿到请求侧（method/url）与响应侧（状态码/响应头/响应体）；
// 随后处理器放行浏览器原请求，页面仍收到真实响应。
func TestRoundTripHandler(t *testing.T) {
	url, err := launcher.New().Headless(true).Launch()
	require.NoError(t, err)
	browser := rod.New().ControlURL(url)
	require.NoError(t, browser.Connect())
	defer browser.Close()

	// 同一个服务器同时提供页面与接口，"页面 fetch 相对路径"保持同源、避开 CORS。
	const apiBody = `{"hit":true,"data":[1,2,3]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/real-api":
			w.Header().Set("X-Api-Ver", "v9")
			_, _ = w.Write([]byte(apiBody))
		default:
			_, _ = w.Write([]byte(`<!DOCTYPE html><title>pg</title>`))
		}
	}))
	defer srv.Close()

	var (
		mu  sync.Mutex
		got *RoundTripCapture
	)
	router := browser.HijackRequests()
	router.MustAdd("*/real-api*",
		RoundTripHandler(&http.Client{Timeout: 30 * time.Second}, func(c *RoundTripCapture) {
			mu.Lock()
			got = c
			mu.Unlock()
		}))
	go router.Run()

	page, err := browser.Page(proto.TargetCreateTarget{})
	require.NoError(t, err)
	defer page.Close()
	require.NoError(t, page.Navigate(srv.URL))
	require.NoError(t, page.WaitLoad())

	// 页内 fetch 命中同源 /real-api。
	eval, err := page.Eval(`async () => { const r = await fetch('/real-api'); return r.text(); }`)
	require.NoError(t, err, "页面 eval 执行失败")

	// 处理器放行浏览器原请求，页面应读到真实响应体。
	require.Equal(t, apiBody, eval.Value.String(), "页面应收到真实响应体")

	// 等待回调采到往返快照。
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return got != nil
	}, 8*time.Second, 100*time.Millisecond)
	mu.Lock()
	round := got
	mu.Unlock()

	// 请求侧：方法命中、URL 指向后端接口。
	assert.Equal(t, http.MethodGet, round.Request.Method)
	assert.True(t, strings.Contains(round.Request.URL, "/real-api"), "请求 URL 应指向后端接口")

	// 响应侧：状态码、响应体、自定义响应头都应在场。
	assert.Equal(t, http.StatusOK, round.Response.StatusCode)
	assert.True(t, strings.Contains(round.Response.Body, `"hit":true`), "响应体应包含后端数据")
	assert.Equal(t, "v9", round.Response.Headers.Get("X-Api-Ver"), "自定义响应头应被捕获")
}

// TestResponseHandler 验证"仅取响应"：回代命中请求，回调 fn 只拿到响应侧快照，
// 页面仍读到真实响应，证明放行逻辑与其配套工作。
func TestResponseHandler(t *testing.T) {
	url, err := launcher.New().Headless(true).Launch()
	require.NoError(t, err)
	browser := rod.New().ControlURL(url)
	require.NoError(t, browser.Connect())
	defer browser.Close()

	const apiBody = `{"items":["a","b"]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/resp-only":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(apiBody))
		default:
			_, _ = w.Write([]byte(`<!DOCTYPE html><title>pg</title>`))
		}
	}))
	defer srv.Close()

	var (
		mu  sync.Mutex
		res *ResponseCapture
	)
	router := browser.HijackRequests()
	router.MustAdd("*/resp-only*",
		ResponseHandler(&http.Client{Timeout: 30 * time.Second}, func(c *ResponseCapture) {
			mu.Lock()
			res = c
			mu.Unlock()
		}))
	go router.Run()

	page, err := browser.Page(proto.TargetCreateTarget{})
	require.NoError(t, err)
	defer page.Close()
	require.NoError(t, page.Navigate(srv.URL))
	require.NoError(t, page.WaitLoad())

	eval, err := page.Eval(`async () => { const r = await fetch('/resp-only'); return r.text(); }`)
	require.NoError(t, err)
	require.Equal(t, apiBody, eval.Value.String(), "页面应读到真实响应体")

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return res != nil
	}, 8*time.Second, 100*time.Millisecond)
	mu.Lock()
	captured := res
	mu.Unlock()

	assert.Equal(t, http.StatusOK, captured.StatusCode)
	assert.True(t, strings.Contains(captured.Body, `"items"`), "响应体应包含后端数据")
	assert.Equal(t, "application/json", captured.Headers.Get("Content-Type"))
}

// TestHijackSimulateFakeResponse 在真实浏览器里模拟"假响应"能力：
// 注册一个本地 hijack handler，把匹配的 fetch 请求回喂一段假 JSON 给页面，
// 验证浏览器级拦截可以按自定义 handler 构造假响应（原 mock 语义在测试里复现）。
func TestHijackSimulateFakeResponse(t *testing.T) {
	url, err := launcher.New().Headless(true).Launch()
	require.NoError(t, err)
	browser := rod.New().ControlURL(url)
	require.NoError(t, browser.Connect())
	defer browser.Close()

	const fakeBody = `{"mocked":true,"name":"fake"}`
	router := browser.HijackRequests()
	router.MustAdd("*/fake-api*", func(h *rod.Hijack) {
		h.Response.SetHeader("Content-Type", "application/json")
		h.Response.SetBody(fakeBody)
	})
	go router.Run()

	// 同源服务一个空白页，供页内 fetch 命中 */fake-api*。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<!DOCTYPE html><title>pg</title>`))
	}))
	defer srv.Close()

	page, err := browser.Page(proto.TargetCreateTarget{})
	require.NoError(t, err)
	defer page.Close()
	require.NoError(t, page.Navigate(srv.URL))
	require.NoError(t, page.WaitLoad())

	got, err := page.Eval(`async () => { const r = await fetch('/fake-api/users'); return r.text(); }`)
	require.NoError(t, err)
	assert.Equal(t, fakeBody, got.Value.String(), "页内 fetch 应读到被劫持回喂的假响应")
}
