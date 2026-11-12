package kitex

import (
	"testing"
	"time"

	"github.com/cloudwego/kitex/pkg/rpcinfo"
	"github.com/cloudwego/kitex/pkg/rpctimeout"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

// TestGovernanceTemplate_TimeoutApplied 钉住 audit 5.2 阶段 2:统一模板
// dataID 的 timeout_ms 经 TemplateTimeoutOptions 应用到 rpctimeout 容器;
// 坏模板 fail-closed 不应用、不 panic。
func TestGovernanceTemplate_TimeoutApplied(t *testing.T) {
	var onChange func(string, string, string, string)
	stub := &stubConfigClient{
		getConfigFunc: func(vo.ConfigParam) (string, error) { return "", nil },
		listenConfigFunc: func(p vo.ConfigParam) error {
			onChange = p.OnChange
			return nil
		},
	}

	g, err := NewGovernanceConfig(stub, WithGovernanceTemplate("svc.governance"))
	if err != nil {
		t.Fatalf("NewGovernanceConfig: %v", err)
	}
	defer func() { _ = g.Close() }()

	if g.TemplateTimeoutOptions() == nil {
		t.Fatal("TemplateTimeoutOptions should be non-nil when template enabled")
	}

	// 推送模板:timeout_ms 生效
	onChange("svc.governance", "DEFAULT_GROUP", "", `{"timeout_ms":{"Method":200}}`)
	ri := rpcinfo.NewRPCInfo(
		rpcinfo.EmptyEndpointInfo(),
		rpcinfo.EmptyEndpointInfo(),
		rpcinfo.NewInvocation("", "Method"),
		rpcinfo.NewRPCConfig(),
		rpcinfo.NewRPCStats(),
	)
	timeouts := g.templateTC.Timeouts(ri)
	if timeouts == nil || timeouts.RPCTimeout() != 200*time.Millisecond {
		t.Errorf("template RPC timeout = %v, want 200ms", timeouts.RPCTimeout())
	}

	// 坏模板:fail-closed,上一轮配置保留
	onChange("svc.governance", "DEFAULT_GROUP", "", `{"timeout_ms":{"Method":-1}}`)
	timeouts2 := g.templateTC.Timeouts(ri)
	if timeouts2 == nil || timeouts2.RPCTimeout() != 200*time.Millisecond {
		t.Errorf("invalid template must not change applied config, RPC timeout = %v", timeouts2.RPCTimeout())
	}

	// 未启用模板:TemplateTimeoutOptions 为 nil
	g2, err := NewGovernanceConfig(stub)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g2.Close() }()
	if g2.TemplateTimeoutOptions() != nil {
		t.Error("TemplateTimeoutOptions should be nil without WithGovernanceTemplate")
	}
}

