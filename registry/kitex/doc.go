// Package kitex 提供 ekit registry 抽象到 CloudWeGo Kitex 框架的适配器。
//
// 本包将 ekit 的 [registry.Registrar] 和 [registry.Discoverer] 接口桥接到
// Kitex 的 [registry.Registry] 和 [discovery.Resolver]，使 Kitex Server/Client
// 可以复用 ekit 的服务注册与发现能力（当前支持 Nacos，未来可扩展 etcd/Consul 等）。
//
// # 设计要点
//
//   - 薄适配层：仅做类型转换，不引入额外逻辑（负载均衡、熔断等由 Kitex 内部处理）。
//   - 纯轮询模型：Kitex lbcache 默认每 5 秒调用一次 Resolver.Resolve，
//     不需要 ekit 的 Subscriber（推送），只桥接 Discoverer（拉取）。
//   - provider 无关：底层 Registrar/Discoverer 可替换为任意实现。
//
// # 服务端用法
//
//	namingCli, _ := nacos.NewNamingClient(cfg.Nacos)
//	reg, _ := nacosimpl.NewRegistrar(namingCli)
//	svr := echo.NewServer(new(EchoImpl),
//	    server.WithRegistry(kitex.NewRegistry(reg)),
//	)
//
// # 客户端用法
//
//	disc, _ := nacosimpl.NewDiscoverer(namingCli)
//	cli, _ := echo.NewClient("target-service",
//	    client.WithResolver(kitex.NewResolver(disc)),
//	)
package kitex
