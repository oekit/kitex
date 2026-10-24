// Package kitex 为 CloudWeGo Kitex RPC 框架提供 ekit 生态集成。
//
// 本包覆盖以下能力：
//
//   - 日志桥接：将 Kitex klog 接管到 ekit [logx]——已迁 github.com/oekit/logx/kitexlog
//     （kitexlog.NewLogger）；本包保留 [NewLogger] Deprecated 别名委托，行为一致。
//   - 可观测性：Kitex OpenTelemetry tracing/metrics 一键接入——**已迁嵌套模块**
//     github.com/oekit/kitex/observability/tracing/instrumentation/kitexotel
//     （kitexotel.NewServerTracingSuite / kitexotel.NewClientTracingSuite）。
//     v0.7.0 起本包不再持有该能力：obs-opentelemetry 会把 apache/thrift 抬到
//     v0.16.0，留在根模块会打断下游按 thrift 0.13 生成的 Thrift 代码（#37）。
//   - 治理配置：从 Nacos 配置中心加载 retry/rpc_timeout/circuit_breaker/limiter 策略并热更新（见 [NewGovernanceConfig]）。
//   - Suite 聚合：以 Kitex [client.Suite]/[server.Suite] 形式聚合治理选项（见 [NewClientSuite]、[NewServerSuite]）。
//   - 启动辅助：与 ekit [bootstrap.App] 集成的生命周期管理（见 [RunServer]）。
//
// # 快速开始
//
//	// 1. 日志桥接（推荐 logx/kitexlog；必须传 slog 实例避免无限递归）
//	klog.SetLogger(kitexlog.NewLogger(logx.NewSlogLogger("my-rpc")))
//	//    或 Deprecated 别名：klog.SetLogger(kitex.NewLogger(logx.NewSlogLogger("my-rpc")))
//
//	// 2. 可观测性（需先调用 tracing.Init；v0.7.0 起随嵌套模块 kitexotel 提供）
//	svr := echo.NewServer(new(Impl),
//	    server.WithSuite(kitexotel.NewServerTracingSuite()),
//	)
//
//	// 3. 治理配置（需 Nacos config client）
//	gov, _ := kitex.NewGovernanceConfig(configCli)
//	svr := echo.NewServer(new(Impl),
//	    server.WithSuite(kitex.NewServerSuite("echo", gov)),
//	)
//
// # 优雅退出最佳实践（对照官方 best practice 文档）
//
// Kitex 的 server.Stop() 按以下流程排空连接：
//  1. 停止接受新连接（关闭监听端口）
//  2. 非多路复用：每秒检查直到所有连接处理完毕；多路复用：先发控制帧通知客户端再等待
//  3. 超时退出：达到 ExitWaitTime（默认 5s）后直接退出
//
// 关键旋钮：
//
//	server.WithExitWaitTime(10*time.Second) // 排空等待上限（默认 5s）
//
// 时间关系（K8s 环境下必须满足）：
//
//	client.DiscoverLatency < k8s.PreStopTime < server.MaxLatency < kitex.ExitWaitTime < k8s.TerminationGracePeriodSeconds
//
// 其中：
//   - client.DiscoverLatency：客户端服务发现延迟（Nacos 推送 + 缓存刷新）
//   - k8s.PreStopTime：preStop 命令等待时间（建议 sleep 5s，确保 iptables/endpoint 更新传播）
//   - server.MaxLatency：单请求最大处理耗时
//   - kitex.ExitWaitTime：框架排空等待上限
//   - k8s.TerminationGracePeriodSeconds：K8s 强杀超时（须大于以上所有值之和）
//
// 推荐 K8s 配置：
//
//	lifecycle:
//	  preStop:
//	    exec:
//	      command: ["sleep", "5"]
//	terminationGracePeriodSeconds: 30
//
// 与 ekit bootstrap 集成时，bootstrap 的 ShutdownTimeout 是单资源关闭预算；
// Kitex server.Stop() 使用自身 ExitWaitTime（忽略传入 ctx），两者独立。
// 保持 ShutdownTimeout > ExitWaitTime 以确保排空不被 bootstrap 兜底截断。
package kitex
