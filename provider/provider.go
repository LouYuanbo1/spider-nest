// Package provider 定义"会话底层资源"的来源契约。
//
// 它是一份独立于任何池实现的协议：本地 Nest、单个 Spider、云端编排后端等异构实现
// 都能实现 WebProvider；上层会话管理器（session.Manager）只依赖本包，
// 从而在不改任何业务逻辑的前提下自由切换底层资源来源。
//
// 设计要点：
//   - 令牌解耦：AcquireWeb 不直接泄漏进程内指针，而返回 WebHandle（Token 可跨 RPC
//     寻址；Web 仅本地/节点内后端非空），从而"会话身份"与"底层活页"彻底解耦；
//   - 调度可见性：Stats() 暴露"容量/占用/余量"，让云编排器只按容量决策；
//   - 优雅排空：Drain() 停止接收新租约、等在途跑完，支撑滚动发布/缩容/故障摘除。
package provider

import (
	"context"
	"errors"

	"github.com/LouYuanbo1/spider-nest/web"
)

// AcquireOptions 表达一次借租的调度诉求。
//
//	Sticky  会话强身份/粘性选择（固定登录态、独享 IP、代理池、会话亲和等）。
//	        有强身份的会话必须落到特定叶节点——编排器据此路由，对应"方案A"。
//	KeepHot 明示需要持续保活（pin）：不参与后续的"冷提交"回收。
type AcquireOptions struct {
	Sticky  map[string]string
	KeepHot bool
}

// WebHandle 借租的结果：把"跨链路可寻址的令牌"与"本进程内的具体页"绑定。
//
//	Token   可寻址令牌：跨 RPC 交给云端编排器/远端节点；
//	Web     本进程内的页：仅"本地/节点内后端"非空，远端后端恒为 nil；
//	Release 归还闭包：把网还回所属浏览器、再把浏览器还回池；幂等，可安全重复调用。
type WebHandle struct {
	Token   string
	Web     *web.Web
	Release func() error
}

// PoolState 是池占用的一次只读快照，供云编排器"只看容量"地做放置决策。
type PoolState struct {
	Capacity  int
	InUse     int
	Available int
	Ready     bool // 是否可服务新租约（Drain 中 / 已关停时=false）
}

// 错误分类：调度侧据此决定重试 / 退避 / 改派 / 放弃。
var (
	// ErrPoolExhausted 池满：请求可稍后重试或由调度扩容。
	ErrPoolExhausted = errors.New("resource: 池已满,稍后重试或扩容")
	// ErrUnavailable 后端暂不可用（排空/关停中），应改派其它节点。
	ErrUnavailable = errors.New("resource: provider 暂不可用")
	// ErrTokenUnknown 令牌未知或已过期：不可盲目重试。
	ErrTokenUnknown = errors.New("resource: 令牌未知或已过期")
)

// WebProvider 是"会话底层资源"的来源契约。
//
// 设计注记：返回 WebHandle 而非裸 *web.Web，正是为了"编排层与底层资源来源彻底解耦"——
// 编排器眼里只有各节点剩余容量（Stats），实现层眼里只有本地蜘蛛/浏览器。本地 Nest、
// 单个 Spider、云端远端后端只需各自实现本接口，即可透明切换，会话管理器的其余逻辑零改动。
type WebProvider interface {
	// AcquireWeb 借一个逻辑会话资源（可能含强身份绑定），按 opts 路由。
	// 池满 / 排空时返回 ErrPoolExhausted / ErrUnavailable（本地池满可阻塞等待）。
	AcquireWeb(ctx context.Context, opts AcquireOptions) (*WebHandle, error)
	// Heartbeat 让"保活中的令牌"续期，避免长任务被误回收。远端后端必须实现。
	Heartbeat(ctx context.Context, token string) error
	// Stats 返回当前占用快照，供调度/负载均衡读取。
	Stats() PoolState
	// Drain 停止接收新租约并等在途租约归还完毕，供滚动发布/缩容/故障摘除。
	Drain(ctx context.Context) error
	// Close 硬关停：释放全部底层资源（浏览器进程等）。
	Close() error
}
