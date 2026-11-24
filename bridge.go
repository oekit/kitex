package kitex

import (
	"context"
	stderrors "errors"
	"strings"
	"sync"

	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/pkg/endpoint"
	"github.com/cloudwego/kitex/pkg/kerrors"
	"github.com/cloudwego/kitex/pkg/retry"
	"github.com/cloudwego/kitex/pkg/rpcinfo"
	"github.com/cloudwego/kitex/pkg/transmeta"
	"github.com/cloudwego/kitex/pkg/utils"
	"github.com/cloudwego/kitex/server"
	"github.com/cloudwego/kitex/transport"

	"github.com/oekit/logx"
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
// # ⚠️ 禁止与 retry.AllErrorRetry() 同用(#315)
//
// kitex 的 result-retry 谓词对**任意非 nil error** 成立时就会重试,而
// [retry.AllErrorRetry] 正是 `err != nil` 的官方 common choice。开启本桥
// 后,类型化业务错误(余额不足/重复下单等)被改写成非 nil *BizError——重试
// 容器会把它当成可重试失败,在支付/下单这类非幂等 RPC 上造成重复扣款。
//
// 本 option 在构造期用一枚合成 *BizError 探测 result-retry 谓词(覆盖
// `WithSpecifiedResultRetry`、`WithFailureRetry(...)`/`WithRetryMethodPolicies`
// 内嵌的 FailurePolicy/MixedPolicy),命中即告警一次。探测本身不会打挂构造
// (谓词 panic 按不冲突吞掉);为避免漏报,自定义谓词仍必须自行排除 *BizError:
//
//	// ❌ 业务失败会被重放
//	client.WithSpecifiedResultRetry(retry.AllErrorRetry())
//
//	// ✅ 只重试链路错误
//	client.WithSpecifiedResultRetry(&retry.ShouldResultRetry{
//	    ErrorRetryWithCtx: func(ctx context.Context, err error, ri rpcinfo.RPCInfo) bool {
//	        if _, isBiz := ekiterr.AsBizError(err); isBiz {
//	            return false
//	        }
//	        return err != nil
//	    },
//	})
//
// 重试容器(含 [NewClientSuite] 的 Nacos 热更 retry)同样消费该谓词,组合时
// 按同一口径排除 *BizError。
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
		// TailOption 在全部 client.Option 应用完之后执行：WithSpecifiedResultRetry
		// / WithFailureRetry 无论排在本 option 之前还是之后都已落进 Options，
		// 此时检测才能覆盖完整组合（#315）。
		o.TailOptions = append(o.TailOptions, client.Option{F: func(o *client.Options, di *utils.Slice) {
			warnBizErrorRetryConflict(o)
		}})
	}}
}

// bizBridgeLog 是 BizError 桥接的控制面日志（构造期告警）。
var bizBridgeLog = logx.Delegating("kitex-bridge")

// bizErrorRetryWarnOnce 保证进程内只告警一次（同一配置可能构造多个 client）。
var bizErrorRetryWarnOnce sync.Once

// retriesBizError 用一枚合成 *BizError 探测 result-retry 谓词：返回 true
// 表示该谓词会把桥接后的业务错误当成可重试失败（retry.AllErrorRetry 的
// `err != nil` 即命中）。
//
// 不用函数指针比对：闭包代码指针在不同 Go 版本/内联决策下不稳定（CI 已
// 实证 1.26 与 1.27 行为不同），语义探测与「谓词实际看到 *BizError」完全
// 同口径。谓词契约是纯判定函数，探测一次可接受；若自定义谓词 panic（如
// 解引用 nil ri），按不冲突处理并吞掉——诊断探测不能把 client 构造打挂。
func retriesBizError(rr *retry.ShouldResultRetry) (retried bool) {
	if rr == nil || rr.ErrorRetryWithCtx == nil {
		return false
	}
	probeErr := ekiterr.NewBizError(40001, "biz-error-bridge probe")
	probeRI := rpcinfo.NewRPCInfo(
		rpcinfo.NewEndpointInfo("probe-from", "Probe", nil, nil),
		rpcinfo.NewEndpointInfo("probe-to", "Probe", nil, nil),
		rpcinfo.NewInvocation("probe", "Probe"),
		rpcinfo.NewRPCConfig(),
		rpcinfo.NewRPCStats(),
	)
	defer func() {
		if recover() != nil {
			retried = false
		}
	}()
	return rr.ErrorRetryWithCtx(context.Background(), probeErr, probeRI)
}

