package kitex

import (
	"context"
	stderrors "errors"

	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/pkg/endpoint"
	"github.com/cloudwego/kitex/pkg/kerrors"
	"github.com/cloudwego/kitex/pkg/rpcinfo"
	"github.com/cloudwego/kitex/pkg/transmeta"
	"github.com/cloudwego/kitex/pkg/utils"
	"github.com/cloudwego/kitex/server"
	"github.com/cloudwego/kitex/transport"

	ekiterr "github.com/oekit/pkg/errors"
)

// WithBizErrorBridge 返回 kitex client.Option,把对端抛的 kitex kerrors
// (BizStatusError / GRPCBizStatusError / DetailedError / timeout / no-instance / ACL)
// 转为 ekit BizError 沿原 error 通道上抛,让 ekit handler 层 middleware.Fail +
// StatusMapper 走统一处理路径,不再 fallback 500。
//
// ⚠️ 行为变化:开启后,client 调用的 error 返回类型可能从 kitex kerrors 变成
// ekit *BizError,使用 errors.Is/As 判定的代码需注意。pkg/errors.AsBizError
// 仍可识别(*BizError Unwrap 自身 cause,kerrors.BizStatusError 通过
// kerrors.FromBizStatusError 提取)。
//
// # 类型化业务错误的收敛位置(#94)
//
// kitex client.Call 把类型化业务错误(ri.Invocation().BizStatusErr(),TTHeader
// biz-status 头解出)附到 err 的时机在 **middleware 链之后**(doFallbackIfNeeded
// 尾部):handler 抛 kerrors.BizStatusError 时 unary endpoint 的语义是"塞 rpcinfo
// 后 return nil",链内 err 恒 nil。故本 bridge 在 middleware 出口直接读 rpcinfo
// 的 BizStatusErr 收敛——包括 err==nil 的分支,否则类型化业务错误会以
// (nil, nil) 假成功放行。
//
// ⚠️ 收敛发生在链内:client fallback / retry 看到的 err 从 nil 变为
// *BizError(业务错误本身);业务错误经 rpcinfo 的判定路径
// (fallback 内 ri.Invocation().BizStatusErr())仍然可用。
//
// # TTHeader 传输前提(#94)
//
// 类型化 BizStatusError 经 TTHeader biz-status 头 + 双侧 transmeta meta handler
// 送达:server 端 transmeta.ServerTTHeaderHandler 写头,client 端
// transmeta.ClientTTHeaderHandler 读头。缺任一环节时 biz-status 头不走,
// 业务错误被静默吞成 (nil, nil) 假成功(丢的是"业务错误"而不是"链路错误",
// 调用方完全无感)。故本 option 默认附加:
//
//   - client.WithTransportProtocol(transport.TTHeaderFramed)
//   - client.WithMetaHandler(transmeta.ClientTTHeaderHandler)
//
// 服务端需对称使用 [WithBizErrorBridgeServer]。在此 option 之后再传
// client.WithTransportProtocol 可覆盖默认传输协议。
//
// ⚠️ 修正(此前注释有误):普通 thrift/TTHeader ping-pong 下 server 端
// BizStatusError 的类型化 code/msg **可以**到达 client(TTHeader + 双侧 meta
// handler 路径,kerrors.FromBizStatusError 直接命中);"仅 ttstream/gRPC 流式
// 支持"的旧结论是缺 meta handler 时的观测,按它配置会误判"类型化映射不可得"
// 而放弃正确配置。
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
//	svr := myservice.NewServer(handler, kitex.WithBizErrorBridgeServer())
func WithBizErrorBridge() client.Option {
	parts := []client.Option{
		client.WithTransportProtocol(transport.TTHeaderFramed),
		client.WithMetaHandler(transmeta.ClientTTHeaderHandler),
		client.WithMiddlewareBuilder(bizErrorBridgeMWBuilder),
	}
	return client.Option{F: func(o *client.Options, di *utils.Slice) {
		for _, opt := range parts {
			opt.F(o, di)
		}
	}}
}

// WithBizErrorBridgeServer 返回与 [WithBizErrorBridge] 对称的 kitex server.Option:
// 注册 transmeta.ServerTTHeaderHandler,把 handler 抛的类型化 BizStatusError
// 写进 TTHeader biz-status 头。缺它时业务错误不随响应上行,client 侧无论是否
// 开桥接都只能收到 (nil, nil) 假成功。
//
// 可选 opts 注册 server 侧 error-mapper(见 [WithBizErrorMapper]):应用侧
// 错误 → 类型化 BizStatusError 的映射在**中间件出口**对所有 outbound error
// 单点生效,不再依赖每个 handler 每个返回点的人工 toRPCError 纪律(#122,
// octopus 实证:4 个方法漏 1 处,漏掉的路径以未类型化传输错误上抛,同服务内
// 契约漂移)。
//
// 用法:
//
//	svr := myservice.NewServer(handler,
//	    kitex.WithBizErrorBridgeServer(
//	        kitex.WithBizErrorMapper(myAppErrorMapper),
//	    ))
func WithBizErrorBridgeServer(opts ...BizErrorBridgeServerOption) server.Option {
	cfg := &bizErrorBridgeServerConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	parts := []server.Option{
		server.WithMetaHandler(transmeta.ServerTTHeaderHandler),
	}
	if cfg.mapper != nil {
		parts = append(parts, server.WithMiddleware(bizErrorMapperMW(cfg.mapper)))
	}
	return server.Option{F: func(o *server.Options, di *utils.Slice) {
		for _, opt := range parts {
			opt.F(o, di)
		}
	}}
}

