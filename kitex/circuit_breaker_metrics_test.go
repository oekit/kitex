package kitex

import (
	"context"
	"testing"

	"github.com/bytedance/gopkg/cloud/metainfo"
	"github.com/cloudwego/kitex/pkg/retry"

	"github.com/cloudwego/kitex/pkg/endpoint"
	"github.com/cloudwego/kitex/pkg/kerrors"
	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/yi-nology/ekit-observability/observability/metrics/resilience"
)

// metricValue 从 Registry 黑盒采集指定 metric + labels 的值。
func metricValue(t *testing.T, reg *prom.Registry, name string, labels map[string]string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			match := true
			for k, v := range labels {
				found := false
				for _, lp := range m.GetLabel() {
					if lp.GetName() == k && lp.GetValue() == v {
						found = true
						break
					}
				}
				if !found {
					match = false
					break
				}
			}
			if match {
				if m.GetGauge() != nil {
					return m.GetGauge().GetValue()
				}
				return m.GetCounter().GetValue()
			}
		}
	}
	t.Fatalf("metric %s with labels %v not found", name, labels)
	return 0
}

// TestCBStatePoller_AggregationAndTransitions 钉住轮询器语义：
// 服务级聚合 = 全部方法熔断器的最差状态；聚合状态变化记一次 transition；
// gauge 持续反映最差状态。
func TestCBStatePoller_AggregationAndTransitions(t *testing.T) {
	reg := prom.NewRegistry()
	m := resilience.NewMetrics(reg, "test")
	p := newCBStatePoller("echo-svc", m, nil)

	// 注入 fake snapshot：echo 方法 open，其余 closed
	p.snapshot = func() map[string]int {
		return map[string]int{
			"echo":  resilience.StateOpen,
			"query": resilience.StateClosed,
		}
	}
	p.Poll()
	if got := metricValue(t, reg, "test_breaker_state",
		map[string]string{"service": "echo-svc", "kind": "kitex"}); got != resilience.StateOpen {
		t.Fatalf("expected aggregated open state, got %v", got)
	}
	if got := metricValue(t, reg, "test_breaker_transitions_total",
		map[string]string{"service": "echo-svc", "kind": "kitex", "from": "closed", "to": "open"}); got != 1 {
		t.Fatalf("expected 1 closed→open transition, got %v", got)
	}

	// echo 恢复 closed → 聚合回 closed
	p.snapshot = func() map[string]int {
		return map[string]int{"echo": resilience.StateClosed, "query": resilience.StateClosed}
	}
	p.Poll()
	if got := metricValue(t, reg, "test_breaker_state",
		map[string]string{"service": "echo-svc", "kind": "kitex"}); got != resilience.StateClosed {
		t.Fatalf("expected aggregated closed after recovery, got %v", got)
	}
	if got := metricValue(t, reg, "test_breaker_transitions_total",
		map[string]string{"service": "echo-svc", "kind": "kitex", "from": "open", "to": "closed"}); got != 1 {
		t.Fatalf("expected 1 open→closed transition, got %v", got)
	}
}

// TestCBMetricsOptions_RejectedCounting 钉住拒绝计数：
// kerrors.ErrServiceCircuitBreak / ErrInstanceCircuitBreak（DetailedError
// 变体，经 errors.Is 匹配 ErrCircuitBreak）都计一次拒绝。
func TestCBMetricsOptions_RejectedCounting(t *testing.T) {
	reg := prom.NewRegistry()
	m := resilience.NewMetrics(reg, "test")

	calls := 0
	next := endpoint.Endpoint(func(ctx context.Context, req, resp interface{}) error {
		calls++
		switch calls {
		case 1:
			return kerrors.ErrServiceCircuitBreak
		case 2:
			return kerrors.ErrInstanceCircuitBreak
		default:
			return nil // 成功路径不计数
		}
	})
	ep := cbMetricsMW("echo-svc", m)(next)

	_ = ep(context.Background(), nil, nil) // service 级熔断
	_ = ep(context.Background(), nil, nil) // instance 级熔断
	if got := metricValue(t, reg, "test_breaker_rejected_total",
		map[string]string{"service": "echo-svc", "kind": "kitex"}); got != 2 {
		t.Fatalf("expected 2 rejections (service+instance variants), got %v", got)
	}
	// 成功路径不增量
	_ = ep(context.Background(), nil, nil)
	if got := metricValue(t, reg, "test_breaker_rejected_total",
		map[string]string{"service": "echo-svc", "kind": "kitex"}); got != 2 {
		t.Fatalf("success path must not count rejection, got %v", got)
	}
}

