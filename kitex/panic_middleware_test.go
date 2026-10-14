package kitex

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/kitex/pkg/gofunc"
	"github.com/cloudwego/kitex/pkg/kerrors"
	"github.com/cloudwego/kitex/pkg/rpcinfo"

	"github.com/yi-nology/ekit-logx"
)

// captureLogger 捕获 CtxError 调用,其余方法透传给内嵌 Logger。
type captureLogger struct {
	logx.Logger
	mu   sync.Mutex
	msgs []string
	kvs  []map[string]any
}

func (c *captureLogger) CtxError(_ context.Context, msg string, keysAndValues ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := map[string]any{}
	for i := 0; i+1 < len(keysAndValues); i += 2 {
		if k, ok := keysAndValues[i].(string); ok {
			m[k] = keysAndValues[i+1]
		}
	}
	c.msgs = append(c.msgs, msg)
	c.kvs = append(c.kvs, m)
}

func (c *captureLogger) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.msgs)
}

func (c *captureLogger) last() (string, map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.msgs) == 0 {
		return "", nil
	}
	return c.msgs[len(c.msgs)-1], c.kvs[len(c.kvs)-1]
}

func withCapturePanicLog(t *testing.T) *captureLogger {
	t.Helper()
	cap := &captureLogger{Logger: logx.NewStdLogger("test-panic-capture")}
	old := panicLog
	panicLog = cap
	t.Cleanup(func() { panicLog = old })
	return cap
}

// TestNewPanicLogMiddleware_LogsHandlerPanic 验证官方推荐路径:handler panic 被
// kitex 框架 recover 后,middleware 检查 Stats().Panicked() 写 logx 错误日志,
// 且 err 原样透传(不改变 kitex 默认错误语义)。
func TestNewPanicLogMiddleware_LogsHandlerPanic(t *testing.T) {
	cap := withCapturePanicLog(t)

	stats := rpcinfo.NewRPCStats()
	panicDetail := kerrors.ErrPanic.WithCauseAndStack(errors.New("boom in handler"), "fake-stack")
	rpcinfo.AsMutableRPCStats(stats).SetPanicked(panicDetail)

	ri := rpcinfo.NewRPCInfo(
		rpcinfo.EmptyEndpointInfo(),
		rpcinfo.EmptyEndpointInfo(),
		rpcinfo.NewInvocation("test-svc", "Echo"),
		rpcinfo.NewRPCConfig(),
		stats,
	)
	ctx := rpcinfo.NewCtxWithRPCInfo(context.Background(), ri)

	handler := NewPanicLogMiddleware()(func(_ context.Context, _, _ interface{}) error {
		return panicDetail
	})
	gotErr := handler(ctx, nil, nil)
	if !errors.Is(gotErr, kerrors.ErrPanic) {
		t.Fatalf("err should pass through unchanged, got %v", gotErr)
	}
	if cap.count() == 0 {
		t.Fatal("expected panic log to be written")
	}
	msg, kv := cap.last()
	if msg != "kitex handler panic recovered" {
		t.Errorf("unexpected log msg %q", msg)
	}
	if kv["method"] != "Echo" {
		t.Errorf("expected method=Echo in log, got %v", kv["method"])
	}
	if kv["panic"] == nil {
		t.Error("expected panic value in log")
	}
	if kv["stack"] == "" {
		t.Error("expected stack extracted from DetailedError")
	}
}

// TestNewPanicLogMiddleware_PlainErrorNoLog 验证普通业务错误(无 panic 记录)
// 不打 panic 日志,只透传。
func TestNewPanicLogMiddleware_PlainErrorNoLog(t *testing.T) {
	cap := withCapturePanicLog(t)

	stats := rpcinfo.NewRPCStats()
	ri := rpcinfo.NewRPCInfo(
		rpcinfo.EmptyEndpointInfo(),
		rpcinfo.EmptyEndpointInfo(),
		rpcinfo.NewInvocation("test-svc", "Echo"),
		rpcinfo.NewRPCConfig(),
		stats,
	)
	ctx := rpcinfo.NewCtxWithRPCInfo(context.Background(), ri)

	handler := NewPanicLogMiddleware()(func(_ context.Context, _, _ interface{}) error {
		return errors.New("normal biz error")
	})
	gotErr := handler(ctx, nil, nil)
	if gotErr == nil || gotErr.Error() != "normal biz error" {
		t.Fatalf("err should pass through unchanged, got %v", gotErr)
	}
	if cap.count() != 0 {
		t.Errorf("plain error should not produce panic log, got %d", cap.count())
	}
}

// TestNewPanicLogMiddleware_SuccessNoLog 验证成功路径零日志。
func TestNewPanicLogMiddleware_SuccessNoLog(t *testing.T) {
	cap := withCapturePanicLog(t)
	handler := NewPanicLogMiddleware()(func(_ context.Context, _, _ interface{}) error {
		return nil
	})
	if err := handler(context.Background(), nil, nil); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cap.count() != 0 {
		t.Errorf("success path should not log, got %d logs", cap.count())
	}
}

// TestNewPanicLogMiddleware_NilRPCInfoSafe 验证无 rpcinfo 的 ctx 不 panic。
func TestNewPanicLogMiddleware_NilRPCInfoSafe(t *testing.T) {
	withCapturePanicLog(t)
	handler := NewPanicLogMiddleware()(func(_ context.Context, _, _ interface{}) error {
		return kerrors.ErrPanic
	})
	if err := handler(context.Background(), nil, nil); err == nil {
		t.Fatal("expected err passthrough")
	}
}

// TestSetDefaultPanicHandler_CoverageDocGuard AST 守卫:防止文档再次宣称
// SetDefaultPanicHandler 覆盖业务 handler panic(2026-09-05 修正的事实)。
// 业务 handler panic 由 NewPanicLogMiddleware 覆盖,gofunc.SetPanicHandler
// 只覆盖 kitex 内部 goroutine(RecoverGoFuncWithInfo 路径)。
func TestSetDefaultPanicHandler_CoverageDocGuard(t *testing.T) {
	srcBytes, err := os.ReadFile("panic.go")
	if err != nil {
		t.Fatalf("read panic.go: %v", err)
	}
	src := string(srcBytes)
	if !strings.Contains(src, "NewPanicLogMiddleware") {
		t.Error("panic.go must provide NewPanicLogMiddleware for handler panics")
	}
	if strings.Count(src, "gofunc.SetPanicHandler(func") != 1 {
		t.Error("SetDefaultPanicHandler must register exactly one gofunc handler")
	}
}

// TestSetDefaultPanicHandler_GoroutinePath 保留原有行为验证:kitex 内部
// goroutine panic 走 gofunc handler 不 panic。
func TestSetDefaultPanicHandler_GoroutinePath(t *testing.T) {
	SetDefaultPanicHandler()

	info := gofunc.NewBasicInfo("trigger-svc", "10.0.0.1:9999")
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("unexpected panic bubbled: %v", r)
		}
	}()

	gofunc.RecoverGoFuncWithInfo(t.Context(), func() {
		panic("simulated goroutine panic")
	}, info)
}
