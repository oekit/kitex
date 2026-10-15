package kitex

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/cloudwego/kitex/pkg/endpoint"
	"github.com/cloudwego/kitex/pkg/kerrors"

	ekiterr "github.com/oekit/pkg/errors"
)

// invokeMiddleware 调起 bizErrorBridgeMWBuilder,直接构造 Middleware
// (避免 reflect 解 client.Option 的不稳定性),与 production 同源。
func invokeMiddleware(_ *testing.T, next endpoint.Endpoint) endpoint.Endpoint {
	return bizErrorBridgeMWBuilder(context.Background())(next)
}

// fakeEndpoint 返回预设 err,模拟 kitex middleware 链末端真实 RPC 调用结果。
type fakeEndpoint struct {
	err error
}

func (f *fakeEndpoint) call(_ context.Context, _, _ interface{}) error { return f.err }

// wrap 套一层 Middleware,执行 real call 路径
func (f *fakeEndpoint) wrap() endpoint.Endpoint {
	return func(ctx context.Context, req, resp interface{}) error {
		return f.call(ctx, req, resp)
	}
}

// TestWithBizErrorBridge_BizStatusError 验证 BizStatusError 转为 *BizError,
// 透传 BizStatusCode + BizMessage。
func TestWithBizErrorBridge_BizStatusError(t *testing.T) {
	inner := kerrors.NewBizStatusError(1001, "余额不足")
	ep := invokeMiddleware(t, (&fakeEndpoint{err: inner}).wrap())

	err := ep(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	biz, ok := ekiterr.AsBizError(err)
	if !ok {
		t.Fatalf("expected *BizError, got %T: %v", err, err)
	}
	if biz.BizCode != 1001 {
		t.Errorf("BizCode = %d, want 1001", biz.BizCode)
	}
	if biz.Message != "余额不足" {
		t.Errorf("Message = %q, want %q", biz.Message, "余额不足")
	}
}

// TestWithBizErrorBridge_Timeout 验证 timeout 走 504。
func TestWithBizErrorBridge_Timeout(t *testing.T) {
	ep := invokeMiddleware(t, (&fakeEndpoint{err: kerrors.ErrRPCTimeout}).wrap())

	err := ep(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	biz, ok := ekiterr.AsBizError(err)
	if !ok {
		t.Fatalf("expected *BizError, got %T: %v", err, err)
	}
	if biz.HTTPCode != 504 {
		t.Errorf("HTTPCode = %d, want 504", biz.HTTPCode)
	}
	if biz.BizCode != 50400 {
		t.Errorf("BizCode = %d, want 50400", biz.BizCode)
	}
}

// TestWithBizErrorBridge_NoInstance 验证 ErrNoMoreInstance 走 503。
func TestWithBizErrorBridge_NoInstance(t *testing.T) {
	ep := invokeMiddleware(t, (&fakeEndpoint{err: kerrors.ErrNoMoreInstance}).wrap())

	err := ep(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	biz, ok := ekiterr.AsBizError(err)
	if !ok {
		t.Fatalf("expected *BizError, got %T: %v", err, err)
	}
	if biz.HTTPCode != 503 {
		t.Errorf("HTTPCode = %d, want 503", biz.HTTPCode)
	}
}

// TestWithBizErrorBridge_DetailedError 验证 DetailedError 兜底。
func TestWithBizErrorBridge_DetailedError(t *testing.T) {
	inner := kerrors.ErrInternalException.WithCause(errors.New("boom"))
	ep := invokeMiddleware(t, (&fakeEndpoint{err: inner}).wrap())

	err := ep(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	biz, ok := ekiterr.AsBizError(err)
	if !ok {
		t.Fatalf("expected *BizError, got %T: %v", err, err)
	}
	if biz.BizCode != 50000 {
		t.Errorf("BizCode = %d, want 50000", biz.BizCode)
	}
}

// TestWithBizErrorBridge_NilError 验证 err == nil 时原样返回 nil。
func TestWithBizErrorBridge_NilError(t *testing.T) {
	ep := invokeMiddleware(t, (&fakeEndpoint{err: nil}).wrap())

	err := ep(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("expected nil, got: %v", err)
	}
}

// TestWithBizErrorBridge_NonKitex 验证非 kitex 错误原样透传(不转换为 BizError,
// 调用方需要 fallback)。
func TestWithBizErrorBridge_NonKitex(t *testing.T) {
	plain := errors.New("plain error")
	ep := invokeMiddleware(t, (&fakeEndpoint{err: plain}).wrap())

	err := ep(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if _, ok := ekiterr.AsBizError(err); ok {
		t.Fatal("non-kitex err should not be converted to BizError")
	}
	if err.Error() != "plain error" {
		t.Errorf("err.Error() = %q, want %q", err.Error(), "plain error")
	}
}

// TestFromKitexError_Nil 验证 nil 错误返回 (nil, false)。
func TestFromKitexError_Nil(t *testing.T) {
	biz, ok := FromKitexError(nil)
	if ok || biz != nil {
		t.Fatalf("expected (nil, false) for nil err, got (%v, %v)", biz, ok)
	}
}

// TestFromKitexError_NonKitex 验证非 kitex 错误返回 (nil, false)。
func TestFromKitexError_NonKitex(t *testing.T) {
	notKitex := errors.New("plain error")
	biz, ok := FromKitexError(notKitex)
	if ok || biz != nil {
		t.Fatalf("expected (nil, false) for non-kitex err, got (%v, %v)", biz, ok)
	}
}

// TestFromKitexError_BizStatusError 验证业务异常透传 BizStatusCode + BizMessage。
func TestFromKitexError_BizStatusError(t *testing.T) {
	err := kerrors.NewBizStatusError(1001, "余额不足")
	biz, ok := FromKitexError(err)
	if !ok || biz == nil {
		t.Fatalf("expected BizError, got (%v, %v)", biz, ok)
	}
	if biz.BizCode != 1001 {
		t.Errorf("BizCode = %d, want 1001", biz.BizCode)
	}
	if biz.Message != "余额不足" {
		t.Errorf("Message = %q, want %q", biz.Message, "余额不足")
	}
	if biz.HTTPCode != 0 {
		t.Errorf("HTTPCode = %d, want 0 (mapper 推导)", biz.HTTPCode)
	}
}

// TestFromKitexError_BizStatusErrorGRPC 验证 GRPC 业务异常也走相同桥接。
func TestFromKitexError_BizStatusErrorGRPC(t *testing.T) {
	err := kerrors.NewGRPCBizStatusError(40404, "resource not found")
	biz, ok := FromKitexError(err)
	if !ok || biz == nil {
		t.Fatalf("expected BizError, got (%v, %v)", biz, ok)
	}
	if biz.BizCode != 40404 {
		t.Errorf("BizCode = %d, want 40404", biz.BizCode)
	}
	if biz.Message != "resource not found" {
		t.Errorf("Message = %q, want %q", biz.Message, "resource not found")
	}
}

// TestFromKitexError_BizStatusErrorWrapped 验证 wrapped BizStatusError 也能识别
// (errors.As 链式匹配)。
func TestFromKitexError_BizStatusErrorWrapped(t *testing.T) {
	inner := kerrors.NewBizStatusError(503, "downstream busy")
	wrapped := errors.Join(errors.New("rpc call failed"), inner)

	biz, ok := FromKitexError(wrapped)
	if !ok || biz == nil {
		t.Fatalf("expected BizError from wrapped err, got (%v, %v)", biz, ok)
	}
	if biz.BizCode != 503 {
		t.Errorf("BizCode = %d, want 503", biz.BizCode)
	}
}

// TestFromKitexError_Timeout 验证 timeout 走 504。
func TestFromKitexError_Timeout(t *testing.T) {
	// kerrors.ErrRPCTimeout 经 IsTimeoutError 判 timeout = true
	biz, ok := FromKitexError(kerrors.ErrRPCTimeout)
	if !ok || biz == nil {
		t.Fatalf("expected BizError for timeout, got (%v, %v)", biz, ok)
	}
	if biz.BizCode != 50400 {
		t.Errorf("BizCode = %d, want 50400", biz.BizCode)
	}
	if biz.HTTPCode != 504 {
		t.Errorf("HTTPCode = %d, want 504", biz.HTTPCode)
	}
	if biz.Message != "rpc timeout" {
		t.Errorf("Message = %q, want %q", biz.Message, "rpc timeout")
	}
}

// TestFromKitexError_NoInstance 验证无实例走 503。
func TestFromKitexError_NoInstance(t *testing.T) {
	biz, ok := FromKitexError(kerrors.ErrNoMoreInstance)
	if !ok || biz == nil {
		t.Fatalf("expected BizError for no-instance, got (%v, %v)", biz, ok)
	}
	if biz.BizCode != 50300 {
		t.Errorf("BizCode = %d, want 50300", biz.BizCode)
	}
	if biz.HTTPCode != 503 {
		t.Errorf("HTTPCode = %d, want 503", biz.HTTPCode)
	}
}

// TestFromKitexError_ACL 验证 ACL 拒绝走 403。
func TestFromKitexError_ACL(t *testing.T) {
	biz, ok := FromKitexError(kerrors.ErrACL)
	if !ok || biz == nil {
		t.Fatalf("expected BizError for ACL, got (%v, %v)", biz, ok)
	}
	if biz.BizCode != 40301 {
		t.Errorf("BizCode = %d, want 40301", biz.BizCode)
	}
	if biz.HTTPCode != 403 {
		t.Errorf("HTTPCode = %d, want 403", biz.HTTPCode)
	}
}

// TestFromKitexError_DetailedError 验证 DetailedError 兜底转 50000。
// DetailedError 只能通过 basicError.WithCause/WithCauseAndStack 链式构造
// (字段 unexported,无公开 NewDetailedError)。
func TestFromKitexError_DetailedError(t *testing.T) {
	de := kerrors.ErrInternalException.WithCause(errors.New("test cause"))
	biz, ok := FromKitexError(de)
	if !ok || biz == nil {
		t.Fatalf("expected BizError for DetailedError, got (%v, %v)", biz, ok)
	}
	if biz.BizCode != 50000 {
		t.Errorf("BizCode = %d, want 50000", biz.BizCode)
	}
	if biz.HTTPCode != 500 {
		t.Errorf("HTTPCode = %d, want 500", biz.HTTPCode)
	}
	if biz.Message == "" {
		t.Error("Message should be populated from DetailedError")
	}
}

// TestFromKitexError_PriorityBizOverTimeout 验证 BizStatusError 优先于 timeout 识别
// (对端先抛业务异常,内部 wrap 一个 timeout 不会误判)。
func TestFromKitexError_PriorityBizOverTimeout(t *testing.T) {
	// BizStatusError 不带 timeout 属性,IsTimeoutError 应为 false,先 hit 业务分支
	err := kerrors.NewBizStatusError(1001, "余额不足")
	biz, _ := FromKitexError(err)
	if biz.BizCode != 1001 {
		t.Errorf("BizCode = %d, want 1001 (BizStatusError 优先)", biz.BizCode)
	}
}

// TestIsKitexTimeoutError 验证 IsKitexTimeoutError 包装正确。
func TestIsKitexTimeoutError(t *testing.T) {
	if !IsKitexTimeoutError(kerrors.ErrRPCTimeout) {
		t.Error("IsKitexTimeoutError(ErrRPCTimeout) = false, want true")
	}
	if IsKitexTimeoutError(kerrors.ErrNoMoreInstance) {
		t.Error("IsKitexTimeoutError(ErrNoMoreInstance) = true, want false")
	}
	if IsKitexTimeoutError(nil) {
		t.Error("IsKitexTimeoutError(nil) = true, want false")
	}
	if IsKitexTimeoutError(errors.New("plain")) {
		t.Error("IsKitexTimeoutError(plain) = true, want false")
	}
}

// TestFromKitexError_ThriftErrorCodes 覆盖 kitex 文档 best-practice/error_handle
// 列出的 8 个 Thrift 错误码。这些错误在 Kitex 内部会被包装为 DetailedError,
// 走 FromKitexError 的 DetailedError 兜底分支。
//
// basicError.WithCause 是 unexported 类型方法,表里用 buildFn 闭包延迟调用,
// 避免在 case 字段上写 *basicError(类型不可导出)。
func TestFromKitexError_ThriftErrorCodes(t *testing.T) {
	cases := []struct {
		name    string
		buildFn func(cause error) error
	}{
		{"UnknownApplicationException", kerrors.ErrInternalException.WithCause},
		{"UnknownMethod", kerrors.ErrRoute.WithCause},
		{"InvalidMessageTypeException", kerrors.ErrInternalException.WithCause},
		{"WrongMethodName", kerrors.ErrRoute.WithCause},
		{"BadSequenceID", kerrors.ErrInternalException.WithCause},
		{"MissingResult", kerrors.ErrInternalException.WithCause},
		{"InternalError", kerrors.ErrInternalException.WithCause},
		{"ProtocolError", kerrors.ErrInternalException.WithCause},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			de := tc.buildFn(errors.New(tc.name))
			biz, ok := FromKitexError(de)
			if !ok || biz == nil {
				t.Fatalf("[%s] expected BizError, got (%v, %v)", tc.name, biz, ok)
			}
			// Thrift 错误码统一归类为 50000 (RPC 链路错误)
			if biz.BizCode != 50000 {
				t.Errorf("[%s] BizCode = %d, want 50000", tc.name, biz.BizCode)
			}
		})
	}
}

// TestFromKitexError_PreservesCauseChain 钉住 2026-09-05 修复:转换结果经
// Unwrap 保留原始 kitex 错误链,errors.Is / kerrors.FromBizStatusError
// 对 bridged 错误依然可用(排障信息不再断链)。
func TestFromKitexError_PreservesCauseChain(t *testing.T) {
	// timeout:原始错误链应可被 errors.Is 识别
	biz, ok := FromKitexError(kerrors.ErrRPCTimeout)
	if !ok {
		t.Fatal("expected timeout bridged")
	}
	if !errors.Is(biz, kerrors.ErrRPCTimeout) {
		t.Error("errors.Is(biz, kerrors.ErrRPCTimeout) should hold via Cause chain")
	}

	// no more instance
	biz2, ok := FromKitexError(kerrors.ErrNoMoreInstance)
	if !ok {
		t.Fatal("expected no-more-instance bridged")
	}
	if !errors.Is(biz2, kerrors.ErrNoMoreInstance) {
		t.Error("errors.Is(biz2, kerrors.ErrNoMoreInstance) should hold via Cause chain")
	}

	// BizStatusError:kitex 官方提取器对 bridged 错误仍应可识别
	bse := kerrors.NewBizStatusError(1001, "order not found")
	wrapped := fmt.Errorf("rpc layer: %w", bse)
	biz3, ok := FromKitexError(wrapped)
	if !ok {
		t.Fatal("expected biz status bridged")
	}
	if _, ok := kerrors.FromBizStatusError(biz3); !ok {
		t.Error("kerrors.FromBizStatusError(biz3) should hold via Cause chain")
	}
}
