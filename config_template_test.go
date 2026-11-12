package kitex

import (
	"testing"
	"time"

	"github.com/cloudwego/kitex/pkg/rpcinfo"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

// newSvcRI 构造带目的服务名的 RPCInfo：TimeoutProvider 按 To.ServiceName +
// Invocation.MethodName 做 governance schema 的四档查找（#213）。
func newSvcRI(svc, method string) rpcinfo.RPCInfo {
	return rpcinfo.NewRPCInfo(
		rpcinfo.EmptyEndpointInfo(),
		rpcinfo.NewEndpointInfo(svc, "", nil, nil),
		rpcinfo.NewInvocation("", method),
		rpcinfo.NewRPCConfig(),
		rpcinfo.NewRPCStats(),
	)
}

// newTemplateGov 构造启用统一模板的 GovernanceConfig，返回实例与 onChange
// 注入点（测试用 stub 监听器捕获）。
func newTemplateGov(t *testing.T) (*GovernanceConfig, func(string, string, string, string)) {
	t.Helper()
	var onChange func(string, string, string, string)
	stub := &stubConfigClient{
		getConfigFunc:    func(vo.ConfigParam) (string, error) { return "", nil },
		listenConfigFunc: func(p vo.ConfigParam) error { onChange = p.OnChange; return nil },
	}
	g, err := NewGovernanceConfig(stub, WithGovernanceTemplate("svc.governance"))
	if err != nil {
		t.Fatalf("NewGovernanceConfig: %v", err)
	}
	t.Cleanup(func() { _ = g.Close() })
	return g, onChange
}

// assertTimeout 断言 provider 对 (svc, method) 的查找结果为 wantMs（负值表示
// 期望无命中——provider 返回 nil，kitex 回退默认超时）。
func assertTimeout(t *testing.T, p *governanceTimeoutProvider, svc, method string, wantMs int) {
	t.Helper()
	got := p.Timeouts(newSvcRI(svc, method))
	if wantMs < 0 {
		if got != nil {
			t.Errorf("lookup(%s,%s) = %v, want nil (no hit)", svc, method, got)
		}
		return
	}
	if got == nil {
		t.Fatalf("lookup(%s,%s) = nil, want %dms", svc, method, wantMs)
	}
	if got.RPCTimeout() != time.Duration(wantMs)*time.Millisecond {
		t.Errorf("lookup(%s,%s) RPC timeout = %v, want %dms", svc, method, got.RPCTimeout(), wantMs)
	}
}

// TestGovernanceTemplate_TimeoutApplied 钉住 audit 5.2 阶段 2：统一模板
// dataID 的 timeout_ms 经 TemplateTimeoutOptions 应用到 RPC 超时；坏模板
// fail-closed 不应用、不 panic。
func TestGovernanceTemplate_TimeoutApplied(t *testing.T) {
	g, onChange := newTemplateGov(t)

	if g.TemplateTimeoutOptions() == nil {
		t.Fatal("TemplateTimeoutOptions should be non-nil when template enabled")
	}

	// 推送模板：精确 key "svcA:Echo" 生效（含连接超时拆分口径）
	onChange("svc.governance", "DEFAULT_GROUP", "", `{"timeout_ms":{"svcA:Echo":200}}`)
	assertTimeout(t, g.templateTP, "svcA", "Echo", 200)
	got := g.templateTP.Timeouts(newSvcRI("svcA", "Echo"))
	if got.ConnectTimeout() != 100*time.Millisecond {
		t.Errorf("conn timeout = %v, want 100ms (fixed split)", got.ConnectTimeout())
	}

	// 坏模板（负值非法）：fail-closed，上一轮配置保留
	onChange("svc.governance", "DEFAULT_GROUP", "", `{"timeout_ms":{"svcA:Echo":-1}}`)
	assertTimeout(t, g.templateTP, "svcA", "Echo", 200)

	// 未启用模板：TemplateTimeoutOptions 为 nil
	g2, err := NewGovernanceConfig(&stubConfigClient{
		getConfigFunc:    func(vo.ConfigParam) (string, error) { return "", nil },
		listenConfigFunc: func(vo.ConfigParam) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g2.Close() }()
	if g2.TemplateTimeoutOptions() != nil {
		t.Error("TemplateTimeoutOptions should be nil without WithGovernanceTemplate")
	}
}

// TestGovernanceTemplate_ServiceAwareLookup 回归（#213）：service 维度必须
// 在查找面内被正确消费——精确 > 服务级 > 前缀最长 > 全局 四档有序。
// 旧实现把 key 翻译成裸方法名/"*"，多个 service 级 key 塌进同一个槽位，
// 生效超时随 map 遍历序随机漂移且跨服务串味。
func TestGovernanceTemplate_ServiceAwareLookup(t *testing.T) {
	g, onChange := newTemplateGov(t)

	onChange("svc.governance", "DEFAULT_GROUP", "",
		`{"timeout_ms":{"svcA:Echo":300,"svcB:Echo":900,"svcA":750,"com.example":500,"":5000}}`)

	// 精确档：svcA:Echo / svcB:Echo 各自命中，互不串味
	assertTimeout(t, g.templateTP, "svcA", "Echo", 300)
	assertTimeout(t, g.templateTP, "svcB", "Echo", 900)

	// 服务级档：svcA 的其他方法吃 svcA 的服务级默认；svcB 其他方法落到全局
	assertTimeout(t, g.templateTP, "svcA", "Other", 750)
	assertTimeout(t, g.templateTP, "svcB", "Other", 5000)

	// 前缀档（按「.」段最长匹配）：com.example.svc1 命中 com.example；
	// 无关前缀不命中
	assertTimeout(t, g.templateTP, "com.example.svc1", "Any", 500)
	assertTimeout(t, g.templateTP, "com.other.svc1", "Any", 5000)

	// 全局档："" 兜底一切
	assertTimeout(t, g.templateTP, "unknown-svc", "Any", 5000)

	// 单轮模板内多档并存的行为由查找顺序决定，与推送次数/次序无关
	onChange("svc.governance", "DEFAULT_GROUP", "", `{"timeout_ms":{"svcA":750,"svcA:Echo":300}}`)
	assertTimeout(t, g.templateTP, "svcA", "Echo", 300)
	assertTimeout(t, g.templateTP, "svcA", "Other", 750)
}

// TestGovernanceTemplate_ConnTimeoutSplit 钉住连接超时拆分口径：ms < 100
// 时连接超时取 ms/2（与旧翻译路径同口径）。
func TestGovernanceTemplate_ConnTimeoutSplit(t *testing.T) {
	g, onChange := newTemplateGov(t)

	onChange("svc.governance", "DEFAULT_GROUP", "", `{"timeout_ms":{"svcA:Echo":50}}`)
	got := g.templateTP.Timeouts(newSvcRI("svcA", "Echo"))
	if got == nil {
		t.Fatal("expected non-nil timeouts")
	}
	if got.RPCTimeout() != 50*time.Millisecond || got.ConnectTimeout() != 25*time.Millisecond {
		t.Errorf("rpc=%v conn=%v, want 50ms/25ms", got.RPCTimeout(), got.ConnectTimeout())
	}
}

// TestGovernanceTemplate_ClearResetsToDefault 清空语义：timeout_ms 段删除
// ⇒ 无任何命中，provider 返回 nil（kitex 回退默认超时）。
func TestGovernanceTemplate_ClearResetsToDefault(t *testing.T) {
	g, onChange := newTemplateGov(t)

	onChange("svc.governance", "DEFAULT_GROUP", "", `{"timeout_ms":{"svcA:Method":300,"":5000}}`)
	assertTimeout(t, g.templateTP, "svcA", "Method", 300)

	onChange("svc.governance", "DEFAULT_GROUP", "", `{"retry":{}}`)
	assertTimeout(t, g.templateTP, "svcA", "Method", -1)
	assertTimeout(t, g.templateTP, "unknown", "Any", -1)
}
