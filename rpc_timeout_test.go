package kitex

import (
	"testing"
	"time"

	"github.com/cloudwego/kitex/pkg/rpcinfo"
	"github.com/cloudwego/kitex/pkg/rpctimeout"
)

// riForMethod 构造仅携带方法名的最小 RPCInfo（rpctimeout.Container.Timeouts
// 只读 Invocation().MethodName）。
func riForMethod(method string) rpcinfo.RPCInfo {
	return rpcinfo.NewRPCInfo(nil, nil, rpcinfo.NewInvocation("", method), nil, nil)
}

// timeoutFor 从容器读取 method 的 rpc_timeout（未配置则回退全局默认）。
func timeoutFor(t *testing.T, tc *rpctimeout.Container, method string) time.Duration {
	t.Helper()
	to, ok := tc.Timeouts(riForMethod(method)).(*rpctimeout.RPCTimeout)
	if !ok {
		t.Fatalf("unexpected Timeouts type %T", tc.Timeouts(riForMethod(method)))
	}
	return to.RPCTimeout()
}

// TestRPCTimeoutApplier_NullValueTreatedAsRemoval 回归：{"m":null} 曾产出
// nil 指针直达 kitex DeepCopy（无 nil 保护）——nacos OnChange 的裸 goroutine
// 里 panic 直接打死进程。钉住：null 被过滤（不 panic、按删除处理回退默认
// 超时），合法条目保留，坏 JSON 返回 -1 且保留上一轮状态。
func TestRPCTimeoutApplier_NullValueTreatedAsRemoval(t *testing.T) {
	tc := rpctimeout.NewContainer()
	a := newRPCTimeoutApplier(tc, "svc.governance")

	n := a.applyConfig(`{"m1":{"conn_timeout_ms":50,"rpc_timeout_ms":300},"m2":null}`)
	if n != 1 {
		t.Fatalf("expected 1 applied method after null filter (m2 dropped), got %d", n)
	}
	if got := timeoutFor(t, tc, "m1"); got != 300*time.Millisecond {
		t.Errorf("m1 rpc_timeout = %v, want 300ms", got)
	}
	// m2 为 null：按删除处理 → 回退全局默认，而非零值/panic。
	def, ok := rpctimeout.CopyDefaultRPCTimeout().(*rpctimeout.RPCTimeout)
	if !ok {
		t.Fatal("unexpected default timeout type")
	}
	if got := timeoutFor(t, tc, "m2"); got != def.RPCTimeout() {
		t.Errorf("m2 (null) rpc_timeout = %v, want default %v", got, def.RPCTimeout())
	}

	// 全 null 配置：合法 JSON，产出空表（全部方法回退默认），非解析失败。
	if n := a.applyConfig(`{"m1":null}`); n != 0 {
		t.Errorf("all-null config should yield 0 applied methods, got %d", n)
	}
	if got := timeoutFor(t, tc, "m1"); got != def.RPCTimeout() {
		t.Errorf("m1 after all-null round = %v, want default %v", got, def.RPCTimeout())
	}

	// 坏 JSON：解析失败返回 -1，容器保留上一轮状态。
	if n := a.applyConfig("not-json{"); n != -1 {
		t.Errorf("bad JSON should return -1 (keep previous round), got %d", n)
	}
}
