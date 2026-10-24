package kitexotel

import (
	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/server"
	kitexoteltracing "github.com/kitex-contrib/obs-opentelemetry/tracing"
)

// ClientOption 是 kitex-contrib/obs-opentelemetry/tracing.Option 的
// type alias（与 [ServerOption] 同源类型——上游 client/server suite 共用
// 一套 Option；分两个名字只为避免 co-import 时的裸 Option 歧义）。
type ClientOption = kitexoteltracing.Option

// NewServerTracingSuite 返回 Kitex Server 的 OpenTelemetry tracing suite。
//
// 前提：ekit [tracing.Init] 已在 bootstrap 阶段调用，OTel 全局
// TracerProvider/Propagator 已设置。该 Suite 会注入 tracing middleware
// + TTHeader/HTTP2 meta handler。
//
// ⚠️ v0.7.0 起本函数从**根包** `github.com/oekit/kitex` 迁到本嵌套模块：
// 根包依赖面里 obs-opentelemetry 会把 apache/thrift 抬到 v0.16.0，打断
// 按 thrift 0.13 生成的 Thrift 代码（#37）。迁移写法：
//
//	// 旧：kitex.NewServerTracingSuite()
//	// 新：
//	kitexotel.NewServerTracingSuite()
//
// 与 [NewServerSuite] 等价（同一上游 suite），保留两个名字只为让根包
// 使用方一行改包名即可迁移。
func NewServerTracingSuite(opts ...ServerOption) server.Suite {
	return kitexoteltracing.NewServerSuite(opts...)
}

// NewClientTracingSuite 返回 Kitex Client 的 OpenTelemetry tracing suite。
//
// 前提：ekit [tracing.Init] 已在 bootstrap 阶段调用，OTel 全局
// TracerProvider/Propagator 已设置。该 Suite 会注入 tracing middleware
// + TTHeader/HTTP2 meta handler + transport 协议。
//
// ⚠️ v0.7.0 起本函数从**根包** `github.com/oekit/kitex` 迁到本嵌套模块
// （理由同 [NewServerTracingSuite]）。迁移写法：
//
//	// 旧：kitex.NewClientTracingSuite()
//	// 新：
//	kitexotel.NewClientTracingSuite()
func NewClientTracingSuite(opts ...ClientOption) client.Suite {
	return kitexoteltracing.NewClientSuite(opts...)
}
