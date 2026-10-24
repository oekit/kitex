// Package kitexotel server 端补全。P2-2 实现了 client 端 ctx 透传,
// 本文件补 P2-3 计划: 直接透传 tracing.NewServerSuite,让 ekit OTel
// 在 kitex server 端跟 client 端对称。
//
// 官方依据:https://github.com/kitex-contrib/obs-opentelemetry README
// "Server usage" 段,server 端走:
//
//	svr := echo.NewServer(
//	    new(EchoImpl),
//	    server.WithSuite(tracing.NewServerSuite()),
//	    server.WithServerBasicInfo(&rpcinfo.EndpointBasicInfo{...}),
//	)
//
// ekit 命名空间: kitexotel.NewTracingServerOptions() 等价于上面
// 完整用法,把 WithSuite 包装成 []server.Option,业务 1 行挂载。
package kitexotel

import (
	"github.com/cloudwego/kitex/server"
	kitexoteltracing "github.com/kitex-contrib/obs-opentelemetry/tracing"
	"go.opentelemetry.io/otel/propagation"
)

// NewServerSuite 透传 kitex-contrib/obs-opentelemetry/tracing.NewServerSuite。
// 业务用 server.WithSuite(kitexotel.NewServerSuite(...)) 启用 server
// 端 OTel span。
//
// 跟 P2-2 client 端对称:server 端接收 metainfo 携带的 trace header,
// 提取 SpanContext 并启 root span(默认 server name = RPC method)。
//
// 业务用法:
//
//	svr := myservice.NewServer(handler,
//	    kitexotel.NewServerSuite(), // 默认全局 provider + W3C propagator
//	)
//	// 或拆开拿 []server.Option:
//	svr := myservice.NewServer(handler, kitexotel.NewTracingServerOptions()...)
//	// 自定义 provider/propagator 经上游 kitexoteltracing option 透传
//	// (ServerOption type alias;上游 v0.3.0 无 WithTracerProvider Option)
//
// 注意: 上游 NewServerSuite 返回 unexported *serverSuite,ekit
// 用 server.Suite interface 包一层,业务不能直接转回 *serverSuite;
// 业务用 server.WithSuite() 接受。
func NewServerSuite(opts ...ServerOption) server.Suite {
	return NewServerTracingSuite(opts...)
}

// NewTracingServerOptions 把 NewServerSuite 包装成 []server.Option,
// 业务用 NewServer(handler, NewTracingServerOptions(...)...) 启用
// OTel server 端 tracing。
//
// 跟 NewServerOptions 区别: 后者只设 metainfo,前者 + 上 OTel server
// middleware(走 obs-opentelemetry/tracing)。
func NewTracingServerOptions(opts ...ServerOption) []server.Option {
	return []server.Option{
		server.WithSuite(NewServerSuite(opts...)),
	}
}

// ServerOption 是 kitex-contrib/obs-opentelemetry/tracing.Option 的
// type alias(避免业务 import 长串上游 path)。
type ServerOption = kitexoteltracing.Option

// WithTextMapPropagator 是 ekit 重新导出的 kitexoteltracing.WithTextMapPropagator。
func WithTextMapPropagator(p propagation.TextMapPropagator) ServerOption {
	return kitexoteltracing.WithTextMapPropagator(p)
}

// WithRecordSourceOperation 透传 kitexoteltracing.WithRecordSourceOperation。
func WithRecordSourceOperation(b bool) ServerOption {
	return kitexoteltracing.WithRecordSourceOperation(b)
}

// WithEnableGRPCMetadata 透传 kitexoteltracing.WithEnableGRPCMetadata。
func WithEnableGRPCMetadata() ServerOption {
	return kitexoteltracing.WithEnableGRPCMetadata()
}

// 注意: kitex-contrib/obs-opentelemetry v0.3.0 没有 WithTracerProvider
// option,TracerProvider 走 otel.GetTracerProvider()(全局)。业务先
// tracing.Init(tp) 设全局,然后 NewServerSuite 即可生效。
