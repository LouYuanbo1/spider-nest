// Package provider 定义"会话底层资源"的来源契约。
//
// 它是一份独立于任何池实现的协议：本地 Nest、单个 Spider 等异构实现都能实现
// WebProvider；上层会话管理器（session.Manager）只依赖本包，从而在不改任何业务
// 逻辑的前提下自由切换底层资源来源。
//
// 设计要点：
//   - 归还可闭包化：AcquireWeb 不泄漏进程内指针，而返回 WebHandle —— 把"借到的活页"
//     与"归还闭包"打包成一个自包含句柄，资源借还得力闭环、不依赖调用方自觉归还；
//   - 优雅排空：Drain() 停止接收新租约、等在途跑完，支撑滚动发布/缩容/故障摘除。
package provider

import (
	"context"
	"errors"

	"github.com/LouYuanbo1/spider-nest/web"
)

// WebHandle 借租的结果：把"能执行动作的活页"与其"归还闭包"打包在一起。
//
//	Web     可执行动作的活页门面（web.Web 契约）：本地后端为真实 *web.Page；
//	Release 归还闭包：把网还回所属浏览器、再把浏览器还回池；幂等，可安全重复调用。
type WebHandle struct {
	Web     web.Web
	Release func() error
}

// 错误分类：调度侧据此决定重试 / 退避 / 放弃。
var (
	// ErrPoolExhausted 池满：请求可稍后重试或扩容。
	ErrPoolExhausted = errors.New("resource: 池已满,稍后重试或扩容")
	// ErrUnavailable 后端暂不可用（排空/关停中）。
	ErrUnavailable = errors.New("resource: provider 暂不可用")
)

// WebProvider 是"会话底层资源"的来源契约。
//
// 设计注记：返回 WebHandle 而非裸 *web.Page，正是为了"编排层与底层资源来源彻底解耦"——
// 借还打包在一个句柄里，实现方负责资源配置与归还，消费方只按契约用活页。本地 Nest、
// 单个 Spider 只需各自实现本接口，即可透明切换，会话管理器的其余逻辑零改动。
type WebProvider interface {
	// AcquireWeb 借一个逻辑会话资源，池满/排空时返回 ErrPoolExhausted / ErrUnavailable。
	AcquireWeb(ctx context.Context) (*WebHandle, error)
	// Drain 停止接收新租约并等在途租约归还完毕，供滚动发布/缩容/故障摘除。
	Drain(ctx context.Context) error
	// Close 硬关停：释放全部底层资源（浏览器进程等）。
	Close() error
}
