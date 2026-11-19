package kitexotel

// 跨包 e2e 集成测试(audit 5.3 闭环):真实 kitex server+client 走完整
// transport 链,验证此前只有单测覆盖的链路:
//   1. kitexmeta   — ekit trace_id 经 metainfo 跨进程透传
//   2. kitexotel   — OTel traceparent 经 metainfo 跨进程透传(v0.25.14 修复后)
//
// BizErrorBridge 链路(原链路 3)已随 #301 删除:kitexotel 无桥接代码,本模块
// 的该用例(#301 前滞留旧口径注释 + 断言兜底 50000)在改配正典形态后与根模块
// bridge_e2e_test.go 的 TestE2E_BizErrorBridge_PostChainTypedBizError
// (WithBizErrorBridgeServer + 类型化 1001)完全重复——正典以根模块单源为准。
//
// 生成代码在 internal/e2e/kitex_gen(用 kitex 工具对 examples/kitex/idl/echo.thrift 真实生成——
// examples/kitex/kitex_gen 是手写桩,不做真 RPC,不能用于 e2e);e2e server
// 启动 helper 单源 internal/e2e.StartEchoServer(#301 收编,替换本地
// startE2EServer 42 行副本)。

import (
	"context"

	"strings"
	"sync/atomic"
	"testing"

	"github.com/bytedance/gopkg/cloud/metainfo"
	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/pkg/endpoint"
	"github.com/cloudwego/kitex/pkg/transmeta"

	"github.com/cloudwego/kitex/server"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/oekit/kitex/kitexmeta"

	e2e "github.com/oekit/kitex/internal/e2e"
	echo "github.com/oekit/kitex/internal/e2e/kitex_gen/kitex/echo"
	echoservice "github.com/oekit/kitex/internal/e2e/kitex_gen/kitex/echo/echoservice"
)

// echoHandler 是 e2e 用最小 handler:原样回显 message。
type echoHandler struct{}

func (h *echoHandler) Echo(_ context.Context, req *echo.EchoRequest) (*echo.EchoResponse, error) {
	return &echo.EchoResponse{Message: req.Message, Timestamp: 1}, nil
}

// TestE2E_KitexMetaPropagation 链路 1(audit 5.3):client 经
// kitexmeta.PropagateClientOption 注入 trace_id → 真实 transport →
// server 端 kitexmeta.TraceIDFromContext 能取回。
func TestE2E_KitexMetaPropagation(t *testing.T) {
	var gotTraceID atomic.Value
	addr := e2e.StartEchoServer(t, &echoHandler{},
		// server 端收 metainfo 需要 transmeta handler
		server.WithMetaHandler(transmeta.MetainfoServerHandler),
		server.WithMiddleware(func(next endpoint.Endpoint) endpoint.Endpoint {
			return func(ctx context.Context, req, resp interface{}) error {
				gotTraceID.Store(kitexmeta.TraceIDFromContext(ctx))
				return next(ctx, req, resp)
			}
		}),
	)

	opts := append([]client.Option{client.WithHostPorts(addr)},
		kitexmeta.PropagateClientOption()...)
	cli, err := echoservice.NewClient("e2e-echo", opts...)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx := kitexmeta.ContextWithTraceID(context.Background(), "e2e-trace-123")
	resp, err := cli.Echo(ctx, &echo.EchoRequest{Message: "ping"})
	if err != nil {
		t.Fatalf("Echo: %v", err)
	}
	if resp.Message != "ping" {
		t.Errorf("resp = %q, want ping", resp.Message)
	}
	if got, _ := gotTraceID.Load().(string); got != "e2e-trace-123" {
		t.Errorf("server got trace_id = %q, want e2e-trace-123 (metainfo 跨进程透传断裂)", got)
	}
}

// TestE2E_OTelTraceparentPropagation 链路 2(audit 5.3):client 带 OTel
// SpanContext → kitexotel.NewTracingClientOptions 注入 traceparent →
// server 端 metainfo 取回,且 trace-id 与 client span 一致。
func TestE2E_OTelTraceparentPropagation(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator()) })
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	var gotTraceparent atomic.Value
	addr := e2e.StartEchoServer(t, &echoHandler{},
		server.WithMetaHandler(transmeta.MetainfoServerHandler),
		server.WithMiddleware(func(next endpoint.Endpoint) endpoint.Endpoint {
			return func(ctx context.Context, req, resp interface{}) error {
				if v, ok := metainfo.GetValue(ctx, "traceparent"); ok {
					gotTraceparent.Store(v)
				}
				return next(ctx, req, resp)
			}
		}),
	)

	opts := append([]client.Option{client.WithHostPorts(addr)},
		NewTracingClientOptions()...)
	cli, err := echoservice.NewClient("e2e-echo", opts...)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	tracer := tp.Tracer("e2e")
	ctx, span := tracer.Start(context.Background(), "e2e-call")
	defer span.End()

	if _, err := cli.Echo(ctx, &echo.EchoRequest{Message: "otel"}); err != nil {
		t.Fatalf("Echo: %v", err)
	}

	got, _ := gotTraceparent.Load().(string)
	want := span.SpanContext().TraceID().String()
	if got == "" {
		t.Fatal("server did not receive traceparent (kitexotel OTel 传播断裂)")
	}
	if !strings.Contains(got, want) {
		t.Errorf("traceparent = %q, want trace-id %q", got, want)
	}
}

// TestE2E_BizErrorBridge(原链路 3)已于 #301 删除:本用例断言滞留旧口径
// (「handler 直返 BizStatusError 被吞 (nil,nil)」「类型化仅 ttstream/gRPC
// 流式支持」——bridge.go 修正说明已宣告为缺 meta handler 时的误判),改配
// 正典形态(WithBizErrorBridgeServer + 断言类型化 1001)后与根模块
// bridge_e2e_test.go 的 TestE2E_BizErrorBridge_PostChainTypedBizError 完全
// 重复,且 kitexotel 无桥接代码——正典 e2e 以根模块单源为准:
// 见 github.com/oekit/kitex 根仓 bridge_e2e_test.go。
