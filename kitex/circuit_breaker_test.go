package kitex

import (
	"testing"

	"github.com/cloudwego/kitex/pkg/circuitbreak"
	"github.com/cloudwego/kitex/pkg/rpcinfo"
)

// TestMethodCBKey 验证熔断配置 key 即 RPC 方法名（与 Nacos 配置格式一致），
// 而非 kitex 默认 RPCInfo2Key 的 "from/to/method" 复合格式——后者会让
// UpdateServiceCBConfig 写入的配置永远查不到。
func TestMethodCBKey(t *testing.T) {
	ri := rpcinfo.NewRPCInfo(
		rpcinfo.EmptyEndpointInfo(),
		rpcinfo.NewEndpointInfo("echo-server", "Echo", nil, nil),
		rpcinfo.NewInvocation("echo-client", "Echo"),
		rpcinfo.NewRPCConfig(),
		rpcinfo.NewRPCStats(),
	)
	if got := methodCBKey(ri); got != "Echo" {
		t.Errorf("methodCBKey = %q, want %q", got, "Echo")
	}
	if got := methodCBKey(nil); got != "" {
		t.Errorf("methodCBKey(nil) = %q, want empty", got)
	}
}

// storedCBConfigs 从 CBSuite.Dump() 提取当前存储的服务级熔断配置。
func storedCBConfigs(t *testing.T, cbs *circuitbreak.CBSuite) map[string]circuitbreak.CBConfig {
	t.Helper()
	dump, ok := cbs.Dump().(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected Dump type %T", cbs.Dump())
	}
	cfgInfo, ok := dump["cb_config"].(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected cb_config type %T", dump["cb_config"])
	}
	svc, ok := cfgInfo["service"].(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected service type %T", cfgInfo["service"])
	}
	out := make(map[string]circuitbreak.CBConfig, len(svc))
	for k, v := range svc {
		cfg, ok := v.(circuitbreak.CBConfig)
		if !ok {
			t.Fatalf("unexpected config value type %T for key %q", v, k)
		}
		out[k] = cfg
	}
	return out
}

// TestCBApplier_RemovedMethodRestoresDefault 验证全量同步语义：新一轮配置中
// 被删除的 method 必须恢复 kitex 默认熔断参数（CBSuite 无删除 API）。
func TestCBApplier_RemovedMethodRestoresDefault(t *testing.T) {
	cbs := circuitbreak.NewCBSuite(methodCBKey)
	a := &cbConfigApplier{cbs: cbs}

	a.apply(`{"m1":{"enable":true,"err_rate":0.3,"min_sample":100},"m2":{"enable":true,"err_rate":0.4,"min_sample":200}}`)

	stored := storedCBConfigs(t, cbs)
	if len(stored) != 2 {
		t.Fatalf("expected 2 stored configs, got %d", len(stored))
	}
	if got := stored["m1"]; !got.Enable || got.ErrRate != 0.3 || got.MinSample != 100 {
		t.Errorf("m1 config = %+v, want {enable:true err_rate:0.3 min_sample:100}", got)
	}

	// 第二轮配置删除 m2 → m2 必须恢复默认
	a.apply(`{"m1":{"enable":true,"err_rate":0.3,"min_sample":100}}`)

	stored = storedCBConfigs(t, cbs)
	def := circuitbreak.GetDefaultCBConfig()
	if got := stored["m2"]; !got.Equals(&def) {
		t.Errorf("m2 config = %+v, want default %+v", got, def)
	}
}

// TestCBApplier_BadJSONKeepsPrevState 验证解析失败时不改动上一轮已应用的配置。
func TestCBApplier_BadJSONKeepsPrevState(t *testing.T) {
	cbs := circuitbreak.NewCBSuite(methodCBKey)
	a := &cbConfigApplier{cbs: cbs}

	a.apply(`{"m1":{"enable":true,"err_rate":0.3,"min_sample":100}}`)
	if n := a.apply("not-json{"); n != -1 {
		t.Errorf("apply(bad json) = %d, want -1", n)
	}

	stored := storedCBConfigs(t, cbs)
	if got := stored["m1"]; !got.Enable || got.ErrRate != 0.3 {
		t.Errorf("m1 config = %+v, want previous round value preserved", got)
	}
}

// TestCBApplier_NullValueTreatedAsRemoval 回归：json.Unmarshal 对 {"m":null}
// （值类型解析）产出零值 CBConfig（Enable=false）——等于对该方法关闭熔断。
// 钉住：null 按删除处理，恢复 kitex 默认熔断参数。
func TestCBApplier_NullValueTreatedAsRemoval(t *testing.T) {
	cbs := circuitbreak.NewCBSuite(methodCBKey)
	a := &cbConfigApplier{cbs: cbs}

	a.apply(`{"m1":{"enable":true,"err_rate":0.3,"min_sample":100}}`)
	a.apply(`{"m1":null}`)

	stored := storedCBConfigs(t, cbs)
	def := circuitbreak.GetDefaultCBConfig()
	if got := stored["m1"]; !got.Equals(&def) {
		t.Errorf("m1 config = %+v, want default %+v (null must not disable CB)", got, def)
	}
}
