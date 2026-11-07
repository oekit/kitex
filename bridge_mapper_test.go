package kitex

// #122 单元测试：WithBizErrorMapper 把应用侧错误在 server 中间件出口单点
// 映射为类型化 BizStatusError（塞 rpcinfo + return nil，由
// transmeta.ServerTTHeaderHandler 写 biz-status 头）。
//
// 契约点：
//   - 已是 BizStatusError 的错误原样透传（mapper 不覆盖显式桥）；
//   - mapper 返回 ok=false 走原传输错误语义；
//   - ok=true 时 rpcinfo Invocation 被写入映射结果（对称 kitex core endpoint
//     对 handler 直抛 BizStatusError 的处理）。

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/cloudwego/kitex/pkg/endpoint"
	"github.com/cloudwego/kitex/pkg/kerrors"
	"github.com/cloudwego/kitex/pkg/rpcinfo"
	"github.com/cloudwego/kitex/pkg/transmeta"
	"github.com/cloudwego/kitex/pkg/utils"
	"github.com/cloudwego/kitex/server"
)

// appError 是测试用应用侧错误（模拟 octopus 的 pkg.AppError）。
type appError struct {
	code int32
	msg  string
}

func (e *appError) Error() string { return fmt.Sprintf("app error %d: %s", e.code, e.msg) }

// mapperCtx 构造带 rpcinfo 的 ctx（server 中间件真实执行环境），返回 ctx
// 与可读回 BizStatusErr 的 rpcinfo。
func mapperCtx() (context.Context, rpcinfo.RPCInfo) {
	ink := rpcinfo.NewInvocation("mapper-svc", "Echo")
	ri := rpcinfo.NewRPCInfo(nil, nil, ink, nil, nil)
	return rpcinfo.NewCtxWithRPCInfo(context.Background(), ri), ri
}

// invokeMapperMW 以生产同源路径调起 bizErrorMapperMW。
func invokeMapperMW(mapper BizErrorMapper, next endpoint.Endpoint) endpoint.Endpoint {
	return bizErrorMapperMW(mapper)(next)
}

// appErrMapper 是把 *appError 映射为类型化业务错误的标准形态 mapper。
func appErrMapper(err error) (int32, string, bool) {
	var ae *appError
	if errors.As(err, &ae) {
		return ae.code, ae.msg, true
	}
	return 0, "", false
}

// TestBizErrorMapper_MapsAppErrorToBizStatus #122 主路径：handler 返回应用侧
// 错误（kitex core endpoint 会包一层 kerrors.ErrBiz）→ mapper 在中间件出口
// 单点映射 → rpcinfo Invocation 写入映射结果、err 归 nil（client 侧经
// biz-status 头收到类型化错误，而不是未类型化传输错误）。
func TestBizErrorMapper_MapsAppErrorToBizStatus(t *testing.T) {
	ctx, ri := mapperCtx()

	// 模拟 core endpoint 出口形态：ErrBiz.WithCause(appErr)。
	outbound := kerrors.ErrBiz.WithCause(&appError{code: 40901, msg: "conflict"})
	ep := invokeMapperMW(appErrMapper, func(context.Context, interface{}, interface{}) error {
		return outbound
	})

	if err := ep(ctx, nil, nil); err != nil {
		t.Fatalf("mapped error must return nil (对称 core endpoint 的塞 rpcinfo 后 return nil), got %v", err)
	}
	bizErr := ri.Invocation().BizStatusErr()
	if bizErr == nil {
		t.Fatal("Invocation.BizStatusErr() 未写入：biz-status 头不会随响应上行")
	}
	if bizErr.BizStatusCode() != 40901 || bizErr.BizMessage() != "conflict" {
		t.Errorf("mapped = (%d, %q), want (40901, \"conflict\")", bizErr.BizStatusCode(), bizErr.BizMessage())
	}
}

// TestBizErrorMapper_AppErrorReachableThroughErrBizWrap 钉住 mapper 契约：
// 出口 err 被 kitex 包成 ErrBiz DetailedError 后，原始应用错误经 Unwrap 链
// 仍可达（errors.As 直接可用）——mapper 无需感知 kitex 的包装细节。
func TestBizErrorMapper_AppErrorReachableThroughErrBizWrap(t *testing.T) {
	outbound := kerrors.ErrBiz.WithCause(&appError{code: 50001, msg: "boom"})
	var seen *appError
	if !errors.As(outbound, &seen) {
		t.Fatal("errors.As 必须能穿过 ErrBiz 包装命中 *appError")
	}
	if seen.code != 50001 {
		t.Errorf("seen.code = %d, want 50001", seen.code)
	}
}

// TestBizErrorMapper_NotMappedKeepsTransportError #122 契约点：mapper 返回
// ok=false ⇒ 原传输错误语义（原样上抛，rpcinfo 不写入）。
func TestBizErrorMapper_NotMappedKeepsTransportError(t *testing.T) {
	ctx, ri := mapperCtx()

	outbound := kerrors.ErrBiz.WithCause(errors.New("链路错误"))
	ep := invokeMapperMW(appErrMapper, func(context.Context, interface{}, interface{}) error {
		return outbound
	})

	err := ep(ctx, nil, nil)
	if !errors.Is(err, outbound) {
		t.Fatalf("ok=false 必须原样上抛 outbound error, got %v", err)
	}
	if ri.Invocation().BizStatusErr() != nil {
		t.Error("ok=false 时不得写入 Invocation.BizStatusErr()")
	}
}

