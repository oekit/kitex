// Package chainmw 把 core/chain 中间件栈接入 Kitex middleware 链。
//
// 形态差异：kitex endpoint.Endpoint 是 (ctx, req, resp any) error 的出参形态，
// 而 chain.Handler 是 (ctx, req any) (any, error)。ToKitex 经 ctx 携带 resp
// 与 next（包内私有键），chain.Handler 终端闭包取出后调用 next 并把真实
// 错误透传——出参回填与错误语义均与 kitex 直连一致。
//
// 对 core/transport 的依赖由 transportx 承载（ServerContext 经
// transportx.ServerContext → transport.NewServerContext 注入 server
// Transporter，ctx 契约与 hertz 侧一致）；本包不直接 import transport。
//
// # 自动注入 transport 上下文（#315）
//
// ToKitex 在链首自动注入 server Transporter（缓存内已注入则保留），
// Operation/Kind 从 rpcinfo 推导：
//
//   - operation：gRPC 为 "/{Service}/{Method}"，Thrift 为 "{Service}.{Method}"
//     （Service 取 rpcinfo.To().ServiceName()，Method 取
//     rpcinfo.Invocation().MethodName()）；
//   - kind：rpcinfo.Config().TransportProtocol() 含 transport.GRPC 位为
//     "grpc"，否则为 [transportx.KindThrift]。
//
// 因此漏挂 [ServerContext] 时 AccessLog 的 op 不再是 "-"，SkipPaths /
// RedactPathPrefixes 恢复生效（token 不再整体落日志）；[ServerContext]
// 保留为显式覆盖（外层注入生效、或挂在内层覆盖自动推导值）。
//
// # 传输能力矩阵（kitex 侧 metainfo）
//
// 能力                     | TTHeader / TTHeaderFramed / gRPC | Framed / PurePayload
// -------------------------|----------------------------------|---------------------
// 请求头读取与写透          | 支持（默认 meta handler）          | 不支持
// ReplyHeader 服务端回传    | 支持（metainfo backward API）      | 不支持
//
// Kitex v0.13+ 缺省传输协议是 Framed（无 trans-info 通道）：需要元数据
// 透传/回传时显式配 client.WithTransportProtocol(transport.TTHeaderFramed)
// 与 server.WithMetaHandler(transmeta.ServerTTHeaderHandler)（
// kitex.WithBizErrorBridge / kitex.WithBizErrorBridgeServer 已自动附带）。
// 客户端接收回传值前需 metainfo.WithBackwardValues(ctx) 标记接收桶。
//
// 用法：
//
//	svr := server.New(server.WithMiddleware(chainmw.ToKitex(chain.Recovery())))
package chainmw

import (
	"context"
	"errors"

	"github.com/cloudwego/kitex/pkg/rpcinfo"
	ktransport "github.com/cloudwego/kitex/transport"
	"github.com/oekit/core/chain"
	"github.com/oekit/core/transport"
	"github.com/oekit/kitex/transportx"
)

type (
	// respKey 携带 kitex 出参 resp。
	respKey struct{}
	// nextKey 携带还原的 kitex next endpoint。
	nextKey struct{}
)

type kitexEndpoint func(ctx context.Context, req, resp any) error
type kitexMiddleware func(next kitexEndpoint) kitexEndpoint

var (
	errRespMissing = errors.New("kitex/chainmw: resp missing in ctx (internal bridging broken)")
	errNextMissing = errors.New("kitex/chainmw: next missing in ctx (internal bridging broken)")
)

// ToKitex 把 chain 栈接入 Kitex：返回 kitex endpoint.Middleware。
// req/resp 原样透传（resp/next 经 ctx 供终端闭包使用），链返回的错误
// 透传给 kitex（nil = 成功，与 kitex 语义一致）。
func ToKitex(mw chain.Middleware) kitexMiddleware {
	return func(next kitexEndpoint) kitexEndpoint {
		terminal := func(ctx context.Context, req any) (any, error) {
			resp := ctx.Value(respKey{})
			if resp == nil {
				return nil, errRespMissing
			}
			nx, _ := ctx.Value(nextKey{}).(kitexEndpoint)
			if nx == nil {
				return nil, errNextMissing
			}
			return nil, nx(ctx, req, resp)
		}
		h := mw(terminal)
		return func(ctx context.Context, req, resp any) error {
			ctx = withServerTransport(ctx)
			ctx = context.WithValue(ctx, respKey{}, resp)
			ctx = context.WithValue(ctx, nextKey{}, next)
			_, err := h(ctx, req)
			return err
		}
	}
}

// withServerTransport 在 ctx 尚无 server Transporter 时按 rpcinfo 自动注入。
//
// 已注入（例如外层显式挂了 [ServerContext]）时原样保留，让显式覆盖优先；
// rpcinfo 缺失（非 RPC ctx / 单测直调）时不做注入，链行为与修改前一致。
func withServerTransport(ctx context.Context) context.Context {
	if _, ok := transport.FromServerContext(ctx); ok {
		return ctx
	}
	ri := rpcinfo.GetRPCInfo(ctx)
	if ri == nil || ri.To() == nil || ri.Invocation() == nil {
		return ctx
	}
	svc := ri.To().ServiceName()
	method := ri.Invocation().MethodName()
	kind := serverKind(ri)
	op := operation(kind, svc, method)
	if op == "" {
		return ctx
	}
	if svc == "" {
		svc = "kitex"
	}
	return transportx.ServerContext(ctx, svc, op, transportx.WithServerKind(kind))
}

// serverKind 从 rpcinfo 的传输协议位判定 Kind：gRPC 位 → grpc，其余 → thrift。
func serverKind(ri rpcinfo.RPCInfo) transport.Kind {
	if cfg := ri.Config(); cfg != nil && cfg.TransportProtocol()&ktransport.GRPC != 0 {
		return transport.KindGRPC
	}
	return transportx.KindThrift
}

// operation 组装框架无关的 Operation：gRPC 为 "/svc/method"，Thrift 为
// "svc.method"；service 缺失时退化为 method，method 缺失时退化为 service。
func operation(kind transport.Kind, service, method string) string {
	if service == "" {
		return method
	}
	if method == "" {
		return service
	}
	if kind == transport.KindGRPC {
		return "/" + service + "/" + method
	}
	return service + "." + method
}

// ServerContext 返回注入 server Transporter 的 kitex middleware（header 元数据
// 通道 = metainfo），供框架无关中间件在 kitex 侧读取 Kind/Operation。
//
// ToKitex 已自动注入（从 rpcinfo 推导 Kind/Operation），本中间件保留为
// 显式覆盖手段：方法名不再是必填项，仅在需要改写 operation 时使用。
func ServerContext(method string) kitexMiddleware {
	return func(next kitexEndpoint) kitexEndpoint {
		return func(ctx context.Context, req, resp any) error {
			kind := transport.KindGRPC
			if ri := rpcinfo.GetRPCInfo(ctx); ri != nil {
				kind = serverKind(ri)
			}
			return next(transportx.ServerContext(ctx, "discovery:///kitex", method,
				transportx.WithServerKind(kind)), req, resp)
		}
	}
}
