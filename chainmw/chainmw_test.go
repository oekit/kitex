package chainmw

import (
	"context"
	"errors"
	"testing"

	"github.com/bytedance/gopkg/cloud/metainfo"
	"github.com/cloudwego/kitex/pkg/rpcinfo"
	ktransport "github.com/cloudwego/kitex/transport"
	"github.com/oekit/core/chain"
	"github.com/oekit/core/transport"
	"github.com/oekit/kitex/transportx"
)

func TestToKitexRespOutParamAndError(t *testing.T) {
	type echoResp struct{ Msg string }

	// 模拟 kitex 出参形态的 next：写 resp、返回错误
	wantErr := errors.New("rpc-fail")
	next := kitexEndpoint(func(_ context.Context, req, resp any) error {
		r := resp.(*echoResp)
		r.Msg = "hello"
		return wantErr
	})

	var chainSawReq any
	h := ToKitex(func(next chain.Handler) chain.Handler {
		return func(ctx context.Context, req any) (any, error) {
			chainSawReq = req
			return next(ctx, req)
		}
	})(next)

	resp := &echoResp{}
	err := h(context.Background(), "req-in", resp)
	if err == nil || err.Error() != "rpc-fail" {
		t.Fatalf("err = %v, want rpc-fail", err)
	}
	if resp.Msg != "hello" {
		t.Fatalf("resp not populated: %+v", resp)
	}
	if chainSawReq != "req-in" {
		t.Fatalf("chain req = %v", chainSawReq)
	}
}

func TestToKitexSuccess(t *testing.T) {
	type pong struct{ N int }
	next := kitexEndpoint(func(_ context.Context, _ any, resp any) error {
		resp.(*pong).N = 7
		return nil
	})
	h := ToKitex(chain.Recovery())(next)
	resp := &pong{}
	if err := h(context.Background(), nil, resp); err != nil {
		t.Fatalf("err = %v", err)
	}
	if resp.N != 7 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestToKitexRecoversPanic(t *testing.T) {
	next := kitexEndpoint(func(context.Context, any, any) error {
		panic("boom")
	})
	h := ToKitex(chain.Recovery())(next)
	err := h(context.Background(), nil, &struct{}{})
	var pe *chain.PanicError
	if !errors.As(err, &pe) || pe.Value != "boom" {
		t.Fatalf("err = %v, want PanicError(boom)", err)
	}
}

func TestServerContextHeaderRoundTrip(t *testing.T) {
	base := metainfo.WithValue(context.Background(), "X-Tenant", "acme")
	ctx := transportx.ServerContext(base, "discovery:///svc", "/pkg.S/Method")

	tr, ok := transportFromServer(ctx)
	if !ok {
		t.Fatal("transporter not injected")
	}
	if got := tr.RequestHeader().Get("X-Tenant"); got != "acme" {
		t.Fatalf("Get = %q, want acme", got)
	}
	tr.RequestHeader().Set("X-Extra", "v1")
	if got := tr.RequestHeader().Get("X-Extra"); got != "v1" {
		t.Fatalf("Set broken: %q", got)
	}
	tr.RequestHeader().Del("X-Extra")
	if got := tr.RequestHeader().Get("X-Extra"); got != "" {
		t.Fatalf("Del broken: %q", got)
	}
	keys := tr.RequestHeader().Keys()
	if len(keys) != 1 || keys[0] != "X-Tenant" {
		t.Fatalf("Keys = %v", keys)
	}
	if tr.Operation() != "/pkg.S/Method" || tr.Kind() != "grpc" {
		t.Fatalf("kind/op = %s/%s", tr.Kind(), tr.Operation())
	}
}

func transportFromServer(ctx context.Context) (transport.Transporter, bool) {
	return transport.FromServerContext(ctx)
}

// serverRPCInfoCtx 构造带 server 侧 rpcinfo 的 ctx（模拟 Kitex 服务端链内）。
func serverRPCInfoCtx(proto ktransport.Protocol) context.Context {
	to := rpcinfo.NewEndpointInfo("echo", "Echo", nil, nil)
	from := rpcinfo.NewEndpointInfo("caller", "Call", nil, nil)
	inv := rpcinfo.NewInvocation("echo", "Echo")
	cfg := rpcinfo.NewRPCConfig()
	rpcinfo.AsMutableRPCConfig(cfg).SetTransportProtocol(proto)
	ri := rpcinfo.NewRPCInfo(from, to, inv, cfg, rpcinfo.NewRPCStats())
	return rpcinfo.NewCtxWithRPCInfo(context.Background(), ri)
}

// captureTransport 返回记录链内 transport 信息的 chain.Middleware 与快照指针。
type tmwCapture struct {
	seen bool
	kind string
	op   string
}

func captureTransport(c *tmwCapture) chain.Middleware {
	return func(next chain.Handler) chain.Handler {
		return func(ctx context.Context, req any) (any, error) {
			if tr, ok := transport.FromServerContext(ctx); ok {
				c.seen = true
				c.kind = string(tr.Kind())
				c.op = tr.Operation()
				if tr.RequestHeader() == nil || tr.ReplyHeader() == nil {
					panic("transportx headers must not be nil")
				}
			}
			return next(ctx, req)
		}
	}
}

// TestToKitex_AutoInjectsServerContext 回归（#315）：不挂 ServerContext 时
// ToKitex 也必须从 rpcinfo 自动注入 transport 上下文，AccessLog 的 op 不能
// 是 "-"（否则 SkipPaths / RedactPathPrefixes 整体失效）。
func TestToKitex_AutoInjectsServerContext(t *testing.T) {
	cases := []struct {
		name     string
		proto    ktransport.Protocol
		wantKind string
		wantOp   string
	}{
		{"thrift-framed", ktransport.Framed, "thrift", "echo.Echo"},
		{"thrift-ttheader", ktransport.TTHeaderFramed, "thrift", "echo.Echo"},
		{"grpc", ktransport.GRPC, "grpc", "/echo/Echo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c tmwCapture
			h := ToKitex(captureTransport(&c))(func(context.Context, any, any) error { return nil })
			if err := h(serverRPCInfoCtx(tc.proto), nil, &struct{}{}); err != nil {
				t.Fatalf("endpoint err = %v", err)
			}
			if !c.seen {
				t.Fatal("ToKitex must auto-inject server transport context")
			}
			if c.op == "-" || c.op == "" {
				t.Fatalf("operation must be derived from rpcinfo, got %q", c.op)
			}
			if c.op != tc.wantOp || c.kind != tc.wantKind {
				t.Fatalf("kind/op = %s/%s, want %s/%s", c.kind, c.op, tc.wantKind, tc.wantOp)
			}
		})
	}
}

