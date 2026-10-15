// Package kitexotel 提供 Kitex 客户端/服务端的 metainfo propagation 启用
// helper,以及 OTel 链路透传(通过 metainfo carrier)选项。
//
// 设计:
//   - NewClientOptions / NewServerOptions: 启用 kitex transmeta.Metainfo 透传,
//     业务可用 metainfo.WithValue(ctx, "x-trace-id", ...) 传自定义 key。
//   - NewTracingClientOptions: 在 metainfo 透传基础上叠加 OTel client propagation,
//     把 ctx 的 SpanContext 通过 otel.GetTextMapPropagator() 写到 metainfo,
//     下游 server 端可从 metainfo 提取并启 span。
//
// 集成方式(在 ekit 服务中):
//
//	import "github.com/oekit/tracing"
//	import kitexotel "github.com/oekit/kitex/observability/tracing/instrumentation/kitexotel"
//
//	func main() {
//	    // 1. 初始化全局 OTel tracer + propagator
//	    tracing.Init(tracing.Config{ServiceName: "my-svc", Endpoint: "otel:4317"})
//
//	    // 2. kitex client / server 启用 metainfo + OTel 透传
//	    cli, _ := myservice.NewClient("myservice", kitexotel.NewTracingClientOptions()...)
//	    svr := myservice.NewServer(handler, kitexotel.NewServerOptions()...)
//	}
//
// 官方依据:
//   - kitex-contrib obs-opentelemetry
//   - kitex tutorials/framework-exten/transmeta
//   - hertz-contrib obs-opentelemetry(对标 hertz 侧)
package kitexotel

import (
	"context"

	"github.com/bytedance/gopkg/cloud/metainfo"
	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/pkg/endpoint"
	"github.com/cloudwego/kitex/pkg/transmeta"
	"github.com/cloudwego/kitex/server"
	"github.com/cloudwego/kitex/transport"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

// NewClientOptions 返回启用 metainfo 透传的基础 client options。
// 业务可在 ctx 通过 metainfo.WithValue 注入自定义 KV,下游 server 端可读。
func NewClientOptions() []client.Option {
	return []client.Option{
		client.WithMetaHandler(transmeta.MetainfoClientHandler),
	}
}

// NewServerOptions 返回启用 metainfo 接收的基础 server options。
func NewServerOptions() []server.Option {
	return []server.Option{
		server.WithMetaHandler(transmeta.MetainfoServerHandler),
	}
}

// NewTracingClientOptions 启用 metainfo + OTel 客户端 ctx 透传:
// 把 ctx 中的 SpanContext 通过 otel.GetTextMapPropagator() 注入到
// outgoing metainfo。下游服务用 metainfo.NewIncomingContext 配合
// propagation.TextMapCarrier 提取并启 span(参考 [P2-3 kitexmeta])。
func NewTracingClientOptions() []client.Option {
	opts := NewClientOptions()
	// metainfo 依赖 TTHeader 传输协议携带(默认协议不上线,见 kitexmeta 同款修复)
	return append(opts,
		client.WithMiddlewareBuilder(otelClientMWBuilder),
		client.WithTransportProtocol(transport.TTHeader),
	)
}

// otelClientMWBuilder 客户端 middleware builder:把 ctx 的 SpanContext
// 注入到 outgoing metainfo。失败不影响 RPC,只 log warn。
func otelClientMWBuilder(_ context.Context) endpoint.Middleware {
	return func(next endpoint.Endpoint) endpoint.Endpoint {
		return func(ctx context.Context, req, resp interface{}) error {
			spanCtx := trace.SpanContextFromContext(ctx)
			if spanCtx.IsValid() {
				carrier := metainfoCarrier{values: make(map[string]string)}
				otel.GetTextMapPropagator().Inject(ctx, carrier)
				// 把 carrier values 写进 ctx metainfo(transmeta.MetainfoClientHandler
				// 已由 NewClientOptions 注册,会把 metainfo 自定义 KV 写进 outgoing
				// transport,完成 trace 透传);下游用 metainfo 取回后交给
				// propagator.Extract 恢复 SpanContext。
				//
				// 官方依据:kitex metainfo 机制
				// https://www.cloudwego.io/zh/docs/kitex/tutorials/basic-feature/observability/
				// 及 bytedance/gopkg/cloud/metainfo(WithValue 是公开 ctx API,
				// kitex transmeta 负责跨进程传递)。
				for k, v := range carrier.values {
					ctx = metainfo.WithValue(ctx, k, v)
				}
			}
			return next(ctx, req, resp)
		}
	}
}

// metainfoCarrier 把 metainfo KV 适配 propagation.TextMapCarrier 接口,
// 兼容 otel propagator 的 Inject/Extract 调用。
type metainfoCarrier struct {
	values map[string]string
}

// Get 返回 carrier 中的 value(供 Extract)。
func (c metainfoCarrier) Get(key string) string { return c.values[key] }

// Set 写 carrier 中的 value(供 Inject)。
func (c metainfoCarrier) Set(key, value string) { c.values[key] = value }

// Keys 返回所有 key(供 Extract enumerate)。
func (c metainfoCarrier) Keys() []string {
	keys := make([]string, 0, len(c.values))
	for k := range c.values {
		keys = append(keys, k)
	}
	return keys
}
