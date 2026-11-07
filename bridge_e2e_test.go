package kitex

// #94 e2e 钉子测试:真实 kitex server + client 走完整 transport 链,钉住
// "类型化业务错误在 middleware 链之后才附到 err" 的收敛与 TTHeader 前提:
//
//   - 主回归:client 仅给 WithBizErrorBridge()(不手动配 TTHeader/meta handler
//     ——旧的假成功触发形态),handler 直返类型化 BizStatusError,调用方应拿到
//     *BizError(code=1001) 而不是 (nil, nil) 假成功;
//   - 对照:缺 TTHeader meta handler 时 biz-status 头不走,业务错误被静默吞成
//     (nil, nil) —— 钉住 WithBizErrorBridgeServer 的存在理由。

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/pkg/kerrors"
	"github.com/cloudwego/kitex/server"

	echo "github.com/oekit/kitex/internal/e2e/kitex_gen/kitex/echo"
	echoservice "github.com/oekit/kitex/internal/e2e/kitex_gen/kitex/echo/echoservice"
	ekiterr "github.com/oekit/pkg/errors"
)

// bizErrHandler 固定返回类型化业务错误(BizStatusError)。
type bizErrHandler struct{}

func (h *bizErrHandler) Echo(_ context.Context, _ *echo.EchoRequest) (*echo.EchoResponse, error) {
	return nil, kerrors.NewBizStatusError(1001, "余额不足")
}

// appBizErrHandler 返回**应用侧**错误(ekit *BizError,非 kitex 类型)——
// #122 的 mapper 场景:映射不应依赖 handler 逐 return 点人工 toRPCError。
type appBizErrHandler struct{}

func (h *appBizErrHandler) Echo(_ context.Context, _ *echo.EchoRequest) (*echo.EchoResponse, error) {
	return nil, ekiterr.NewNotFoundError("task not found")
}

// ekitBizErrMapper 是 #122 的标准 mapper 形态:把 ekit *BizError 映射为
// (BizCode, Message)——挂在 WithBizErrorBridgeServer 的中间件出口单点生效。
func ekitBizErrMapper(err error) (int32, string, bool) {
	if biz, ok := ekiterr.AsBizError(err); ok {
		return int32(biz.BizCode), biz.Message, true
	}
	return 0, "", false
}

