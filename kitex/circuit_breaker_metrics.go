package kitex

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/yi-nology/ekit-concurrency/async"
	"github.com/yi-nology/ekit-metrics/resilience"

	gopkgcb "github.com/bytedance/gopkg/cloud/circuitbreaker"
	"github.com/bytedance/gopkg/cloud/metainfo"
	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/pkg/circuitbreak"
	"github.com/cloudwego/kitex/pkg/endpoint"
	"github.com/cloudwego/kitex/pkg/kerrors"
	"github.com/cloudwego/kitex/pkg/retry"
)

// kitexMetricsKind 是 kitex 治理栈在弹性指标里的 kind 标签值（契约 §5.5）。
const kitexMetricsKind = "kitex"

// cbPollInterval 是 CBSuite 熔断状态的默认轮询间隔。
// kitex CBSuite 的状态变化回调（onStateChange）是私有的、仅向内部
// event.Queue 发布格式化字符串；轮询 Panel.DumpBreakers + Breaker.State()
// 是稳定公开接口，5s 粒度足以覆盖熔断状态的告警语义（迁移频率低）。
const cbPollInterval = 5 * time.Second

// normalizeCBState 把 gopkg circuitbreaker.State 映射到 resilience 状态值。
// 两族常量数值序相反（gopkg: Open=0/HalfOpen=1/Closed=2；
// resilience 对齐 gobreaker: Closed=0/HalfOpen=1/Open=2），禁止直接 int()。
func normalizeCBState(s gopkgcb.State) int {
	switch s {
	case gopkgcb.Open:
		return resilience.StateOpen
	case gopkgcb.HalfOpen:
		return resilience.StateHalfOpen
	default:
		return resilience.StateClosed
	}
}

// cbStatePoller 轮询 CBSuite 的方法级熔断状态，diff 后记指标与日志。
//
// 指标聚合到服务级（契约 §5.5 labels {service, kind}，方法名是潜在高基数
// 不得入标签）：聚合状态 = 全部方法熔断器的最差状态；方法级变化走结构化
// 日志（排障定位用），指标管服务级趋势告警。
type cbStatePoller struct {
	service  string
	metrics  *resilience.Metrics
	snapshot func() map[string]int // method key → 归一状态（测试注入点）

	mu      sync.Mutex
	last    map[string]int
	lastAgg int

	stop     chan struct{}
	stopOnce sync.Once
}

func newCBStatePoller(service string, m *resilience.Metrics, cbs *circuitbreak.CBSuite) *cbStatePoller {
	p := &cbStatePoller{
		service: service,
		metrics: m,
		stop:    make(chan struct{}),
	}
	p.snapshot = func() map[string]int {
		panel := cbs.ServicePanel()
		if panel == nil {
			return nil // panel 懒加载，首个请求前为 nil
		}
		dumper, ok := panel.(interface {
			DumpBreakers() map[string]gopkgcb.Breaker
		})
		if !ok {
			return nil
		}
		breakers := dumper.DumpBreakers()
		if len(breakers) == 0 {
			return nil
		}
		out := make(map[string]int, len(breakers))
		for key, b := range breakers {
			out[key] = normalizeCBState(b.State())
		}
		return out
	}
	return p
}

// start 启动后台轮询；goroutine 经 async.GoSafe 托管（panic 不打死进程）。
func (p *cbStatePoller) start(interval time.Duration) {
	ticker := time.NewTicker(interval)
	async.GoSafe(func() {
		defer ticker.Stop()
		for {
			select {
			case <-p.stop:
				return
			case <-ticker.C:
				p.Poll()
			}
		}
	})
}

// Stop 停止轮询（幂等）。挂 ClientSuite.Close 链。
func (p *cbStatePoller) Stop() error {
	p.stopOnce.Do(func() { close(p.stop) })
	return nil
}

// Poll 执行一次状态快照 diff（导出供测试同步调用）。
func (p *cbStatePoller) Poll() {
	current := p.snapshot()
	if current == nil {
		return
	}

	worst := resilience.StateClosed
	for _, st := range current {
		if st > worst {
			worst = st
		}
	}

	p.mu.Lock()
	for key, st := range current {
		if old, ok := p.last[key]; ok && old != st {
			govLog.Warn("circuit breaker state change",
				"service", p.service, "method", key,
				"from", resilience.StateName(old), "to", resilience.StateName(st))
		}
	}
	aggOld := p.lastAgg
	p.last = current
	p.lastAgg = worst
	p.mu.Unlock()

	p.metrics.ObserveBreakerState(p.service, kitexMetricsKind, worst)
	if worst != aggOld {
		p.metrics.ObserveTransition(p.service, kitexMetricsKind, aggOld, worst)
	}
}

// cbMetricsMW 构造弹性计数 middleware：
//   - 重试计数：kitex retry 容器在 middleware 链外层，每次重试重新走整条
//     middleware 链并向 metainfo 注入 retry.TransitKey（重试序号，首试不注入）；
//     middleware 里读到 TransitKey 即是一次额外重试尝试
//   - 拒绝计数：拦截 kerrors.ErrCircuitBreak 及其 Service/Instance 变体
//     （DetailedError.Is 匹配 basic 指针）
func cbMetricsMW(service string, m *resilience.Metrics) endpoint.Middleware {
	return func(next endpoint.Endpoint) endpoint.Endpoint {
		return func(ctx context.Context, req, resp interface{}) error {
			if _, ok := metainfo.GetPersistentValue(ctx, retry.TransitKey); ok {
				m.ObserveRetry(service, kitexMetricsKind)
			}
			err := next(ctx, req, resp)
			if err != nil && errors.Is(err, kerrors.ErrCircuitBreak) {
				m.ObserveRejected(service, kitexMetricsKind)
			}
			return err
		}
	}
}

// cbMetricsOptions 返回熔断可观测性的 client.Option 集。
func cbMetricsOptions(service string, m *resilience.Metrics) []client.Option {
	if m == nil {
		return nil
	}
	return []client.Option{client.WithMiddleware(cbMetricsMW(service, m))}
}

// startCBMetrics 组装轮询器并返回其 Stop（挂 closer 链）。
// metrics 为 nil 时零开销返回。
func startCBMetrics(cbs *circuitbreak.CBSuite, service string, m *resilience.Metrics, interval time.Duration) func() error {
	if m == nil {
		return nil
	}
	poller := newCBStatePoller(service, m, cbs)
	poller.start(interval)
	return poller.Stop
}
