package kitex

import (
	"context"
	"errors"
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

	err := probeAndReady(ctx, app, "test-svc", &runServerConfig{
		onReady:      func(context.Context) {},
		probeTimeout: 30 * time.Second,
		probeAddr:    "127.0.0.1:1", // 必然连不上的探活目标
	})
	if !errors.Is(err, errProbeAborted) {
		t.Fatalf("probeAndReady err = %v，期望 errProbeAborted", err)
	}
}

// TestProbeAndReady_TimeoutIsNotAbort 钉住**既有语义不变**：探活超时仍是
// 启动失败（普通 error），不得被误判成「被关停打断」——否则真正的启动失败
// 会被悄悄放过（不再请求关机 ⇒ 服务假活）。
func TestProbeAndReady_TimeoutIsNotAbort(t *testing.T) {
	app := newFakeApp()

	err := probeAndReady(context.Background(), app, "test-svc", &runServerConfig{
		onReady:      func(context.Context) {},
		probeTimeout: 100 * time.Millisecond,
		probeAddr:    "127.0.0.1:1", // 必然连不上的探活目标
	})
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
	})
	if !errors.Is(err, errProbeAborted) {
		t.Fatalf("关停与超时同时发生时 err = %v，期望 errProbeAborted（关停优先）", err)
	}
}