// TestToKitex_KeepsExplicitServerTransport 显式注入优先：链内已有 server
// transport（外层 ServerContext 或手工注入）时不覆盖，ServerContext 退化为
// 可选覆盖手段。
func TestToKitex_KeepsExplicitServerTransport(t *testing.T) {
	base := transportx.ServerContext(context.Background(), "manual-endpoint", "/manual/Method")
	var c tmwCapture
	h := ToKitex(captureTransport(&c))(func(context.Context, any, any) error { return nil })
	if err := h(base, nil, &struct{}{}); err != nil {
		t.Fatalf("endpoint err = %v", err)
	}
	if !c.seen || c.op != "/manual/Method" {
		t.Fatalf("explicit transport must win, got seen=%v op=%q", c.seen, c.op)
	}
}

// TestToKitex_ServerContextMiddlewareOverride 显式 ServerContext 中间件仍可
// 覆盖自动推导的 operation。
func TestToKitex_ServerContextMiddlewareOverride(t *testing.T) {
	var c tmwCapture
	ep := ToKitex(captureTransport(&c))(func(context.Context, any, any) error { return nil })
	// 外层显式覆盖：先注入，再进 ToKitex（ToKitex 检测到已注入并保留）。
	outer := ServerContext("/override/Method")
	if err := outer(ep)(serverRPCInfoCtx(ktransport.Framed), nil, &struct{}{}); err != nil {
		t.Fatalf("endpoint err = %v", err)
	}
	if c.op != "/override/Method" {
		t.Fatalf("explicit ServerContext override lost, op = %q", c.op)
	}
	if c.kind != "thrift" {
		t.Fatalf("ServerContext must derive thrift kind from rpcinfo, got %q", c.kind)
	}
}

// TestOperation 覆盖 operation 组装的退化分支。
func TestOperation(t *testing.T) {
	if got := operation(transport.KindGRPC, "svc", "M"); got != "/svc/M" {
		t.Errorf("grpc op = %q", got)
	}
	if got := operation(transportx.KindThrift, "svc", "M"); got != "svc.M" {
		t.Errorf("thrift op = %q", got)
	}
	if got := operation(transport.KindGRPC, "", "M"); got != "M" {
		t.Errorf("missing service op = %q", got)
	}
	if got := operation(transport.KindGRPC, "svc", ""); got != "svc" {
		t.Errorf("missing method op = %q", got)
	}
	if got := operation(transport.KindGRPC, "", ""); got != "" {
		t.Errorf("empty op = %q", got)
	}
}
