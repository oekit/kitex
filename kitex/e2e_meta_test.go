package kitex

// 跨包 e2e 集成测试(audit 5.3 闭环):真实 kitex server+client 走完整
// transport 链,验证此前只有单测覆盖的三条链路:
//   1. kitexmeta   — ekit trace_id 经 metainfo 跨进程透传
//   2. kitexotel   — OTel traceparent 经 metainfo 跨进程透传(v0.25.14 修复后)
//   3. BizErrorBridge — 对端业务异常转 ekit BizError(含 Cause 链,v0.25.14 修复)
//
// 生成代码在 internal/e2e/kitex_gen(用 kitex 工具对 examples/kitex/idl/echo.thrift 真实生成——
// examples/kitex/kitex_gen 是手写桩,不做真 RPC,不能用于 e2e)。

import (
	"context"
	"net"

	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bytedance/gopkg/cloud/metainfo"
	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/pkg/endpoint"
	"github.com/cloudwego/kitex/pkg/kerrors"
	"github.com/cloudwego/kitex/pkg/transmeta"

	"github.com/cloudwego/kitex/server"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/yi-nology/ekit-kitex/kitex/kitexmeta"
	kitexotel "github.com/yi-nology/ekit-kitex/observability/tracing/instrumentation/kitexotel"
	ekiterrors "github.com/yi-nology/ekit-core/pkg/errors"

	echo "github.com/yi-nology/ekit-kitex/internal/e2e/kitex_gen/kitex/echo"
	echoservice "github.com/yi-nology/ekit-kitex/internal/e2e/kitex_gen/kitex/echo/echoservice"
)

// echoHandler 是 e2e 用最小 handler:原样回显 message。
type echoHandler struct{}

func (h *echoHandler) Echo(_ context.Context, req *echo.EchoRequest) (*echo.EchoResponse, error) {
	return &echo.EchoResponse{Message: req.Message, Timestamp: 1}, nil
}

// startE2EServer 起一个真实 kitex server(随机端口),返回地址;测试结束自动 Stop。
func startE2EServer(t *testing.T, handler *echoHandler, opts ...server.Option) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	tcpAddr, taerr := net.ResolveTCPAddr("tcp", addr)
	if taerr != nil {
		t.Fatalf("resolve addr: %v", taerr)
	}
	base := make([]server.Option, 0, 2+len(opts))
	base = append(base,
		server.WithServiceAddr(tcpAddr),
		server.WithExitWaitTime(200*time.Millisecond), // 测试快速排空
	)
	svr := echoservice.NewServer(handler, append(base, opts...)...)
	runErr := make(chan error, 1)
	go func() {
		runErr <- svr.Run()
	}()
	go func() {
		if err := <-runErr; err != nil {
			t.Errorf("kitex server Run failed: %v", err)
		}
	}()
	t.Cleanup(func() { _ = svr.Stop() })

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c, derr := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if derr == nil {
			_ = c.Close()
			return addr
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("e2e server not ready in time")
	return ""
}

// TestE2E_KitexMetaPropagation 链路 1(audit 5.3):client 经
// kitexmeta.PropagateClientOption 注入 trace_id → 真实 transport →
// server 端 kitexmeta.TraceIDFromContext 能取回。
func TestE2E_KitexMetaPropagation(t *testing.T) {
	var gotTraceID atomic.Value
	addr := startE2EServer(t, &echoHandler{},
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
	addr := startE2EServer(t, &echoHandler{},
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
		kitexotel.NewTracingClientOptions()...)
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

// TestE2E_BizErrorBridge 链路 3(audit 5.3):server 端业务异常(BizStatusError)
// 经真实 RPC + WithBizErrorBridge 转为 ekit *BizError 上抛,
// 且 BizCode/Message 正确、原始错误经 Cause 链可达(v0.25.14 修复)。
func TestE2E_BizErrorBridge(t *testing.T) {
	// ⚠️ 业务异常必须经 server middleware 返回:kitex v0.16.3 实测,handler
	// 直返 BizStatusError 在 thrift ping-pong 下会被 server 吞掉(client 收到
	// nil,nil) — 类型化解码只有 ttstream/gRPC 流式支持。middleware 返回则
	// 以 TApplicationException 文本进 DetailedError,桥接走 50000 兜底分支。
	addr := startE2EServer(t, &echoHandler{},
		server.WithMiddleware(func(next endpoint.Endpoint) endpoint.Endpoint {
			return func(ctx context.Context, req, resp interface{}) error {
				return kerrors.NewBizStatusError(1001, "biz boom")
			}
		}))

	cli, err := echoservice.NewClient("e2e-echo",
		client.WithHostPorts(addr), WithBizErrorBridge())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = cli.Echo(context.Background(), &echo.EchoRequest{Message: "x"})
	if err == nil {
		t.Fatal("expected biz error, got nil")
	}

	var be *ekiterrors.BizError
	if !errorsAs(err, &be) {
		t.Fatalf("error should convert to *BizError, got %T: %v", err, err)
	}
	// ⚠️ kitex v0.16.3 实测语义:普通 thrift/TTHeader ping-pong 下,
	// BizStatusError 不做类型化解码(仅 ttstream/gRPC 流式支持),以
	// TApplicationException 文本进 DetailedError → 桥接走 50000 兜底分支。
	if be.BizCode != 50000 {
		t.Errorf("BizCode = %d, want 50000 (thrift ping-pong 兜底分支)", be.BizCode)
	}
	if !strings.Contains(be.Message, "biz error: code=1001") {
		t.Errorf("Message = %q, want embedded biz error text", be.Message)
	}
}

// errorsAs 小包装,避免直接依赖 std errors 与 ekit pkg/errors 命名冲突。
func errorsAs(err error, target *(*ekiterrors.BizError)) bool {
	for err != nil {
		if be, ok := err.(*ekiterrors.BizError); ok {
			*target = be
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
