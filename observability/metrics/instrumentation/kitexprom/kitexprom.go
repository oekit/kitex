// Package kitexprom 桥接 kitex-contrib/monitor-prometheus，为 Kitex 提供
// 方法级 RPC 指标（吞吐/时延，labels 含 caller/callee/method/status/retry）。
//
// 桥接固化了两个与 ekit 指标架构对齐的关键决策：
//   - 指标注册进调用方传入的 Registry（通常 ekit observability/metrics.Registry），
//     与 /metrics 端点同源单端点暴露
//   - 禁用插件自带的独立 HTTP exporter——它监听独占端口，且启动失败会
//     log.Fatal 直接打死进程，不符合 ekit 组件的故障隔离原则
//
// 指标族（与 resilience 指标族互补：本包提供方法粒度的 RPC 视角，
// observability/metrics/resilience 提供服务粒度的熔断/重试治理视角）：
//
//	kitex_client_throughput{caller,callee,method,status,retry}
//	kitex_client_latency_us{caller,callee,method,status,retry}
//	kitex_server_throughput{...} / kitex_server_latency_us{...}
//
// retry label 来自 kitex RPCInfo RetryTag（重试序号），配合治理栈的
// kitex.WithMetrics 服务级 retry_attempts_total 可交叉定位方法级重试热点。
//
// 官方仓库：https://github.com/kitex-contrib/monitor-prometheus
package kitexprom

import (
	"sync"

	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/pkg/stats"
	"github.com/cloudwego/kitex/server"
	kprom "github.com/kitex-contrib/monitor-prometheus"
	"github.com/oekit/metrics"
	prom "github.com/prometheus/client_golang/prometheus"
)

// tracerCache 按 (Registry, 侧) 缓存 tracer。插件内部用 MustRegister 注册
// collector（同名不去重），同 Registry 重复创建会 panic；tracer 是无状态
// 的 stats.Tracer（指标经 labels 区分 caller/callee/method），共享零风险。
//
// 缓存值是 *tracerEntry（构造收口在 sync.Once 内，#211）：Load/LoadOrStore
// 组合不构成互斥——两 goroutine 可同时未命中、各自构造，插件内部的
// MustRegister 遇同名重复注册直接 panic 打死进程（check-then-act）。构造
// 必须收敛到共享 entry 的 once 内，读侧也经 entry.get() 等 once 完成，
// 不存在读到半构造态的窗口。
var tracerCache sync.Map // tracerKey → *tracerEntry

type tracerKey struct {
	serverSide bool
	reg        prom.Registerer
}

type tracerEntry struct {
	once sync.Once
	new  func() stats.Tracer
	tr   stats.Tracer
}

// get 幂等构造：首个调用执行 new，其余（含并发）等首次完成并拿到同一实例
// （sync.Once 保证 f 完成先行于任何后续 Do 返回）。
func (e *tracerEntry) get() stats.Tracer {
	e.once.Do(func() { e.tr = e.new() })
	return e.tr
}

// loadOrNewTracer 按 key 取共享 tracer；未命中时以 newFn 构造一次（并发
// 安全：构造收敛进共享 entry 的 once，输掉 LoadOrStore 竞争的一方不会
// 执行自己的构造）。
func loadOrNewTracer(key tracerKey, newFn func() stats.Tracer) stats.Tracer {
	if v, ok := tracerCache.Load(key); ok {
		return v.(*tracerEntry).get()
	}
	e := &tracerEntry{new: newFn}
	actual, _ := tracerCache.LoadOrStore(key, e)
	return actual.(*tracerEntry).get()
}

// NewClientTracer 创建 client 侧方法级 RPC 指标 tracer。
// reg 为 nil 时回退 ekit 默认 metrics.Registry；同 Registry 幂等（多 client
// 共享一个 tracer 实例，并发创建也只构造一次，#211）。opts 仅在首次创建时
// 生效。
func NewClientTracer(reg prom.Registerer, opts ...kprom.Option) stats.Tracer {
	return loadOrNewTracer(tracerKey{reg: asRegistry(reg)}, func() stats.Tracer {
		return kprom.NewClientTracer("", "",
			append([]kprom.Option{
				kprom.WithRegistry(asRegistry(reg)),
				kprom.WithDisableServer(true),
			}, opts...)...)
	})
}

// NewServerTracer 创建 server 侧方法级 RPC 指标 tracer
// （同 NewClientTracer 语义，client/server 指标名互不冲突）。
func NewServerTracer(reg prom.Registerer, opts ...kprom.Option) stats.Tracer {
	return loadOrNewTracer(tracerKey{serverSide: true, reg: asRegistry(reg)}, func() stats.Tracer {
		return kprom.NewServerTracer("", "",
			append([]kprom.Option{
				kprom.WithRegistry(asRegistry(reg)),
				kprom.WithDisableServer(true),
			}, opts...)...)
	})
}

// ClientOption 返回挂载 client tracer 的 kitex client.Option：
//
//	cli, _ := echo.NewClient("echo-service",
//	    kitexprom.ClientOption(nil),  // 注册进 ekit metrics.Registry
//	    client.WithHostPorts(":8888"),
//	)
func ClientOption(reg prom.Registerer, opts ...kprom.Option) client.Option {
	return client.WithTracer(NewClientTracer(reg, opts...))
}

// ServerOption 返回挂载 server tracer 的 kitex server.Option。
func ServerOption(reg prom.Registerer, opts ...kprom.Option) server.Option {
	return server.WithTracer(NewServerTracer(reg, opts...))
}

// asRegistry 把 Registerer 适配为插件要求的 *prom.Registry 具体类型；
// 非该类型时回退 ekit 默认 Registry（其自身就是 *prom.Registry）。
func asRegistry(reg prom.Registerer) *prom.Registry {
	if r, ok := reg.(*prom.Registry); ok && r != nil {
		return r
	}
	return metrics.Registry
}