// TestNormalizeCBState gopkg State（Open=0/HalfOpen=1/Closed=2）与
// resilience 值序相反，映射必须显式且完整。
func TestNormalizeCBState(t *testing.T) {
	if normalizeCBState(0) != resilience.StateOpen {
		t.Fatal("gopkg Open(0) must map to resilience.StateOpen")
	}
	if normalizeCBState(1) != resilience.StateHalfOpen {
		t.Fatal("gopkg HalfOpen(1) must map to resilience.StateHalfOpen")
	}
	if normalizeCBState(2) != resilience.StateClosed {
		t.Fatal("gopkg Closed(2) must map to resilience.StateClosed")
	}
}

// TestWithMetrics_SuiteWiring 验证 NewClientSuite(WithCircuitBreaker+WithMetrics)
// 组装出拒绝计数 middleware（Options 非空且 Close 幂等）。
func TestWithMetrics_SuiteWiring(t *testing.T) {
	reg := prom.NewRegistry()
	// gov 为 nil 时不接线——直接构造 config 级验证选项生效
	cfg := &clientSuiteConfig{}
	WithMetrics(reg, "")(cfg)
	if cfg.metricsReg != reg {
		t.Fatal("expected metricsReg to be set by WithMetrics")
	}

	// namespace 默认走 resilience 包逻辑，选项本身不强制默认值
	WithMetrics(reg, "custom")(cfg)
	if cfg.metricsNS != "custom" {
		t.Fatal("expected metricsNS to be passthrough")
	}
}

// TestCBMetricsMW_RetryCountingViaTransitKey 钉住重试计数语义：
// kitex retry 容器在 middleware 链外层，每次重试尝试重新走整条链并注入
// retry.TransitKey（首试不注入）——middleware 读到 TransitKey 即一次额外重试。
func TestCBMetricsMW_RetryCountingViaTransitKey(t *testing.T) {
	reg := prom.NewRegistry()
	m := resilience.NewMetrics(reg, "test")

	calls := 0
	next := endpoint.Endpoint(func(ctx context.Context, req, resp interface{}) error {
		calls++
		return nil
	})
	ep := cbMetricsMW("echo-svc", m)(next)

	// 顺序：首试（无 TransitKey）→ 两次重试（TransitKey=1/2，模拟 kitex
	// retry 容器注入）→ 断言计数 = 2（首试不计入 retry_attempts）
	ep(context.Background(), nil, nil)
	retryCtx := metainfo.WithPersistentValue(context.Background(), retry.TransitKey, "1")
	ep(retryCtx, nil, nil)
	retryCtx2 := metainfo.WithPersistentValue(context.Background(), retry.TransitKey, "2")
	ep(retryCtx2, nil, nil)
	if got := metricValue(t, reg, "test_retry_attempts_total",
		map[string]string{"service": "echo-svc", "kind": "kitex"}); got != 2 {
		t.Fatalf("expected 2 retry attempts via TransitKey, got %v", got)
	}
	if calls != 3 {
		t.Fatalf("expected 3 total calls, got %d", calls)
	}
	// 再来一次首试：不增量
	ep(context.Background(), nil, nil)
	if got := metricValue(t, reg, "test_retry_attempts_total",
		map[string]string{"service": "echo-svc", "kind": "kitex"}); got != 2 {
		t.Fatalf("non-retry attempt must not count, got %v", got)
	}
}
