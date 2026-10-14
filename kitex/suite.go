package kitex

import (
	"sync"

	"github.com/yi-nology/ekit-observability/observability/metrics/resilience"
	"github.com/cloudwego/kitex/client"
	klimiter "github.com/cloudwego/kitex/pkg/limiter"
	"github.com/cloudwego/kitex/server"
	prom "github.com/prometheus/client_golang/prometheus"
)

// ClientSuite 聚合 Kitex 客户端治理选项（retry + rpc_timeout + circuit_breaker）。
//
// 实现 [client.Suite] 接口，可通过 [client.WithSuite] 一行接入。
type ClientSuite struct {
	opts    []client.Option
	closers []func() error

	closeOnce sync.Once
	closeErr  error
}

var _ client.Suite = (*ClientSuite)(nil)

// Options 返回聚合的 client.Option 列表。
func (s *ClientSuite) Options() []client.Option {
	return s.opts
}

// Close 释放本 Suite 创建、且不受 kitex client 生命周期管理的治理资源
// （retry 容器内部的熔断面板；CBSuite 由 client.Close() 自动关闭，无需在此处理）。
//
// 必须在所有由本 Suite 构建的 client 关闭之后再调用——提前调用会使 retry
// 容器的重试熔断面板失效。幂等，可安全重复调用。
func (s *ClientSuite) Close() error {
	s.closeOnce.Do(func() {
		for _, c := range s.closers {
			if err := c(); err != nil && s.closeErr == nil {
				s.closeErr = err
			}
		}
	})
	return s.closeErr
}

// ServerSuite 聚合 Kitex 服务端治理选项（limiter）。
//
// 实现 [server.Suite] 接口，可通过 [server.WithSuite] 一行接入。
type ServerSuite struct {
	opts []server.Option
}

var _ server.Suite = (*ServerSuite)(nil)

// Options 返回聚合的 server.Option 列表。
func (s *ServerSuite) Options() []server.Option {
	return s.opts
}

// ClientSuiteOption 是 [NewClientSuite] 的配置选项函数类型。
type ClientSuiteOption func(*clientSuiteConfig)

type clientSuiteConfig struct {
	retryEnabled        bool
	rpcTimeoutEnabled   bool
	circuitBreakEnabled bool
	metricsReg          prom.Registerer
	metricsNS           string
}

// WithRetry 启用 retry 策略热更新。
func WithRetry() ClientSuiteOption {
	return func(c *clientSuiteConfig) { c.retryEnabled = true }
}

// WithRPCTimeout 启用 rpc_timeout 策略热更新。
func WithRPCTimeout() ClientSuiteOption {
	return func(c *clientSuiteConfig) { c.rpcTimeoutEnabled = true }
}

// WithCircuitBreaker 启用 circuit_breaker 策略热更新。
func WithCircuitBreaker() ClientSuiteOption {
	return func(c *clientSuiteConfig) { c.circuitBreakEnabled = true }
}

// WithMetrics 启用弹性指标（熔断状态/迁移/拒绝/重试，kind="kitex"），
// 形态对齐 httpx.WithMetrics 与 nns endpoint.WithMetrics。
// reg 为 nil 时无指标；namespace 为空时用 "kitex"。
// 重试计数经 metainfo 的 retry.TransitKey（kitex retry 容器每次重试
// 重新走 middleware 链并注入序号）；方法级 RPC 视角（吞吐/时延按
// caller/callee/method/status/retry 分维）另见 kitexprom 桥接包。
func WithMetrics(reg prom.Registerer, namespace string) ClientSuiteOption {
	return func(c *clientSuiteConfig) {
		c.metricsReg = reg
		c.metricsNS = namespace
	}
}

// NewClientSuite 创建客户端治理 Suite。
//
// clientService 为当前服务名，serverService 为被调服务名。
// governance 为 [NewGovernanceConfig] 返回的治理配置客户端。
//
// 使用方式：
//
//	suite := kitex.NewClientSuite("my-service", "echo-service", gov,
//	    kitex.WithRetry(),
//	    kitex.WithRPCTimeout(),
//	)
//	cli, _ := echo.NewClient("echo-service", client.WithSuite(suite))
func NewClientSuite(clientService, serverService string, gov *GovernanceConfig, opts ...ClientSuiteOption) *ClientSuite {
	cfg := &clientSuiteConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	var clientOpts []client.Option
	var closers []func() error

	if gov == nil {
		if cfg.retryEnabled || cfg.rpcTimeoutEnabled || cfg.circuitBreakEnabled {
			govLog.Warn("governance client is nil, requested policies stay at kitex defaults",
				"retry", cfg.retryEnabled,
				"rpcTimeout", cfg.rpcTimeoutEnabled,
				"circuitBreaker", cfg.circuitBreakEnabled)
		}
	}
	if cfg.retryEnabled && gov != nil {
		opts, closer := withRetry(clientService, serverService, gov)
		clientOpts = append(clientOpts, opts...)
		closers = append(closers, closer)
	}
	if cfg.rpcTimeoutEnabled && gov != nil {
		clientOpts = append(clientOpts, withRPCTimeout(clientService, serverService, gov)...)
	}
	if cfg.circuitBreakEnabled && gov != nil {
		var m *resilience.Metrics
		if cfg.metricsReg != nil {
			ns := cfg.metricsNS
			if ns == "" {
				ns = "kitex"
			}
			m = resilience.NewMetrics(cfg.metricsReg, ns)
		}
		cbOpts, cbCloser := withCircuitBreaker(clientService, serverService, gov, m)
		clientOpts = append(clientOpts, cbOpts...)
		if cbCloser != nil {
			closers = append(closers, cbCloser)
		}
	}

	return &ClientSuite{opts: clientOpts, closers: closers}
}

// ServerSuiteOption 是 [NewServerSuite] 的配置选项函数类型。
type ServerSuiteOption func(*serverSuiteConfig)

type serverSuiteConfig struct {
	limiterEnabled bool
	limitReporter  klimiter.LimitReporter
}

// WithLimiter 启用 limiter 策略热更新。
func WithLimiter() ServerSuiteOption {
	return func(c *serverSuiteConfig) { c.limiterEnabled = true }
}

// WithLimitReporter 注入限流过载上报器（kitex 在连接/QPS 超限拒绝请求时回调）。
// 未注入时默认使用内置的节流日志上报（每类事件至多每秒一条 Warn）。
func WithLimitReporter(r klimiter.LimitReporter) ServerSuiteOption {
	return func(c *serverSuiteConfig) { c.limitReporter = r }
}

// NewServerSuite 创建服务端治理 Suite。
//
// serverService 为当前服务名。
// governance 为 [NewGovernanceConfig] 返回的治理配置客户端。
//
// 使用方式：
//
//	suite := kitex.NewServerSuite("echo-service", gov, kitex.WithLimiter())
//	svr := echo.NewServer(new(Impl), server.WithSuite(suite))
func NewServerSuite(serverService string, gov *GovernanceConfig, opts ...ServerSuiteOption) *ServerSuite {
	cfg := &serverSuiteConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	var serverOpts []server.Option

	if gov == nil && cfg.limiterEnabled {
		govLog.Warn("governance client is nil, limiter stays disabled",
			"limiter", cfg.limiterEnabled)
	}
	if cfg.limiterEnabled && gov != nil {
		serverOpts = append(serverOpts, withLimiter(serverService, gov, cfg.limitReporter)...)
	}

	return &ServerSuite{opts: serverOpts}
}
