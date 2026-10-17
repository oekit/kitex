package kitexmeta

import (
	"context"
	"errors"
	"testing"

	"github.com/bytedance/gopkg/cloud/metainfo"
	"github.com/cloudwego/kitex/pkg/endpoint"
)

// fakeEndpoint 是 middleware 链的下一跳,记录传过来的 ctx 用于断言 metainfo 注入。
type fakeEndpoint struct {
	gotCtx context.Context
	called bool
}

func (f *fakeEndpoint) invoke(ctx context.Context, req, resp interface{}) error {
	f.gotCtx = ctx
	f.called = true
	// 模拟成功返回
	return nil
}

// TestPropagateMiddlewareBuilder_DefaultExtractor 验证默认 extractor 注入 trace_id。
func TestPropagateMiddlewareBuilder_DefaultExtractor(t *testing.T) {
	// 1. 构造上游 ctx(业务用 ContextWithTraceID 注入)
	ctx := ContextWithTraceID(context.Background(), "trace-12345")

	// 2. 构造 builder 链
	builder := PropagateMiddlewareBuilder()
	mw := builder(ctx)
	fake := &fakeEndpoint{}
	wrapped := mw(fake.invoke)
	if wrapped == nil {
		t.Fatal("middleware returned nil endpoint")
	}

	// 3. 调用 wrapped endpoint
	if err := wrapped(ctx, "req", "resp"); err != nil {
		t.Fatalf("wrapped endpoint returned err: %v", err)
	}
	if !fake.called {
		t.Fatal("fake endpoint not called")
	}

	// 4. 断言 fake.gotCtx 里有 "x-trace-id" metainfo
	if v, ok := metainfo.GetPersistentValue(fake.gotCtx, "x-trace-id"); !ok || v != "trace-12345" {
		t.Errorf("metainfo x-trace-id = (%q, %v), want (trace-12345, true)", v, ok)
	}
}

// TestPropagateMiddlewareBuilder_NoTraceID 验证 ctx 里无 trace_id 时不注入(不 panic)。
func TestPropagateMiddlewareBuilder_NoTraceID(t *testing.T) {
	ctx := context.Background() // 无 trace_id
	builder := PropagateMiddlewareBuilder()
	mw := builder(ctx)
	fake := &fakeEndpoint{}
	wrapped := mw(fake.invoke)
	if err := wrapped(ctx, "req", "resp"); err != nil {
		t.Fatalf("err: %v", err)
	}
	// 验证 ctx 里确实没有 "x-trace-id"
	if _, ok := metainfo.GetPersistentValue(fake.gotCtx, "x-trace-id"); ok {
		t.Error("metainfo x-trace-id should NOT be set when ctx has no trace_id")
	}
}

// TestWithMetaKeyExtractors_CustomExtractors 验证自定义 extractor 列表。
func TestWithMetaKeyExtractors_CustomExtractors(t *testing.T) {
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "alice")

	userIDExtractor := func(c context.Context) (string, string, bool) {
		if v, ok := c.Value(ctxKey{}).(string); ok && v != "" {
			return "x-user-id", v, true
		}
		return "", "", false
	}

	builder := WithMetaKeyExtractors(userIDExtractor)
	mw := builder(context.Background())
	fake := &fakeEndpoint{}
	wrapped := mw(fake.invoke)
	if err := wrapped(ctx, "req", "resp"); err != nil {
		t.Fatalf("err: %v", err)
	}
	if v, ok := metainfo.GetPersistentValue(fake.gotCtx, "x-user-id"); !ok || v != "alice" {
		t.Errorf("x-user-id = (%q, %v), want (alice, true)", v, ok)
	}
}

// TestWithMetaKeyExtractors_MultipleExtractors 验证多个 extractor 都生效。
func TestWithMetaKeyExtractors_MultipleExtractors(t *testing.T) {
	type keyA struct{}
	type keyB struct{}
	ctx := context.WithValue(context.Background(), keyA{}, "alpha")
	ctx = context.WithValue(ctx, keyB{}, "beta")

	aEx := func(c context.Context) (string, string, bool) {
		if v, ok := c.Value(keyA{}).(string); ok {
			return "x-a", v, true
		}
		return "", "", false
	}
	bEx := func(c context.Context) (string, string, bool) {
		if v, ok := c.Value(keyB{}).(string); ok {
			return "x-b", v, true
		}
		return "", "", false
	}

	builder := WithMetaKeyExtractors(aEx, bEx)
	mw := builder(context.Background())
	fake := &fakeEndpoint{}
	wrapped := mw(fake.invoke)
	if err := wrapped(ctx, "req", "resp"); err != nil {
		t.Fatalf("err: %v", err)
	}
	if v, _ := metainfo.GetPersistentValue(fake.gotCtx, "x-a"); v != "alpha" {
		t.Errorf("x-a = %q, want alpha", v)
	}
	if v, _ := metainfo.GetPersistentValue(fake.gotCtx, "x-b"); v != "beta" {
		t.Errorf("x-b = %q, want beta", v)
	}
}

// TestPropagateMiddlewareBuilder_NextErrorPropagated 验证 next 错误透传。
func TestPropagateMiddlewareBuilder_NextErrorPropagated(t *testing.T) {
	ctx := ContextWithTraceID(context.Background(), "trace-err")
	wantErr := errors.New("fake endpoint error")
	next := func(_ context.Context, _, _ interface{}) error { return wantErr }

	builder := PropagateMiddlewareBuilder()
	mw := builder(ctx)
	wrapped := mw(next)
	err := wrapped(ctx, "req", "resp")
	if err != wantErr {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
}

// TestPropagateClientOption_NotEmpty 验证 PropagateClientOption 返回非空 options 列表。
func TestPropagateClientOption_NotEmpty(t *testing.T) {
	opts := PropagateClientOption()
	if len(opts) == 0 {
		t.Fatal("PropagateClientOption returned empty list")
	}
}

// TestPropagateClientOption_WithCustomExtractors 验证带 extractor 的便捷函数。
func TestPropagateClientOption_WithCustomExtractors(t *testing.T) {
	ex := func(_ context.Context) (string, string, bool) { return "", "", false }
	opts := PropagateClientOption(ex)
	if len(opts) == 0 {
		t.Fatal("PropagateClientOption with extractors returned empty list")
	}
}

// TestTraceIDFromContext_EmptyWhenNotSet 验证空 ctx 返回空字符串。
func TestTraceIDFromContext_EmptyWhenNotSet(t *testing.T) {
	if v := TraceIDFromContext(context.Background()); v != "" {
		t.Errorf("TraceIDFromContext = %q, want empty", v)
	}
}

// TestContextWithTraceID_ThenRead 验证 ContextWithTraceID + TraceIDFromContext 闭环。
func TestContextWithTraceID_ThenRead(t *testing.T) {
	ctx := ContextWithTraceID(context.Background(), "trace-xyz")
	if v := TraceIDFromContext(ctx); v != "trace-xyz" {
		t.Errorf("TraceIDFromContext = %q, want trace-xyz", v)
	}
}

// TestMetaKeyExtractor_TypeAlias 验证 type alias 编译期兼容。
func TestMetaKeyExtractor_TypeAlias(t *testing.T) {
	var _ MetaKeyExtractor = func(_ context.Context) (string, string, bool) { return "", "", false }
	// 编译期检查通过
	_ = endpoint.Endpoint(nil)
}