// startEchoServer 起一个真实 kitex server(随机端口,handler 可注入),
// 返回地址;测试结束自动 Stop(startBizErrServer 的通用化)。
func startEchoServer(t *testing.T, handler echo.EchoService, opts ...server.Option) string {
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

// startBizErrServer 起一个返回类型化业务错误的真实 kitex server(随机端口),
// 返回地址;测试结束自动 Stop。
func startBizErrServer(t *testing.T, opts ...server.Option) string {
	t.Helper()
	return startEchoServer(t, &bizErrHandler{}, opts...)
}

// TestE2E_BizErrorBridge_PostChainTypedBizError #94 回归主体:handler 直返
// 类型化 BizStatusError → server 写 TTHeader biz-status 头 → client 侧
// ClientTTHeaderHandler 解进 rpcinfo → bridge 在链后收敛为 ekit *BizError。
//
// client 只给 WithBizErrorBridge() + WithHostPorts:类型化 code/msg 必须到达
// (旧注释"仅 ttstream/gRPC 流式支持"为误判,见 bridge.go 修正说明)。
func TestE2E_BizErrorBridge_PostChainTypedBizError(t *testing.T) {
	addr := startBizErrServer(t, WithBizErrorBridgeServer())

	cli, err := echoservice.NewClient("e2e-echo",
		client.WithHostPorts(addr), WithBizErrorBridge())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = cli.Echo(context.Background(), &echo.EchoRequest{Message: "x"})
	if err == nil {
		t.Fatal("expected biz error, got (nil, nil) fake success")
	}

	biz, ok := ekiterr.AsBizError(err)
	if !ok {
		t.Fatalf("expected *BizError, got %T: %v", err, err)
	}
	if biz.BizCode != 1001 {
		t.Errorf("BizCode = %d, want 1001 (类型化映射,非 50000 兜底)", biz.BizCode)
	}
	if biz.Message != "余额不足" {
		t.Errorf("Message = %q, want %q", biz.Message, "余额不足")
	}
	if _, ok := kerrors.FromBizStatusError(biz); !ok {
		t.Error("kerrors.FromBizStatusError(biz) should hold via Cause chain")
	}
}

// TestE2E_BizErrorBridge_FakeSuccessWithoutMetaHandler 钉住 #94 的假成功场景:
// 缺 TTHeader meta handler 时 biz-status 头不走,BizStatusErr() 恒 nil,
// 业务错误被静默吞成 (nil, nil)——丢的是"业务错误"而不是"链路错误"。
// 这正是 WithBizErrorBridge / WithBizErrorBridgeServer 自动带传输前提的原因。
func TestE2E_BizErrorBridge_FakeSuccessWithoutMetaHandler(t *testing.T) {
	// server 不配 ServerTTHeaderHandler:biz-status 头根本没写
	addr := startBizErrServer(t)

	t.Run("raw client 无 meta handler", func(t *testing.T) {
		cli, err := echoservice.NewClient("e2e-echo", client.WithHostPorts(addr))
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		resp, err := cli.Echo(context.Background(), &echo.EchoRequest{Message: "x"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp != nil {
			t.Errorf("resp = %+v, want nil (业务错误被吞成假成功)", resp)
		}
	})

	t.Run("bridge client 但 server 缺对称 meta handler", func(t *testing.T) {
		cli, err := echoservice.NewClient("e2e-echo",
			client.WithHostPorts(addr), WithBizErrorBridge())
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		resp, err := cli.Echo(context.Background(), &echo.EchoRequest{Message: "x"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp != nil {
			t.Errorf("resp = %+v, want nil (server 端头没写,client 侧兜不住)", resp)
		}
	})
}

// TestE2E_BizErrorMapper_ServerSideSinglePoint #122 e2e 回归:handler 返回
// **应用侧**错误(ekit *BizError,非 kitex 类型,也不逐 return 点人工转
// BizStatusError)→ WithBizErrorBridgeServer(WithBizErrorMapper(...)) 在中间件
// 出口单点映射为类型化业务错误写 biz-status 头 → 挂 WithBizErrorBridge 的
// client 收到 *BizError(BizCode=404)——同服务内错误契约不再依赖人工纪律。
//
// 对照组(缺 mapper):同一 handler、只挂 WithBizErrorBridgeServer() 时,
// 应用侧错误以未类型化传输错误上抛(kerrors ErrBiz 兜底 BizCode=50000),
// 钉住「mapper 是缺的能力面」。
func TestE2E_BizErrorMapper_ServerSideSinglePoint(t *testing.T) {
	t.Run("mapper 单点映射:client 收到类型化 404", func(t *testing.T) {
		addr := startEchoServer(t, &appBizErrHandler{},
			WithBizErrorBridgeServer(WithBizErrorMapper(ekitBizErrMapper)))

		cli, err := echoservice.NewClient("e2e-echo",
			client.WithHostPorts(addr), WithBizErrorBridge())
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}

		_, err = cli.Echo(context.Background(), &echo.EchoRequest{Message: "x"})
		if err == nil {
			t.Fatal("expected typed biz error, got nil")
		}
		biz, ok := ekiterr.AsBizError(err)
		if !ok {
			t.Fatalf("expected *BizError, got %T: %v", err, err)
		}
		if biz.BizCode != 404 {
			t.Errorf("BizCode = %d, want 404 (mapper 透传应用侧业务码)", biz.BizCode)
		}
		if biz.Message != "task not found" {
			t.Errorf("Message = %q, want %q", biz.Message, "task not found")
		}
	})

	t.Run("缺 mapper:应用侧错误落 50000 兜底(漂移形态)", func(t *testing.T) {
		addr := startEchoServer(t, &appBizErrHandler{}, WithBizErrorBridgeServer())

		cli, err := echoservice.NewClient("e2e-echo",
			client.WithHostPorts(addr), WithBizErrorBridge())
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}

		_, err = cli.Echo(context.Background(), &echo.EchoRequest{Message: "x"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		biz, ok := ekiterr.AsBizError(err)
		if !ok {
			t.Fatalf("expected *BizError, got %T: %v", err, err)
		}
		// ErrBiz 兜底:未映射的应用错误以 generic 50000 上抛——正是 #122 描述的
		// 「漏一处 toRPCError 即契约漂移」形态。
		if biz.BizCode != 50000 {
			t.Errorf("BizCode = %d, want 50000 (ErrBiz 兜底,未类型化)", biz.BizCode)
		}
	})
}