// BizErrorMapper 把 server 中间件出口的 outbound error 映射为类型化业务错误
// 的 (code, msg)。返回 ok=false 表示不映射,该错误走**原传输错误语义**。
//
// 传入的 err 是中间件链出口的错误形态:handler 返回的非类型化错误会被 kitex
// core endpoint 包成 kerrors.ErrBiz 的 DetailedError(原始应用错误经
// Unwrap 链可达,errors.Is/As 直接可用):
//
//	code, msg, ok := mapper(err)
//	var appErr *pkg.AppError
//	if errors.As(err, &appErr) && appErr.Code == 40901 { ... }
//
// 契约点(#122):
//   - 已是 BizStatusError 的错误**原样透传**,mapper 不覆盖显式桥(handler
//     主动抛的类型化错误在 core endpoint 已塞 rpcinfo,不会经过 mapper);
//   - ok=false 走原传输错误语义;
//   - ok=true 时映射结果写进 TTHeader biz-status 头,挂 [WithBizErrorBridge]
//     的 client 收到类型化 BizStatusError(与 client 侧桥的链内收敛对称)。
type BizErrorMapper func(err error) (code int32, msg string, ok bool)

// BizErrorBridgeServerOption 配置 [WithBizErrorBridgeServer] 的 server 侧行为。
type BizErrorBridgeServerOption func(*bizErrorBridgeServerConfig)

type bizErrorBridgeServerConfig struct {
	mapper BizErrorMapper
}

// WithBizErrorMapper 注册 server 侧错误映射器(需配合 [WithBizErrorBridgeServer]
// 透传使用,单独传入 kitex server.Option 列表无效)。映射在中间件出口对所有
// outbound error 单点生效,典型用法是把 pkg.AppError/业务码 error 统一收敛为
// 类型化业务错误,消除逐 return 点人工映射的漏点(#122)。
func WithBizErrorMapper(m BizErrorMapper) BizErrorBridgeServerOption {
	return func(cfg *bizErrorBridgeServerConfig) { cfg.mapper = m }
}

// bizErrorMapperMW 是 WithBizErrorMapper 的核心 middleware,抽出为包内
// helper 以便测试直接调(与 bizErrorBridgeMWBuilder 同模式)。
//
// 挂载点:server.WithMiddleware 注册的是 kitex 通用中间件(包裹 unary 与
// streaming 两条路径),位于 core middleware 之外——handler 抛的
// BizStatusError 在 core endpoint(invokeHandleEndpoint/streamHandleEndpoint)
// 内已被塞进 rpcinfo Invocation 并转为 nil,因此本 middleware 出口看到的
// 非 nil err 一定是**非类型化**错误(ErrBiz 包装的应用错误);映射后对称
// core endpoint 的桥路径:塞 rpcinfo + return nil,由
// transmeta.ServerTTHeaderHandler 写 biz-status 头。
func bizErrorMapperMW(mapper BizErrorMapper) endpoint.Middleware {
	return func(next endpoint.Endpoint) endpoint.Endpoint {
		return func(ctx context.Context, req, resp interface{}) error {
			err := next(ctx, req, resp)
			if err == nil {
				// err==nil:成功,或 handler 显式抛的类型化 BizStatusError
				// (core endpoint 已塞 rpcinfo)——mapper 不覆盖显式桥。
				return nil
			}
			if _, ok := kerrors.FromBizStatusError(err); ok {
				// 中间件链内显式抛的类型化错误:原样透传(#122 契约点)。
				return err
			}
			code, msg, mapped := mapper(err)
			if !mapped {
				return err
			}
			bizErr := kerrors.NewBizStatusError(code, msg)
			if ri := rpcinfo.GetRPCInfo(ctx); ri != nil {
				if setter, ok := ri.Invocation().(rpcinfo.InvocationSetter); ok {
					setter.SetBizStatusErr(bizErr)
					return nil
				}
			}
			// rpcinfo 不可写(非 RPC 上下文)时兜底:按类型化错误上抛。
			return bizErr
		}
	}
}

// bizErrorBridgeMWBuilder 是 WithBizErrorBridge 的核心 middleware 构造逻辑,
// 抽出为包内 helper 以便测试直接调,避免 reflect 解 client.Option。
func bizErrorBridgeMWBuilder(ctx context.Context) endpoint.Middleware {
	return func(next endpoint.Endpoint) endpoint.Endpoint {
		return func(ctx context.Context, req, resp interface{}) (err error) {
			err = next(ctx, req, resp)
			if err == nil {
				// 类型化业务错误在 middleware 链之后才附到 err(client.Call →
				// doFallbackIfNeeded),链内 err 恒 nil——从 rpcinfo 直接取,
				// 否则最主要的场景(类型化业务错误)恒不生效。
				if ri := rpcinfo.GetRPCInfo(ctx); ri != nil && ri.Invocation() != nil {
					if bizErr := ri.Invocation().BizStatusErr(); bizErr != nil {
						if biz, ok := FromKitexError(bizErr); ok {
							return biz
						}
					}
				}
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
