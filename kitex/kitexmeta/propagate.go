// Package kitexmeta 桥接 kitex transmeta 体系 + ekit traceid,让
// trace_id / 用户身份 / 空间码等业务元数据在 kitex server 入口注入
// 到 ctx,通过 kitex client transmeta 自动透传到下游 server。
//
// 设计:
//   - 配合 kitexotel(P2-2 client / P9-1 server): OTel trace + 自定义
//     metainfo 全链路
//   - 配合 ekit middleware/traceid: 业务在 gin/hertz 入口调
//     traceid.ContextWith 即可,无需在每个 RPC client 手动注入
//
// 官方依据:
//   - https://www.cloudwego.io/zh/docs/kitex/tutorials/framework-exten/transmeta/
//   - kitex/pkg/transmeta 公开 API: MetainfoClientHandler / MetainfoServerHandler
//   - github.com/bytedance/gopkg/cloud/metainfo 公共 API: WithPersistentValue(全链路透传)
package kitexmeta

import (
	"context"

	"github.com/bytedance/gopkg/cloud/metainfo"
	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/pkg/endpoint"
	"github.com/cloudwego/kitex/pkg/transmeta"
	"github.com/cloudwego/kitex/transport"
)

// MetaKeyExtractor 是从 ctx 提取业务 metainfo key-value 的函数。
// 业务可自定义 extractor 把 ekit traceid / 用户身份 / 空间码
// 转成多个 metainfo。
type MetaKeyExtractor func(ctx context.Context) (key, value string, ok bool)

// traceIDMetaKey 是跨进程 trace_id 的 metainfo key。
// 包内固定读取该 key;不读业务可变全局,消除并发改写的数据竞争面
// (2026-09-15 治理轮:原导出可变全局 DefaultMetaKeys 除 [0] 外既不注入
// 也无人读取,已移除;业务自定义 MetaKeyExtractor 时建议沿用 x-* 命名:
// x-trace-id / x-request-id / x-user-id / x-space-code / x-tenant-id)。
const traceIDMetaKey = "x-trace-id"

// traceIDKey 是 ekit 自己的 traceid ctx key(避免 import middleware/traceid 循环)。
type traceIDKey struct{}

// ContextWithTraceID 把 trace_id 注入 ctx(ekit 内部 helper,跟
// middleware/traceid.ContextWith 等价但 ekit SDK 基础包禁止 import 上层包)。
// 业务用 metainfo.ContextWithTraceID(ctx, id) 替代。
func ContextWithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDKey{}, traceID)
}

// TraceIDFromContext 提取 ctx 里 ekit 注入的 trace_id。
// 两级来源:① 同进程 ContextWithTraceID 注入的 ctx key;② 跨进程场景 —
// server 端收到的 metainfo persistent 值(2026-09-06 e2e 修复:原实现只读
// ①,服务端永远拿不到对端透传的 trace_id;且 metainfo.GetValue 不读
// persistent 存储,必须用 GetPersistentValue)。
func TraceIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(traceIDKey{}).(string); ok && v != "" {
		return v
	}
	if v, ok := metainfo.GetPersistentValue(ctx, traceIDMetaKey); ok && v != "" {
		return v
	}
	return ""
}

// FromContextExtractor 默认 extractor:从 ctx 读 ekit traceid(由
// ContextWithTraceID 注入),key 用 "x-trace-id"。
func FromContextExtractor(ctx context.Context) (string, string, bool) {
	v, ok := ctx.Value(traceIDKey{}).(string)
	if !ok || v == "" {
		return "", "", false
	}
	return traceIDMetaKey, v, true
}

// PropagateMiddlewareBuilder 构造 client.MiddlewareBuilder,每次 RPC
// 调用前把 ekit ctx 里的 metainfo 注入到 kitex outgoing ctx。
//
// 业务用法:
//
//	cli, _ := myservice.NewClient("myservice",
//	    client.WithMiddlewareBuilder(kitexmeta.PropagateMiddlewareBuilder()),
//	    client.WithMetaHandler(transmeta.MetainfoClientHandler),
//	    ...
//	)
//
// 工作机制:
//  1. client middleware 在请求时调 builder,拿到 endpoint.Middleware
//  2. middleware 在 next(ctx, req, resp) 之前,读 ctx 里的 trace_id
//     等 key, 用 metainfo.WithPersistentValue 注入到 ctx
//  3. 注入后的 ctx 传给 next,transmeta.MetainfoClientHandler 在 WriteMeta
//     阶段从 ctx 读 metainfo 写到 outgoing transport header
//  4. 下游 server 收到 header 后,transmeta.MetainfoServerHandler 在
//     ReadMeta 阶段从 transport header 读 metainfo 重新放回 ctx
//  5. 业务在下游 server handler 调 kitexmeta.TraceIDFromContext 拿到
func PropagateMiddlewareBuilder() endpoint.MiddlewareBuilder {
	return func(ctx context.Context) endpoint.Middleware {
		return func(next endpoint.Endpoint) endpoint.Endpoint {
			return func(ctx context.Context, req, resp interface{}) error {
				if key, val, ok := FromContextExtractor(ctx); ok {
					ctx = metainfo.WithPersistentValue(ctx, key, val)
				}
				return next(ctx, req, resp)
			}
		}
	}
}

// WithMetaKeyExtractors 返回带自定义 extractor 列表的 MiddlewareBuilder。
// 业务可注入 user_id / space_code 等:
//
//	cli, _ := myservice.NewClient("myservice",
//	    client.WithMiddlewareBuilder(
//	        kitexmeta.WithMetaKeyExtractors(
//	            func(ctx context.Context) (string, string, bool) {
//	                if v := getUserIDFromCtx(ctx); v != "" {
//	                    return "x-user-id", v, true
//	                }
//	                return "", "", false
//	            },
//	            func(ctx context.Context) (string, string, bool) {
//	                if v := getSpaceCodeFromCtx(ctx); v != "" {
//	                    return "x-space-code", v, true
//	                }
//	                return "", "", false
//	            },
//	        ),
//	    ),
//	)
func WithMetaKeyExtractors(extractors ...MetaKeyExtractor) endpoint.MiddlewareBuilder {
	return func(ctx context.Context) endpoint.Middleware {
		return func(next endpoint.Endpoint) endpoint.Endpoint {
			return func(ctx context.Context, req, resp interface{}) error {
				for _, extract := range extractors {
					if k, v, ok := extract(ctx); ok {
						ctx = metainfo.WithPersistentValue(ctx, k, v)
					}
				}
				return next(ctx, req, resp)
			}
		}
	}
}

// PropagateClientOption 便捷 helper:把 builder + MetaHandler + TTHeader
// 装一起,业务用 client.WithOptions(kitexmeta.PropagateClientOption(...))
// 一行挂载。
//
// ⚠️ 必须显式设 TTHeader 传输协议(2026-09-06 e2e 修复):kitex client 默认
// 传输协议不携带 TransInfo,metainfo 根本不会上线 — 原实现漏设导致透传
// 整体断链(文档宣称的"一行挂载"实际不工作)。
func PropagateClientOption(extractors ...MetaKeyExtractor) []client.Option {
	mwb := PropagateMiddlewareBuilder()
	if len(extractors) > 0 {
		mwb = WithMetaKeyExtractors(extractors...)
	}
	return []client.Option{
		client.WithMiddlewareBuilder(mwb),
		client.WithMetaHandler(transmeta.MetainfoClientHandler),
		client.WithTransportProtocol(transport.TTHeader),
	}
}
