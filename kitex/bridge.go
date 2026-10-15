package kitex

import (
	"context"
	stderrors "errors"

	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/pkg/endpoint"
	"github.com/cloudwego/kitex/pkg/kerrors"

	ekiterr "github.com/yi-nology/ekit-errors"
)

// WithBizErrorBridge 返回 kitex client middleware,把对端抛的 kitex kerrors
// (BizStatusError / GRPCBizStatusError / DetailedError / timeout / no-instance / ACL)
// 转为 ekit BizError 沿原 error 通道上抛,让 ekit handler 层 middleware.Fail +
// StatusMapper 走统一处理路径,不再 fallback 500。
//
// ⚠️ 行为变化:开启后,client 调用的 error 返回类型可能从 kitex kerrors 变成
// ekit *BizError,使用 errors.Is/As 判定的代码需注意。pkg/errors.AsBizError
// 仍可识别(*BizError Unwrap 自身 cause,kerrors.BizStatusError 通过
// kerrors.FromBizStatusError 提取)。
//
// 官方依据:
//
//	https://www.cloudwego.io/zh/docs/kitex/best-practice/error_handle/
//	https://www.cloudwego.io/zh/docs/kitex/tutorials/basic-feature/bizstatuserr/
//
// 用法(对称其他 client.Option):
//
//	cli, err := myservice.NewClient("myservice",
//	    kitex.WithBizErrorBridge(),
//	    client.WithHostPorts(":8888"),
//	)
//
// ⚠️ kitex v0.16.3 实测(e2e 2026-09-06):普通 thrift/TTHeader ping-pong 下,
// server 端 BizStatusError 不做类型化解码(仅 ttstream/gRPC 流式支持),
// 到达 client 的是 DetailedError 文本 → 走 50000 兜底分支;类型化 code/msg
// 映射需要流式协议。详见 kitex/e2e_meta_test.go。
func WithBizErrorBridge() client.Option {
	return client.WithMiddlewareBuilder(bizErrorBridgeMWBuilder)
}

// bizErrorBridgeMWBuilder 是 WithBizErrorBridge 的核心 middleware 构造逻辑,
// 抽出为包内 helper 以便测试直接调,避免 reflect 解 client.Option。
func bizErrorBridgeMWBuilder(ctx context.Context) endpoint.Middleware {
	return func(next endpoint.Endpoint) endpoint.Endpoint {
		return func(ctx context.Context, req, resp interface{}) (err error) {
			err = next(ctx, req, resp)
			if err == nil {
				return nil
			}
			if biz, ok := FromKitexError(err); ok {
				return biz
			}
			return err
		}
	}
}

// FromKitexError 把 kitex kerrors 错误转为 ekit BizError,供客户端统一
// 错误处理路径(middleware.Fail、StatusMapper)使用。覆盖以下类型:
//
//   - *kerrors.BizStatusError / *kerrors.GRPCBizStatusError(对端业务异常):
//     透传 BizStatusCode 作为 BizCode,BizMessage 作为 Message;
//     HTTPCode 留 0,由 ekit StatusMapper 推导。
//   - kerrors.IsTimeoutError(链路超时):BizCode=50400, HTTPCode=504。
//   - kerrors.ErrNoMoreInstance / ErrACL(无可用实例 / ACL 拒绝):
//     BizCode=50300 / 40301, HTTPCode=503 / 403。
//   - *kerrors.DetailedError 兜底:BizCode=50000, HTTPCode=500。
//   - nil / 非 kitex 错误:返回 (nil, false),调用方走 fallback。
//
// 原始错误始终保留为 BizError.Cause(经 Unwrap 链可达):errors.Is /
// errors.As / kerrors.FromBizStatusError 等链式判定对转换结果依然有效,
// DetailedError 携带的底层 cause 不会丢失(2026-09-05 复审修复:原实现
// 丢弃原始错误,转换后排障信息断链)。
//
// 官方依据:https://www.cloudwego.io/zh/docs/kitex/best-practice/error_handle/
// ("kerrors 的错误定义支持 errors.Is 和 errors.Unwrap"、"basic error types
// 可以用 WithCause 来添加详细原因" — 错误链可追溯是 kitex 官方语义)
//
// 用法(典型场景:kitex client middleware,把对端错误转 ekit BizError 上抛):
//
//	if biz, ok := kitex.FromKitexError(err); ok {
//	    return biz
//	}
//	return err
//
// 归属说明(2026-09-19 重构):本函数原在 pkg/errors(kerrors_bridge.go),
// 属"pkg/errors import kitex kerrors"明示例外;为恢复 L1 基础层零框架
// 依赖,迁至 kitex 包——kitex 错误桥接本就是 kitex 专属能力。
func FromKitexError(err error) (*ekiterr.BizError, bool) {
	if err == nil {
		return nil, false
	}

	// 1. 业务异常(BizStatusError)优先识别 — handler 主动抛的业务错误
	if bse, ok := kerrors.FromBizStatusError(err); ok {
		return ekiterr.WrapBizError(int(bse.BizStatusCode()), bse.BizMessage(), err), true
	}

	// 2. RPC 链路超时 — 走 504 Gateway Timeout
	if kerrors.IsTimeoutError(err) {
		return ekiterr.WrapBizErrorWithStatus(50400, 504, "rpc timeout", err), true
	}

	// 3. 无可用实例 / ACL 拒绝 — 服务发现或权限层错误
	if stderrors.Is(err, kerrors.ErrNoMoreInstance) {
		return ekiterr.WrapBizErrorWithStatus(50300, 503, "no instance available", err), true
	}
	if stderrors.Is(err, kerrors.ErrACL) {
		return ekiterr.WrapBizErrorWithStatus(40301, 403, "request forbidden", err), true
	}

	// 4. DetailedError 兜底(RPC 异常的 StandardError + cause 链)
	var de *kerrors.DetailedError
	if stderrors.As(err, &de) {
		return ekiterr.WrapBizErrorWithStatus(50000, 500, de.Error(), err), true
	}

	return nil, false
}

// IsKitexTimeoutError 包装 kerrors.IsTimeoutError,提供不直接 import
// kitex kerrors 的判定入口。
//
// 官方依据:best-practice/error_handle (Kitex 内部错误章节"判断具体错误类型")
func IsKitexTimeoutError(err error) bool {
	return kerrors.IsTimeoutError(err)
}
