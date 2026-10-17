package chainmw

import (
	"context"
	"errors"
	"testing"

	"github.com/bytedance/gopkg/cloud/metainfo"
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