// TestGovernanceTemplate_KeyTranslationAndClear 回归：
// ① schema 的 "svc:Method"/"" key 原样透传给 kitex 容器时任何 RPC 都匹配
//
//	不上（容器按裸方法名 + "*" 通配），超时静默不生效——需翻译；
//
// ② 模板删掉 timeout_ms 段曾被当成 no-op，旧超时永久残留——需清空。
func TestGovernanceTemplate_KeyTranslationAndClear(t *testing.T) {
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

	newInvocation := func(method string) rpcinfo.RPCInfo {
		return rpcinfo.NewRPCInfo(
			rpcinfo.EmptyEndpointInfo(),
			rpcinfo.EmptyEndpointInfo(),
			rpcinfo.NewInvocation("", method),
			rpcinfo.NewRPCConfig(),
			rpcinfo.NewRPCStats(),
		)
	}

	// schema 风格 key：svc:Method → 裸方法名；"" → "*" 全局默认
	onChange("svc.governance", "DEFAULT_GROUP", "", `{"timeout_ms":{"svc:Method":300,"":5000}}`)
	timeouts := g.templateTC.Timeouts(newInvocation("Method"))
	if timeouts == nil || timeouts.RPCTimeout() != 300*time.Millisecond {
		t.Errorf(`"svc:Method" key must apply to Method RPC, got %v`, timeouts)
	}
	global := g.templateTC.Timeouts(newInvocation("Other"))
	if global == nil || global.RPCTimeout() != 5*time.Second {
		t.Errorf(`"" key must apply as wildcard default, got %v`, global)
	}

	// 服务级 key："svc"（无冒号）→ "*" 通配。修复前原样透传 "svc"，
	// 容器按方法名匹配永远命中不了，服务级超时静默失效。
	onChange("svc.governance", "DEFAULT_GROUP", "", `{"timeout_ms":{"svc":750}}`)
	svcLevel := g.templateTC.Timeouts(newInvocation("AnyMethod"))
	if svcLevel == nil || svcLevel.RPCTimeout() != 750*time.Millisecond {
		t.Errorf(`"svc" key must apply as per-client wildcard default, got %v`, svcLevel)
	}

	// 清空语义：timeout_ms 段删除 → 容器重置，回退 kitex 默认
	// （此前 len==0 直接 return，300ms/5s 永久残留）
	onChange("svc.governance", "DEFAULT_GROUP", "", `{"retry":{}}`)
	cleared := g.templateTC.Timeouts(newInvocation("Method"))
	fresh := rpctimeout.NewContainer().Timeouts(newInvocation("Method"))
	if cleared == nil || fresh == nil || cleared.RPCTimeout() != fresh.RPCTimeout() {
		t.Errorf("timeout_ms removal must reset container to kitex defaults, got %v want %v",
			cleared, fresh)
	}
}

// TestWithDataIDTemplate_ServerTemplateNoPlaceholderResidue 回归（#199）：
// 服务端模板必须剥离 {client} 占位符本身（连同紧随的分隔符），任意分隔符
// 形态的模板派生出的服务端 dataId 都不得残留未替换占位符——否则 Nacos 上
// 不存在该 dataId，服务端治理（limit 类目）静默失效。
func TestWithDataIDTemplate_ServerTemplateNoPlaceholderResidue(t *testing.T) {
	stub := &stubConfigClient{
		getConfigFunc:    func(vo.ConfigParam) (string, error) { return "", nil },
		listenConfigFunc: func(vo.ConfigParam) error { return nil },
	}

	cases := []struct {
		tmpl       string
		wantClient string // clientDataID("c1","s1","retry") 的期望值
		wantServer string // serverDataID("s1","limit") 的期望值
	}{
		{tmpl: "{client}.{server}.{category}", wantClient: "c1.s1.retry", wantServer: "s1.limit"},
		{tmpl: "{client}-{server}.{category}", wantClient: "c1-s1.retry", wantServer: "s1.limit"},
		{tmpl: "{client}_{server}_{category}", wantClient: "c1_s1_retry", wantServer: "s1_limit"},
		{tmpl: "{client}{server}{category}", wantClient: "c1s1retry", wantServer: "s1limit"},
		{tmpl: "{server}.{category}", wantClient: "s1.retry", wantServer: "s1.limit"},
	}

	for _, tc := range cases {
		g, err := NewGovernanceConfig(stub, WithDataIDTemplate(tc.tmpl))
		if err != nil {
			t.Fatalf("NewGovernanceConfig(%q): %v", tc.tmpl, err)
		}
		clientID := g.clientDataID("c1", "s1", "retry")
		if clientID != tc.wantClient {
			t.Errorf("template %q: client dataId = %q, want %q", tc.tmpl, clientID, tc.wantClient)
		}
		serverID := g.serverDataID("s1", "limit")
		if serverID != tc.wantServer {
			t.Errorf("template %q: server dataId = %q, want %q", tc.tmpl, serverID, tc.wantServer)
		}
		if containsPlaceholder(serverID) {
			t.Errorf("template %q: server dataId %q carries unsubstituted placeholder", tc.tmpl, serverID)
		}
		_ = g.Close()
	}
}

// containsPlaceholder 检查渲染结果是否残留 {xxx} 形态的未替换占位符。
func containsPlaceholder(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '{' {
			return true
		}
	}
	return false
}
