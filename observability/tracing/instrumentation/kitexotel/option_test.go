package kitexotel

import (
	"context"
	"testing"

	"github.com/bytedance/gopkg/cloud/metainfo"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// TestNewClientOptions_ReturnsMetaHandler 验证 NewClientOptions 含
// metainfo handler(不空,>=1 个 Option)。
func TestNewClientOptions_ReturnsMetaHandler(t *testing.T) {
	opts := NewClientOptions()
	if len(opts) == 0 {
		t.Fatal("NewClientOptions returned empty slice")
	}
}

// TestNewServerOptions_ReturnsMetaHandler 验证 NewServerOptions 含 metainfo handler。
func TestNewServerOptions_ReturnsMetaHandler(t *testing.T) {
	opts := NewServerOptions()
	if len(opts) == 0 {
		t.Fatal("NewServerOptions returned empty slice")
	}
}

// TestNewTracingClientOptions_AddsOTelMiddleware 验证 NewTracingClientOptions
// 比 NewClientOptions 多 2 个 Option(OTel middleware builder + TTHeader 传输协议,
// 后者为 metainfo 上线的必要条件,v0.26.1 起)。
func TestNewTracingClientOptions_AddsOTelMiddleware(t *testing.T) {
	base := NewClientOptions()
	withTrace := NewTracingClientOptions()
	if len(withTrace) != len(base)+2 {
		t.Errorf("NewTracingClientOptions len = %d, want %d (base+2: OTel builder + TTHeader)",
			len(withTrace), len(base)+2)
	}
}

// TestMetainfoCarrier_TextMapCarrier 验证 metainfoCarrier 实现
// propagation.TextMapCarrier 完整接口。
func TestMetainfoCarrier_TextMapCarrier(t *testing.T) {
	c := metainfoCarrier{values: make(map[string]string)}
	c.Set("traceparent", "00-aaa-bbb-01")
	c.Set("tracestate", "k=v")
	if c.Get("traceparent") != "00-aaa-bbb-01" {
		t.Errorf("Get traceparent = %q, want %q", c.Get("traceparent"), "00-aaa-bbb-01")
	}
	keys := c.Keys()
	if len(keys) != 2 {
		t.Errorf("Keys len = %d, want 2", len(keys))
	}
}

// TestOTelClientMWBuilder_InjectsTraceparent 验证 OTel client middleware
// 真实执行时把 traceparent 写进 ctx metainfo(metainfo.WithValue)。
//
// 行为级测试:构造带 SpanContext 的 ctx → 跑 middleware → next 收到的 ctx
// 必须能通过 metainfo.GetValue 取到 traceparent。占位实现(直接 return ctx)
// 会让本测试失败 — 钉住 2026-09-05 修复的 no-op 回归。
func TestOTelClientMWBuilder_InjectsTraceparent(t *testing.T) {
	// 安装 W3C TraceContext propagator
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator()) })

	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	tracer := tp.Tracer("test")

	spanCtx0, span := tracer.Start(context.Background(), "test-span")
	defer span.End()

	var nextCtx context.Context
	mw := otelClientMWBuilder(context.Background())
	handler := mw(func(ctx context.Context, req, resp interface{}) error {
		nextCtx = ctx
		return nil
	})
	if err := handler(spanCtx0, nil, nil); err != nil {
		t.Fatalf("middleware returned error: %v", err)
	}

	if nextCtx == nil {
		t.Fatal("next endpoint was not invoked")
	}
	got, ok := metainfo.GetValue(nextCtx, "traceparent")
	if !ok || got == "" {
		t.Fatal("traceparent not found in ctx metainfo — OTel 传播是 no-op(metainfo.WithValue 未生效)")
	}
	want := trace.SpanContextFromContext(spanCtx0).TraceID().String()
	if traceIDFromTraceparent(got) != want {
		t.Errorf("traceparent trace-id = %q, want %q", traceIDFromTraceparent(got), want)
	}
}

// traceIDFromTraceparent 提取 traceparent 字符串的 32 字符 trace-id。
// 格式: "00-{traceid(32)}-{spanid(16)}-{flags(2)}"
func traceIDFromTraceparent(traceparent string) string {
	if len(traceparent) < 55 {
		return ""
	}
	return traceparent[3:35]
}
