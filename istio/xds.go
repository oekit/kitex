// Package istio 提供 Kitex xDS / Proxyless / Istio 集成的 ekit 桥接。
//
// 官方依据:https://github.com/kitex-contrib/xds (v0.4.2) + Istio
// "Kitex Proxyless" 文档 (https://www.cloudwego.io/zh/docs/kitex/\
// tutorials/service-governance/proxyless/)。
//
// 背景:Kitex 可以 Proxyless 模式对接 Istio(无 sidecar),通过 xDS
// 协议从 istiod 拉路由/限流/熔断等配置,ekit 通过本包 Init 即可启用。
//
// 业务用法(典型部署):
//
//	// 启动:连接 istiod(要求 K8s bootstrap 环境变量;对不可达 istiod
//	// 会阻塞重试,不能当连通性检查用 — 详见 InitXDS 文档)
//	_ = istio.InitXDS(istio.WithXDSServerAddress("istiod.istio-system:15010"))
//
//	// 客户端:动态熔断(替代静态配置)
//	cli, _ := myservice.NewClient("myservice",
//	    istio.NewXDSClientOption(),
//	    client.WithMiddleware(...),
//	)
//
//	// 启动 server:ekit 已有 NewServerOptions(走 metainfo),无需 xDS 接入
//
// 设计选择:
//   - xds 是 kitex 专属(对 hertz 没用,hertz 用 hertz-contrib/mesh 适配
//     istio,本仓库暂不桥接)
//   - Init 幂等:多次调用安全(实际只第一次生效,后续返回 nil)
package istio

import (
	"github.com/cloudwego/kitex/client"
	"github.com/kitex-contrib/xds"
	xdsmanager "github.com/kitex-contrib/xds/core/manager"
	xdssuite "github.com/kitex-contrib/xds/xdssuite"
	"sync"
)

// XDSServerConfig 是 xds/core/manager.XDSServerConfig 的 type alias,
// 业务用 istio.XDSServerConfig{} 构造配置 + WithXDSServerConfig 注入。
type XDSServerConfig = xdsmanager.XDSServerConfig

// XDSOption 是 xds 包的 manager.Option 的 type alias。
type XDSOption = xdsmanager.Option

// InitXDS 初始化 xDS 资源管理器(连 istiod 拉 LDS/RDS/CDS/EDS)。
// 业务需在启动 Kitex server/client 之前调一次;多次调安全(幂等)。
//
// ⚠️ 行为语义(2026-09-06 实测修正,勿想当然):
//   - 构造期要求 K8s bootstrap 环境变量(POD_NAMESPACE/POD_NAME/
//     INSTANCE_IP 等),缺失直接返回 error;
//   - bootstrap 通过后,对不可达 istiod 会**阻塞重试**(不是立即返回
//     连接错误)— 不能把 InitXDS 当启动期连通性检查;生产环境 istiod
//     地址务必正确,开发环境无 istiod 时不要调用。
//
// 官方依据: kitex-contrib/xds README + 源码
// initOnce 串行化 xds.Init：上游 xds.Init 的幂等是 check-then-act
// （XDSInited 检查与 SetXDSResourceManager 之间无互斥），并发调用会各自
// 建一个 ADS 连接 + 常驻 cleaner goroutine，其中一个被上游静默丢弃——
// 连接与 goroutine 泄漏。多 Init 只保留首个结果。
var initOnce sync.Once

var initXDSErr error

func InitXDS(opts ...XDSOption) error {
	initOnce.Do(func() { initXDSErr = xds.Init(opts...) })
	return initXDSErr
}

// WithXDSServerAddress 透传 kitex-contrib/xds.WithXDSServerAddress。
// 业务用:
//
//	istio.InitXDS(istio.WithXDSServerAddress("istiod.istio-system:15010"))
func WithXDSServerAddress(address string) XDSOption {
	return xds.WithXDSServerAddress(address)
}

// WithXDSServerConfig 透传 kitex-contrib/xds.WithXDSServerConfig。
// 业务需要自定义 Node ID / Cert path 等高级配置时用。
func WithXDSServerConfig(cfg *XDSServerConfig) XDSOption {
	return xds.WithXDSServerConfig(cfg)
}

// NewXDSClientOption 桥接 xds/xdssuite.NewCircuitBreaker,作为 kitex
// client option 使用 — 动态熔断配置从 istiod 拉取,不需要业务写静态规则。
//
// 业务用法:
//
//	cli, _ := myservice.NewClient("myservice",
//	    istio.NewXDSClientOption(),
//	    ...
//	)
//
// 返回 client.Option,业务直接挂到 kitex client 上。
func NewXDSClientOption(opts ...XDSClientOption) client.Option {
	return xdssuite.NewCircuitBreaker(opts...)
}

// XDSClientOption 是 xds/xdssuite.Option 的 type alias(避免业务
// import 长串上游 path;后续暴露熔断/重试等 Option 时统一用)。
type XDSClientOption = xdssuite.Option
