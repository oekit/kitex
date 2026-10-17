package kitex

import (
	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/server"
	"github.com/kitex-contrib/obs-opentelemetry/tracing"
)

// NewServerTracingSuite 返回 Kitex Server 的 OpenTelemetry tracing suite。
//
// 前提：ekit [tracing.Init] 已在 bootstrap 阶段调用，OTel 全局 TracerProvider/Propagator 已设置。
// 该 Suite 会注入 tracing middleware + TTHeader/HTTP2 meta handler。
func NewServerTracingSuite(opts ...tracing.Option) server.Suite {
	return tracing.NewServerSuite(opts...)
}

// NewClientTracingSuite 返回 Kitex Client 的 OpenTelemetry tracing suite。
//
// 前提：ekit [tracing.Init] 已在 bootstrap 阶段调用，OTel 全局 TracerProvider/Propagator 已设置。
// 该 Suite 会注入 tracing middleware + TTHeader/HTTP2 meta handler + transport 协议。
func NewClientTracingSuite(opts ...tracing.Option) client.Suite {
	return tracing.NewClientSuite(opts...)
}