// TestBizErrorMapper_BizStatusErrorPassthrough #122 契约点：已是
// BizStatusError 的错误原样透传，mapper 不被调用（显式桥优先）。
func TestBizErrorMapper_BizStatusErrorPassthrough(t *testing.T) {
	ctx, ri := mapperCtx()

	inner := kerrors.NewBizStatusError(1001, "显式类型化错误")
	mapperCalled := false
	ep := invokeMapperMW(func(err error) (int32, string, bool) {
		mapperCalled = true
		return 0, "", false
	}, func(context.Context, interface{}, interface{}) error {
		return inner
	})

	err := ep(ctx, nil, nil)
	if !errors.Is(err, inner) {
		t.Fatalf("BizStatusError 必须原样透传, got %v", err)
	}
	if mapperCalled {
		t.Error("已是 BizStatusError 时不得调用 mapper（不覆盖显式桥）")
	}
	if ri.Invocation().BizStatusErr() != nil {
		t.Error("透传分支不应改写 rpcinfo（由 kitex core endpoint 自己的桥路径负责）")
	}
}

// TestBizErrorMapper_SuccessUntouched 成功路径：err==nil 不触发 mapper。
func TestBizErrorMapper_SuccessUntouched(t *testing.T) {
	ctx, _ := mapperCtx()
	mapperCalled := false
	ep := invokeMapperMW(func(err error) (int32, string, bool) {
		mapperCalled = true
		return 0, "", false
	}, func(context.Context, interface{}, interface{}) error { return nil })

	if err := ep(ctx, nil, nil); err != nil {
		t.Fatalf("success path must stay nil, got %v", err)
	}
	if mapperCalled {
		t.Error("err==nil 时不得调用 mapper")
	}
}

// TestBizErrorMapper_NoRPCInfoFallback 兜底分支：ctx 无 rpcinfo（非 RPC
// 上下文，如本地直调测试）时，映射结果按类型化错误本体上抛而不是塞
// rpcinfo 后吞成 (nil, nil)。
func TestBizErrorMapper_NoRPCInfoFallback(t *testing.T) {
	ep := invokeMapperMW(appErrMapper, func(context.Context, interface{}, interface{}) error {
		return kerrors.ErrBiz.WithCause(&appError{code: 40901, msg: "conflict"})
	})

	err := ep(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("无 rpcinfo 时不得吞成 nil")
	}
	biz, ok := kerrors.FromBizStatusError(err)
	if !ok {
		t.Fatalf("兜底应上抛类型化 BizStatusError, got %T: %v", err, err)
	}
	if biz.BizStatusCode() != 40901 {
		t.Errorf("BizStatusCode = %d, want 40901", biz.BizStatusCode())
	}
}

// TestWithBizErrorBridgeServer_OptionComposition 钉住 option 组装：透传
// WithBizErrorMapper 后 server.Option 同时携带 ServerTTHeaderHandler 与
// mapper middleware；不传 opts 时行为与旧版完全一致（仅 meta handler）——
// 向后兼容。
func TestWithBizErrorBridgeServer_OptionComposition(t *testing.T) {
	t.Run("无 opts：仅 meta handler（旧版行为不变）", func(t *testing.T) {
		o := &server.Options{}
		var di utils.Slice
		WithBizErrorBridgeServer().F(o, &di)

		if len(o.MetaHandlers) != 1 || o.MetaHandlers[0] != transmeta.ServerTTHeaderHandler {
			t.Errorf("MetaHandlers = %v, want [ServerTTHeaderHandler]", o.MetaHandlers)
		}
		if len(o.MWBs) != 0 {
			t.Errorf("len(MWBs) = %d, want 0（未传 mapper 不应挂中间件）", len(o.MWBs))
		}
	})

	t.Run("带 mapper：meta handler + mapper middleware", func(t *testing.T) {
		o := &server.Options{}
		var di utils.Slice
		WithBizErrorBridgeServer(WithBizErrorMapper(appErrMapper)).F(o, &di)

		if len(o.MetaHandlers) != 1 || o.MetaHandlers[0] != transmeta.ServerTTHeaderHandler {
			t.Errorf("MetaHandlers = %v, want [ServerTTHeaderHandler]", o.MetaHandlers)
		}
		if len(o.MWBs) != 1 {
			t.Fatalf("len(MWBs) = %d, want 1（mapper middleware）", len(o.MWBs))
		}
		// MWB 挂上后必须可用：以空 next 调起，不应 panic。
		mw := o.MWBs[0](context.Background())
		if mw == nil {
			t.Fatal("mapper middleware 为 nil")
		}
		_ = mw(func(context.Context, interface{}, interface{}) error { return nil })
	})
}
