package provider_test

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LouYuanbo1/spider-nest/action"
	"github.com/LouYuanbo1/spider-nest/cloud/agent"
	"github.com/LouYuanbo1/spider-nest/provider"
	"github.com/LouYuanbo1/spider-nest/web"
)

// 本文件验证 Route B 的最小闭环：线程内起一个 agent（节点）→ 会话侧 RemoteProvider
// 借活 + 跑动作 + 抓检查点 + 归还。全程不启动真实浏览器,用假活页/假池取代节点内部资源,
// 只验证"数量池 + 令牌租约 + net/rpc+gob + 心跳 + 释放"这条编排链路。
//
// 用外部测试包 provider_test：节点侧 cloud/agent import provider,若本测试用内部包
// provider 则会形成 测试→agent→provider→测试 的环,故独立成包。

// fakePage 一张假的远端活页,记录动作并在检查点上留痕。
type fakePage struct {
	captured atomic.Bool
}

func (f *fakePage) RunAction(_ context.Context, a action.Action) (action.ActionResult, error) {
	return f.run(a)
}

func (f *fakePage) RunActions(_ context.Context, acts []action.Action) ([]action.ActionResult, error) {
	out := make([]action.ActionResult, 0, len(acts))
	for _, a := range acts {
		r, err := f.run(a)
		if err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, nil
}

func (f *fakePage) run(a action.Action) (action.ActionResult, error) {
	switch v := a.(type) {
	case *action.Navigate:
		return action.ActionResult{Action: "navigate", URL: v.URL, Effective: true}, nil
	default:
		return action.ActionResult{Effective: false}, nil
	}
}

func (f *fakePage) CaptureCheckpoint(_ context.Context) (*web.Checkpoint, error) {
	f.captured.Store(true)
	return &web.Checkpoint{}, nil
}

func (f *fakePage) RestoreCheckpoint(_ context.Context, _ *web.Checkpoint) error { return nil }

// fakeProvider 模拟节点内部资源池：capacity 个槽位,借出即记账,归还即减账。
type fakeProvider struct {
	capacity int
	leases   atomic.Int32
}

func (f *fakeProvider) AcquireWeb(_ context.Context, _ provider.AcquireOptions) (*provider.WebHandle, error) {
	if f.leases.Load() >= int32(f.capacity) {
		return nil, provider.ErrPoolExhausted
	}
	f.leases.Add(1)
	var released atomic.Bool
	release := func() error {
		if released.CompareAndSwap(false, true) {
			f.leases.Add(-1)
		}
		return nil
	}
	return &provider.WebHandle{Token: "fake-local", Web: &fakePage{}, Release: release}, nil
}

func (f *fakeProvider) Heartbeat(_ context.Context, _ string) error { return nil }

func (f *fakeProvider) Stats() provider.PoolState {
	in := int(f.leases.Load())
	return provider.PoolState{Capacity: f.capacity, InUse: in, Available: f.capacity - in, Ready: true}
}

func (f *fakeProvider) Drain(_ context.Context) error { return nil }
func (f *fakeProvider) Close() error                  { return nil }

// TestRemoteProviderRoundTrip 打通 借→跑→抓检查点→归还 全链路。
func TestRemoteProviderRoundTrip(t *testing.T) {
	node := agent.New(&fakeProvider{capacity: 2}, agent.Config{})
	defer node.Close()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	defer lis.Close()
	go func() { _ = node.Serve(lis) }()

	p := provider.NewRemoteProvider([]string{lis.Addr().String()}, 100*time.Millisecond)
	defer p.Close()

	// 借活
	ctx := context.Background()
	h, err := p.AcquireWeb(ctx, provider.AcquireOptions{})
	if err != nil {
		t.Fatalf("借活失败: %v", err)
	}
	if h.Token == "" {
		t.Fatal("借活应返回非空令牌")
	}
	if h.Web == nil {
		t.Fatal("远端活页代理不应为 nil")
	}
	if st := p.Stats(); st.InUse != 1 {
		t.Fatalf("借活后应 1 张在途,得到 %d", st.InUse)
	}

	// 跑动作(跨 RPC 的 gob 往返)
	res, err := h.Web.RunAction(ctx, &action.Navigate{URL: "https://example.com"})
	if err != nil {
		t.Fatalf("远端 RunAction 失败: %v", err)
	}
	if !res.Effective || res.URL != "https://example.com" {
		t.Fatalf("结果不符: %+v", res)
	}

	// 抓检查点
	cp, err := h.Web.CaptureCheckpoint(ctx)
	if err != nil {
		t.Fatalf("远端 Capture 失败: %v", err)
	}
	if cp == nil {
		t.Fatal("检查点应为非空")
	}

	// 归还:池应回落到 0
	if err := h.Release(); err != nil {
		t.Fatalf("归还失败: %v", err)
	}
	if st := p.Stats(); st.InUse != 0 {
		t.Fatalf("归还后应 0 在途,得到 %d", st.InUse)
	}

	// 幂等归还:再调不报错、不再扣账
	if err := h.Release(); err != nil {
		t.Fatalf("幂等归还失败: %v", err)
	}
	if st := p.Stats(); st.InUse != 0 {
		t.Fatalf("二次归还后应仍 0 在途,得到 %d", st.InUse)
	}
}

// TestRemoteProviderSignalStaleToken 验证"会话侧归还后心跳即失效"这一边界：
// 心跳只对在途令牌有效,已归还令牌应返回令牌未知错误。
func TestRemoteProviderSignalStaleToken(t *testing.T) {
	node := agent.New(&fakeProvider{capacity: 2}, agent.Config{})
	defer node.Close()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	defer lis.Close()
	go func() { _ = node.Serve(lis) }()

	p := provider.NewRemoteProvider([]string{lis.Addr().String()}, 60*time.Second)
	defer p.Close()

	h, err := p.AcquireWeb(context.Background(), provider.AcquireOptions{})
	if err != nil {
		t.Fatalf("借活失败: %v", err)
	}
	if err := h.Release(); err != nil {
		t.Fatalf("归还失败: %v", err)
	}
	// 节点侧该令牌已被释放,心跳应传导到节点并被判为令牌未知
	if err := p.Heartbeat(context.Background(), h.Token); err == nil {
		t.Fatalf("期望对已归还令牌心跳报错,却成功")
	}
}
