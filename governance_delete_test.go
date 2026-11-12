package kitex

// #197 回归：Nacos 上删除治理配置时 SDK 投递空串（非 JSON），必须按
// 「整表删除 = 治理面回退默认」处理，而不是当成坏 JSON 保留上一轮旧策略。
// 覆盖五条路径：OnChange 归一化 choke point、retry/rpc_timeout/circuit_break
// 三类 fullSyncApplier、limiterController、governance 模板。

import (
	"testing"
	"time"

	"github.com/cloudwego/kitex/pkg/circuitbreak"
	"github.com/cloudwego/kitex/pkg/retry"
	"github.com/cloudwego/kitex/pkg/rpcinfo"
	"github.com/cloudwego/kitex/pkg/rpctimeout"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

// TestGovernanceDelete_OnChangeNormalizesEmptyContent 钉住 choke point：
// subscribe 注册的 OnChange 把空串（含纯空白）归一成 "{}" 空表再交给
// handler——这是四类治理配置共享的单一入口，坏 JSON 判定不再吞掉删除事件。
func TestGovernanceDelete_OnChangeNormalizesEmptyContent(t *testing.T) {
	var onChange func(string, string, string, string)
	stub := &stubConfigClient{
		getConfigFunc:    func(vo.ConfigParam) (string, error) { return "", nil },
		listenConfigFunc: func(p vo.ConfigParam) error { onChange = p.OnChange; return nil },
	}

	g, err := NewGovernanceConfig(stub)
	if err != nil {
		t.Fatalf("NewGovernanceConfig: %v", err)
	}
	defer func() { _ = g.Close() }()

	var got []string
	if err := g.subscribe("c.s.retry", func(data string) {
		got = append(got, data)
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	onChange("c.s.retry", "DEFAULT_GROUP", "", "")
	onChange("c.s.retry", "DEFAULT_GROUP", "", "   \n\t")
	if len(got) != 2 || got[0] != "{}" || got[1] != "{}" {
		t.Fatalf("empty content must be normalized to empty table, got %q", got)
	}
}

// TestGovernanceDelete_RetryPolicyCleared 删除 retry 配置（空串）⇒ 容器内
// 全部方法策略被 DeletePolicy（回退为不重试），而不是旧策略残留。
func TestGovernanceDelete_RetryPolicyCleared(t *testing.T) {
	rc := retry.NewRetryContainer()
	a := newRetryPolicyApplier(rc)

	a.applyConfig(`{"m1":{"enable":true,"type":0,"failure_policy":{"stop_policy":{"max_retry_times":2}}}}`)
	if !dumpRetryKeys(t, rc)["m1"] {
		t.Fatal("precondition: m1 policy registered")
	}

	if n := a.applyConfig(""); n != 0 {
		t.Fatalf("apply(empty content) = %d, want 0 (deleted, not parse error)", n)
	}
	keys := dumpRetryKeys(t, rc)
	if len(keys) != 0 {
		t.Errorf("retry policies must be cleared on config deletion, got %v", keys)
	}
}

// TestGovernanceDelete_RPCTimeoutResetToDefault 删除 rpc_timeout 配置 ⇒
// 容器整表替换为空（NotifyPolicyChange 空表重置为默认值），临时收紧的
// 超时不再永久残留；坏 JSON（非空）仍保留上一轮（既有语义不回退）。
func TestGovernanceDelete_RPCTimeoutResetToDefault(t *testing.T) {
	tc := rpctimeout.NewContainer()
	a := newRPCTimeoutApplier(tc, "c.s.rpc_timeout")

	ri := rpcinfo.NewRPCInfo(
		rpcinfo.EmptyEndpointInfo(),
		rpcinfo.EmptyEndpointInfo(),
		rpcinfo.NewInvocation("", "m1"),
		rpcinfo.NewRPCConfig(),
		rpcinfo.NewRPCStats(),
	)

	a.applyConfig(`{"m1":{"conn_timeout_ms":50,"rpc_timeout_ms":300}}`)
	timeouts := tc.Timeouts(ri)
	if timeouts == nil || timeouts.RPCTimeout() != 300*time.Millisecond {
		t.Fatalf("precondition: applied timeout = %v, want 300ms", timeouts)
	}

	// 删除配置（空串）→ 回默认（对照全新容器的默认值）
	if n := a.applyConfig(""); n != 0 {
		t.Fatalf("apply(empty content) = %d, want 0", n)
	}
	got := tc.Timeouts(ri)
	fresh := rpctimeout.NewContainer().Timeouts(ri)
	if got == nil || fresh == nil || got.RPCTimeout() != fresh.RPCTimeout() {
		t.Errorf("deleted config must reset timeout to kitex default, got %v want %v", got, fresh)
	}

	// 坏 JSON（非空）：上一轮（此处为默认空表）不改动、不报删除语义
	if n := a.applyConfig("not-json{"); n != -1 {
		t.Errorf("apply(bad json) = %d, want -1", n)
	}
}

// TestGovernanceDelete_CircuitBreakResetToDefault 删除 circuit_break 配置 ⇒
// 全部方法恢复 kitex 默认熔断参数。
func TestGovernanceDelete_CircuitBreakResetToDefault(t *testing.T) {
	cbs := circuitbreak.NewCBSuite(methodCBKey)
	a := newCBConfigApplier(cbs)

	a.applyConfig(`{"Echo":{"enable":true,"err_rate":0.3,"min_sample":100}}`)
	if cfg := storedCBConfigs(t, cbs)["Echo"]; cfg.ErrRate != 0.3 {
		t.Fatalf("precondition: applied config = %+v, want err_rate 0.3", cfg)
	}

	if n := a.applyConfig(""); n != 0 {
		t.Fatalf("apply(empty content) = %d, want 0", n)
	}
	cfg := storedCBConfigs(t, cbs)["Echo"]
	def := circuitbreak.GetDefaultCBConfig()
	if cfg.ErrRate != def.ErrRate || cfg.MinSample != def.MinSample {
		t.Errorf("deleted config must restore default CB params, got %+v want %+v", cfg, def)
	}
}

// TestGovernanceDelete_LimiterCleared 删除 limit 配置 ⇒ {0,0} 原样下发
// UpdateLimit（kitex 语义 limit<=0 = 不限流），旧限流参数不再永久残留。
func TestGovernanceDelete_LimiterCleared(t *testing.T) {
	ctrl := &limiterController{dataID: "s.limit"}
	fake := &fakeUpdater{}
	ctrl.updateControl(fake)

	if !ctrl.apply(`{"connection_limit":100,"qps_limit":2000}`) {
		t.Fatal("precondition: apply config failed")
	}
	if !ctrl.apply("") {
		t.Fatal("apply(empty content) must be treated as deletion, not parse failure")
	}
	if fake.got == nil || fake.got.MaxConnections != 0 || fake.got.MaxQPS != 0 {
		t.Errorf("deleted limit config must update to {0,0} (unlimited), got %+v", fake.got)
	}
	if fake.calls != 2 {
		t.Errorf("UpdateLimit calls = %d, want 2", fake.calls)
	}
}

// TestGovernanceDelete_TemplateClearsTimeout 删除模板配置（空串）⇒
// NotifyPolicyChange 空表重置，方法级超时回退 kitex 默认。
func TestGovernanceDelete_TemplateClearsTimeout(t *testing.T) {
	var onChange func(string, string, string, string)
	stub := &stubConfigClient{
		getConfigFunc:    func(vo.ConfigParam) (string, error) { return "", nil },
		listenConfigFunc: func(p vo.ConfigParam) error { onChange = p.OnChange; return nil },
	}

	g, err := NewGovernanceConfig(stub, WithGovernanceTemplate("svc.governance"))
	if err != nil {
		t.Fatalf("NewGovernanceConfig: %v", err)
	}
	defer func() { _ = g.Close() }()

	onChange("svc.governance", "DEFAULT_GROUP", "", `{"timeout_ms":{"Method":200}}`)

	onChange("svc.governance", "DEFAULT_GROUP", "", "")
	ri := rpcinfo.NewRPCInfo(
		rpcinfo.EmptyEndpointInfo(),
		rpcinfo.EmptyEndpointInfo(),
		rpcinfo.NewInvocation("", "Method"),
		rpcinfo.NewRPCConfig(),
		rpcinfo.NewRPCStats(),
	)
	got := g.templateTC.Timeouts(ri)
	fresh := rpctimeout.NewContainer().Timeouts(ri)
	if got == nil || fresh == nil || got.RPCTimeout() != fresh.RPCTimeout() {
		t.Errorf("deleted template must reset timeout to kitex default, got %v want %v", got, fresh)
	}
}
