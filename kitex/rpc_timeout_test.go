package kitex

import (
	"testing"
)

// TestParseRPCTimeoutConfig_NullValue 回归：{"m":null} 曾产出 nil 指针直达
// kitex DeepCopy（无 nil 保护）——nacos OnChange 的裸 goroutine 里 panic
// 直接打死进程。钉住：null 被过滤，合法条目保留，坏 JSON 返回 nil。
func TestParseRPCTimeoutConfig_NullValue(t *testing.T) {
	raw := parseRPCTimeoutConfig(`{"m1":{"conn_timeout_ms":50,"rpc_timeout_ms":300},"m2":null}`, "svc.governance")
	if raw == nil {
		t.Fatal("valid JSON with null entry must not be treated as parse failure")
	}
	if len(raw) != 1 {
		t.Fatalf("expected 1 entry after null filter (m2 dropped), got %d: %v", len(raw), raw)
	}
	if raw["m1"].RPCTimeoutMS != 300 {
		t.Errorf("m1 rpc_timeout_ms = %d, want 300", raw["m1"].RPCTimeoutMS)
	}

	if got := parseRPCTimeoutConfig(`{"m1":null}`, "x"); len(got) != 0 || got == nil {
		t.Errorf("all-null config should yield empty non-nil map, got %v", got)
	}
	if got := parseRPCTimeoutConfig("not-json{", "x"); got != nil {
		t.Errorf("bad JSON should return nil (keep previous round), got %v", got)
	}
}
