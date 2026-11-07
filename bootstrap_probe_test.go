package kitex

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// TestProbeAndReady_AbortedReturnsSentinel 回归测试（#48，与 hertz 同型）：
// ctx 被取消（正常关停）⇒ 探活必须返回**可判别**的 errProbeAborted。
//
// 旧实现返回的是裸 `errors.New("ready probe aborted: app shutting down")`
// ——调用方拿不到哨兵 ⇒ 只能把所有错误一律当「探活失败」⇒ 关停被误报成
// 启动失败（ERROR + 再次 RequestShutdown）。
func TestProbeAndReady_AbortedReturnsSentinel(t *testing.T) {
	app := newFakeApp()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 已取消

	// started/runDone 传 nil（永不就绪），保证命中 ctx 分支。
	err := probeAndReady(ctx, app, "test-svc", &runServerConfig{
		onReady:      func(context.Context) {},
		probeTimeout: 30 * time.Second,
		probeAddr:    "127.0.0.1:1", // 必然连不上的探活目标
	}, nil, nil)
	if !errors.Is(err, errProbeAborted) {
		t.Fatalf("probeAndReady err = %v，期望 errProbeAborted", err)
	}
}

// TestProbeAndReady_TimeoutIsNotAbort 钉住**既有语义不变**：探活超时仍是
// 启动失败（普通 error），不得被误判成「被关停打断」——否则真正的启动失败
// 会被悄悄放过（不再请求关机 ⇒ 服务假活）。
//
// 新语义下超时最常见的形态是阶段一（server 一直未 started）：fake server
// 不触发 RegisterStartHook，started=nil 永不就绪。
func TestProbeAndReady_TimeoutIsNotAbort(t *testing.T) {
	app := newFakeApp()

	err := probeAndReady(context.Background(), app, "test-svc", &runServerConfig{
		onReady:      func(context.Context) {},
		probeTimeout: 100 * time.Millisecond,
		probeAddr:    "127.0.0.1:1", // 必然连不上的探活目标
	}, nil, nil)
	if err == nil {
		t.Fatal("probeAndReady 在探活目标不可达时必须失败")
	}
	if errors.Is(err, errProbeAborted) {
		t.Fatalf("探活超时不得被报成 abort：%v", err)
	}
}

// TestProbeAndReady_AbortWinsOverTimeout 钉住**关停优先于超时**：ctx 取消与
// 超时同时发生时，返回的必须是 abort（正常关停）而不是 timeout——
// 否则关停恰好落在探活窗口尾部时，仍会被误报成启动失败。
func TestProbeAndReady_AbortWinsOverTimeout(t *testing.T) {
	app := newFakeApp()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := probeAndReady(ctx, app, "test-svc", &runServerConfig{
		onReady:      func(context.Context) {},
		probeTimeout: time.Nanosecond, // 立即超时
		probeAddr:    "127.0.0.1:1",
	}, nil, nil)
	if !errors.Is(err, errProbeAborted) {
		t.Fatalf("关停与超时同时发生时 err = %v，期望 errProbeAborted（关停优先）", err)
	}
}

// TestProbeAndReady_ServerExitedBeforeReady_NoReady 是 #120 的 TOCTOU 回归：
// 端口归属预检通过后、kitex 真正 bind 之前端口被抢占 ⇒ 本进程 server 绑定
// 失败退出（runDone 就绪），此时**即使探活目标可建连**（连上的是对方的
// 监听器），也绝不允许回调 onReady——否则不属于自己的实例被注册到注册中心。
//
// 旧实现（无 started 门/runDone 复核）在「连上即回调」语义下必然误判就绪。
func TestProbeAndReady_ServerExitedBeforeReady_NoReady(t *testing.T) {
	app := newFakeApp()

	// 「他人」的监听器占住探活端口：裸 TCP 探活对它建连必然成功。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	var readyCalled atomic.Int64
	cfg := &runServerConfig{
		onReady:      func(context.Context) { readyCalled.Add(1) },
		probeTimeout: 2 * time.Second,
		probeAddr:    ln.Addr().String(),
	}

	// 场景 A：server 在探活开始前已退出（started 永不就绪、runDone 已关闭）。
	err = probeAndReady(context.Background(), app, "stolen-svc", cfg, nil, closedChan())
	if err == nil {
		t.Fatal("端口被抢占且 server 已退出时，probeAndReady 必须失败")
	}
	if errors.Is(err, errProbeAborted) {
		t.Fatalf("端口被抢占是启动失败，不得报成 abort：%v", err)
	}
	if readyCalled.Load() != 0 {
		t.Fatal("端口被抢占时不得回调 onReady（注册中心会收到不属于自己的实例）")
	}

	// 场景 B：启动钩子已触发（started 就绪）但 server 随即退出（runDone 已
	// 关闭）——两信号同时就绪时无论 select 先命中哪一支，都必须失败、不回调。
	err = probeAndReady(context.Background(), app, "stolen-svc", cfg, closedChan(), closedChan())
	if err == nil {
		t.Fatal("server 已退出时，probeAndReady 必须失败（含建连成功后的复核分支）")
	}
	if readyCalled.Load() != 0 {
		t.Fatal("server 已退出时不得回调 onReady")
	}
}

// TestProbeAndReady_StartedThenConnected_FiresReady 钉住正向路径：started
// 就绪（kitex 已绑定端口进入 serving）+ 建连成功 + runDone 未关闭 ⇒ 回调
// onReady（#120 的复核不得误伤正常就绪）。
func TestProbeAndReady_StartedThenConnected_FiresReady(t *testing.T) {
	app := newFakeApp()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			_ = c.Close()
		}
	}()

	var readyCalled atomic.Int64
	runDone := make(chan struct{}) // 不关闭：server 持续 serving
	err = probeAndReady(context.Background(), app, "ok-svc", &runServerConfig{
		onReady:      func(context.Context) { readyCalled.Add(1) },
		probeTimeout: 2 * time.Second,
		probeAddr:    ln.Addr().String(),
	}, closedChan(), runDone)
	if err != nil {
		t.Fatalf("正常就绪路径不应失败：%v", err)
	}
	if readyCalled.Load() != 1 {
		t.Fatal("started+建连成功时必须回调 onReady")
	}
}

// TestProbeAndReady_NeverStarted_TimesOut 钉住阶段一超时：server 一直未
// started 也未退出（如 svr.Run() 长时间阻塞在启动流程）⇒ 按探活超时处理。
func TestProbeAndReady_NeverStarted_TimesOut(t *testing.T) {
	app := newFakeApp()

	err := probeAndReady(context.Background(), app, "slow-svc", &runServerConfig{
		onReady:      func(context.Context) {},
		probeTimeout: 100 * time.Millisecond,
		probeAddr:    "127.0.0.1:1",
	}, nil, nil)
	if err == nil {
		t.Fatal("server 永不 started 时必须超时失败")
	}
	if errors.Is(err, errProbeAborted) {
		t.Fatalf("超时不得报成 abort：%v", err)
	}
}

// closedChan 返回一个已关闭的 channel（信号「已就绪」）。
func closedChan() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}
