// Package chainmw 把 core/chain 中间件栈接入 Kitex middleware 链。
//
// 形态差异：kitex endpoint.Endpoint 是 (ctx, req, resp any) error 的出参形态，
// 而 chain.Handler 是 (ctx, req any) (any, error)。ToKitex 经 ctx 携带 resp
// 与 next（包内私有键），chain.Handler 终端闭包取出后调用 next 并把真实
// 错误透传——出参回填与错误语义均与 kitex 直连一致。
//
// 用法：
//
//	svr := server.New(server.WithMiddleware(chainmw.ToKitex(chain.Recovery())))
package chainmw

import (
	"context"
	"errors"

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
			ctx = context.WithValue(ctx, respKey{}, resp)
			ctx = context.WithValue(ctx, nextKey{}, next)
			_, err := h(ctx, req)
			return err
		}
	}
}

// ServerContext 返回注入 server Transporter 的 kitex middleware（header 元数据
// 通道 = metainfo），供框架无关中间件在 kitex 侧读取 Kind/Operation。
func ServerContext(method string) kitexMiddleware {
	return func(next kitexEndpoint) kitexEndpoint {
		return func(ctx context.Context, req, resp any) error {
			return next(transportx.ServerContext(ctx, "discovery:///kitex", method), req, resp)
		}
	}
}

// 确保 transport 引用不悬空（ServerContext 已间接依赖）。
var _ = transport.NoopHeader{}