// bizErrorRetryConflictSources 返回与 *BizError 冲突的 result-retry 配置来源。
// 覆盖 client.WithSpecifiedResultRetry 与 WithFailureRetry/WithRetryMethodPolicies
// 内嵌的 FailurePolicy/MixedPolicy；RetryContainer 的谓词由 client.Init 从
// UnaryOptions.RetryWithResult 注入，因此同一检测对 Suite 注入的容器生效。
func bizErrorRetryConflictSources(o *client.Options) []string {
	if o == nil {
		return nil
	}
	var sources []string
	if retriesBizError(o.UnaryOptions.RetryWithResult) {
		sources = append(sources, "client.WithSpecifiedResultRetry(...) retries *BizError")
	}
	for method, p := range o.UnaryOptions.RetryMethodPolicies {
		if p.FailurePolicy != nil && retriesBizError(p.FailurePolicy.ShouldResultRetry) {
			sources = append(sources, "retry policy method="+method)
		}
		if p.MixedPolicy != nil && retriesBizError(p.MixedPolicy.ShouldResultRetry) {
			sources = append(sources, "mixed retry policy method="+method)
		}
	}
	return sources
}

// warnBizErrorRetryConflict 在构造期检测「BizError 桥 + 会重试 *BizError 的
// result-retry 谓词」（retry.AllErrorRetry 是常见命中）并告警一次。无法据此
// 自动关闭重试（谓词对任意非 nil error 成立），故只做显式信号；godoc 已
// 明确禁止同用并给出排除 *BizError 的写法。
func warnBizErrorRetryConflict(o *client.Options) {
	sources := bizErrorRetryConflictSources(o)
	if len(sources) == 0 {
		return
	}
	bizErrorRetryWarnOnce.Do(func() {
		bizBridgeLog.Warn("WithBizErrorBridge conflicts with a result-retry predicate that retries *BizError (e.g. retry.AllErrorRetry): business failures (payment/order) may be replayed; exclude *BizError from the retry predicate",
			"sources", strings.Join(sources, ", "))
	})
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
		return func(ctx context.Context, req, resp any) error {
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
		return func(ctx context.Context, req, resp any) (err error) {
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
//   - kerrors.ErrOverlimit 哨兵族(ErrConnOverLimit / ErrQPSOverLimit,服务端
//     限流/连接数超限拒绝):BizCode=50300, HTTPCode=503(#300,对齐 middleware
//     shedding 口径)。
//   - *kerrors.DetailedError 兜底:BizCode=50000, HTTPCode=500, Message 为
//     固定短文案 "rpc error"(#298)。
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

	// 3.5 服务端限流/连接数超限拒绝(#300) — 对齐 middleware shedding 口径
	// (BizCode 50300 / HTTP 503,#291 三轨收敛:过载拒绝统一 50300)。
	// 一个 errors.Is 覆盖哨兵族:ErrConnOverLimit / ErrQPSOverLimit 都是
	// ErrOverlimit.WithCause 派生的 DetailedError(DetailedError.Is 命中
	// de.basic == ErrOverlimit)。
	//
	// ⚠️ 可达性调查结论(探针实证,完整证据链见 issue #300):kitex 默认
	// thrift ping-pong(server.WithLimit 路径,QPSLimitPostDecode=false)下,
	// QPS 拒绝发生在解码前的 pipeline OnRead——服务端直接关连接、不写任何
	// 错误响应,client 收到 DetailedError(ErrRemoteOrNetwork:"... EOF peer
	// close");连接数超限(OnActive)同样只关连接。ErrOverlimit 哨兵是
	// server 侧构造,kerrors 哨兵不随线上传输(typeID=6 InternalError 与
	// 通用内部错误共用,无独立类型位),client 侧 errors.Is 永不命中——本段
	// 是「可达的最近语义」:哨兵族一旦出现在 client 错误链(进程内构造、
	// kitex 未来版本回传类型化错误、gRPC ResourceExhausted 反向映射等)即
	// 正确归类为 50300,不再被 DetailedError 兜底误渲染成 50000/500"内部
	// 错误"。不做文本匹配("request over limit"):误报面大且 kitex 未承诺
	// 该文案契约。
	if stderrors.Is(err, kerrors.ErrOverlimit) {
		return ekiterr.WrapBizErrorWithStatus(50300, 503, "request over limit", err), true
	}

	// 4. DetailedError 兜底(RPC 异常的 StandardError + cause 链)
	// Message 用固定短文案(#298):de.Error() 会携带本地传输层细节/远端原始
	// 错误链全文,该 BizError 冒泡到 HTTP 侧时 writeBizError 会把 Message
	// 原样回显进响应体,与 middleware.Fail「cause 不回显防内部细节泄漏」
	// 的教义冲突。脱敏后排障信息不丢:原文经 BizError.Cause(Unwrap 链)可达,
	// Fail 落 cause 日志即可定位。
	var de *kerrors.DetailedError
	if stderrors.As(err, &de) {
		return ekiterr.WrapBizErrorWithStatus(50000, 500, "rpc error", err), true
	}

	return nil, false
}
