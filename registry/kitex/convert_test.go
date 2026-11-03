package kitex

import (
	"testing"

	kdiscovery "github.com/cloudwego/kitex/pkg/discovery"
	"github.com/oekit/registry"
)

// TestInstancesToKitex_WeightFallback #92 回归:发现侧 Weight <= 0 回填
// kdiscovery.DefaultWeight,与注册侧 InfoFrom 的 defaultWeight 兜底同口径。
// 透传 0 会被 kitex weighted_balancer 判 "invalid weight" 视作无可用实例。
func TestInstancesToKitex_WeightFallback(t *testing.T) {
	instances := []registry.Instance{
		{IP: "10.0.0.1", Port: 8080, Weight: 0},  // 零值写法(WithStaticFallback 测试同形)
		{IP: "10.0.0.2", Port: 8081, Weight: -5}, // 负权重同样兜底
		{IP: "10.0.0.3", Port: 8082, Weight: 77}, // 显式正权重保留
	}
	result := InstancesToKitex(instances)
	if len(result) != 3 {
		t.Fatalf("len = %d, want 3", len(result))
	}

	if got := result[0].Weight(); got != kdiscovery.DefaultWeight {
		t.Errorf("zero weight -> %d, want DefaultWeight %d", got, kdiscovery.DefaultWeight)
	}
	if got := result[1].Weight(); got != kdiscovery.DefaultWeight {
		t.Errorf("negative weight -> %d, want DefaultWeight %d", got, kdiscovery.DefaultWeight)
	}
	if got := result[2].Weight(); got != 77 {
		t.Errorf("explicit weight -> %d, want 77", got)
	}
}

// TestInstancesToKitex_Empty 输入为空返回空切片(非 nil)。
func TestInstancesToKitex_Empty(t *testing.T) {
	result := InstancesToKitex(nil)
	if len(result) != 0 {
		t.Errorf("len = %d, want 0", len(result))
	}
}
