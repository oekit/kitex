// Package kitex 提供 NNS 门面到 CloudWeGo Kitex 框架的适配器。
//
// 与 registry/kitex（薄适配层，仅桥接 registry.Discoverer）不同，
// 本包包装 nns/client.ServiceClient，继承 NNS 的全部能力：
//
//   - Mock 模式：本地开发不依赖 Nacos
//   - 静态实例：直接配置服务地址
//   - 实例缓存 + singleflight 防击穿
//   - 多级降级：静态 → mock → 缓存 → discoverer → 降级缓存
//   - ConfigStore 服务配置（超时/重试/负载均衡策略）
//
// # 用法
//
//	nnsc := nns.New(cfg, nns.WithMockMode(true))
//	err := nnsc.Init(ctx, "my-service", []string{"dep-service"})
//
//	// Kitex client 带 NNS 服务发现
//	cli, _ := echo.NewClient("dep-service",
//	    client.WithResolver(nnskitex.NewResolver(nnsc.ServiceClient())),
//	)
//
//	// Kitex server 带 NNS 注册
//	svr := echo.NewServer(handler,
//	    server.WithRegistry(nnskitex.NewRegistry(nnsc.ServiceClient())),
//	)
package kitex
