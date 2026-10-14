package kitexprom

import (
	"errors"
	"strings"
	"testing"

	"github.com/yi-nology/ekit-observability/observability/metrics"

	prom "github.com/prometheus/client_golang/prometheus"
)

// assertRegistered 证明指定名称的 metric 已注册进 Registry：
// 空 CounterVec 尚无 series（CollectAndCount/gather 数不到），用
// "注册同名 collector 应得 AlreadyRegisteredError" 反证。
func assertRegistered(t *testing.T, reg *prom.Registry, name string, labels []string) {
	t.Helper()
	dup := prom.NewCounterVec(prom.CounterOpts{Name: name, Help: "dup probe"}, labels)
	err := reg.Register(dup)
	if err == nil {
		t.Fatalf("expected %s to be already registered in provided registry", name)
	}
	// AlreadyRegisteredError（help/labels 全同时）或 descriptor 冲突
	// （"previously registered ... different help string"）——两者都证明
	// 同名 collector 已在该 Registry
	var are prom.AlreadyRegisteredError
	if !errors.As(err, &are) && !strings.Contains(err.Error(), "previously registered") {
		t.Fatalf("expected %s already registered (AlreadyRegisteredError or descriptor conflict), got: %v", name, err)
	}
}

// TestNewClientTracer_RegistersIntoRegistry 钉住桥接的核心决策：
// 指标注册进传入的 Registry（单端点模式），且禁用插件自带 exporter
// （addr 传空串也不监听任何端口）。
func TestNewClientTracer_RegistersIntoRegistry(t *testing.T) {
	reg := prom.NewRegistry()
	tr := NewClientTracer(reg)
	if tr == nil {
		t.Fatal("expected non-nil tracer")
	}
	lb := []string{"caller", "callee", "method", "status", "retry"}
	assertRegistered(t, reg, "kitex_client_throughput", lb)
	assertRegistered(t, reg, "kitex_client_latency_us", lb)
}

// TestNewServerTracer_RegistersIntoRegistry server 侧对称验证。
func TestNewServerTracer_RegistersIntoRegistry(t *testing.T) {
	reg := prom.NewRegistry()
	tr := NewServerTracer(reg)
	if tr == nil {
		t.Fatal("expected non-nil tracer")
	}
	assertRegistered(t, reg, "kitex_server_throughput", nil)
}

// TestClientOption_WiresTracer 验证 Option 工厂可构造（kitex Option 是
// 接口，构造即装配路径成立；真实挂载由 kitex client 装配，e2e 已覆盖
// kitex 侧治理栈）。
func TestClientOption_WiresTracer(t *testing.T) {
	_ = ClientOption(prom.NewRegistry())
	_ = ServerOption(prom.NewRegistry())
}

// TestAsRegistry_Fallback 非 *prom.Registry 类型回退 ekit 默认 Registry
// （metrics.Registry 自身就是 *prom.Registry，指标仍暴露在 /metrics 端点；
// 注意 DefaultRegisterer 的动态类型恰是 *Registry，走透传而非回退）。
func TestAsRegistry_Fallback(t *testing.T) {
	if asRegistry(nil) != metrics.Registry {
		t.Fatal("nil should fall back to ekit metrics.Registry")
	}
	custom := prom.NewRegistry()
	if asRegistry(custom) != custom {
		t.Fatal("*prom.Registry should pass through")
	}
	if asRegistry(prom.DefaultRegisterer) != prom.DefaultRegisterer {
		t.Fatal("DefaultRegisterer (dynamic type *Registry) should pass through")
	}
	// 裸接口包装（非 *Registry 动态类型）才回退
	if asRegistry(onlyRegisterer{custom}) != metrics.Registry {
		t.Fatal("plain Registerer should fall back to ekit metrics.Registry")
	}
}

type onlyRegisterer struct{ prom.Registerer }

// TestRegistryReuse 同 Registry 重复创建 tracer 必须幂等（返回同实例）：
// 插件内部 MustRegister 不去重，裸调用第二次会 panic duplicate collector——
// 桥接层经 tracerCache 保证多 client 共享。
func TestRegistryReuse(t *testing.T) {
	reg := prom.NewRegistry()
	first := NewClientTracer(reg)
	second := NewClientTracer(reg)
	if first != second {
		t.Fatal("expected same tracer instance for same registry (idempotent creation)")
	}
	// client 与 server 侧互不冲突（指标名不同）
	if NewServerTracer(reg) == first {
		t.Fatal("client and server tracers must be distinct")
	}
}
