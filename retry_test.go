package kitex

import (
	"testing"

	"github.com/cloudwego/kitex/pkg/retry"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

// dumpRetryKeys 从 Container.Dump() 提取已注册的策略 key 集合。
func dumpRetryKeys(t *testing.T, rc *retry.Container) map[string]bool {
	t.Helper()
	dump, ok := rc.Dump().(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected Dump type %T", rc.Dump())
	}
	keys := make(map[string]bool)
	for k := range dump {
		if k != "msg" && k != "has_code_cfg" {
			keys[k] = true
		}
	}
	return keys
}

// TestClientSuite_Close 验证 Close 幂等，且对未启用 retry 的 Suite 安全。
func TestClientSuite_Close(t *testing.T) {
	stub := &stubConfigClient{
		getConfigFunc:    func(_ vo.ConfigParam) (string, error) { return "", nil },
		listenConfigFunc: func(_ vo.ConfigParam) error { return nil },
	}
	g, _ := NewGovernanceConfig(stub)

	// kitex client.Close() 只关闭自建的治理对象，不关闭经 WithRetryContainer
	// 注入的用户容器——Suite 必须自行提供 Close 释放 retry 容器内的熔断面板。
	suite := NewClientSuite("c", "s", g, WithRetry())
	if err := suite.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := suite.Close(); err != nil {
		t.Errorf("second Close (idempotent): %v", err)
	}

	empty := NewClientSuite("c", "s", g)
	if err := empty.Close(); err != nil {
		t.Errorf("empty suite Close: %v", err)
	}
}

// TestRetryApplier_RemovedMethodDeletesPolicy 验证全量同步语义：新一轮配置中
// 被删除的 method 必须从容器删除策略（回退为不重试），而非永久保留旧策略。
func TestRetryApplier_RemovedMethodDeletesPolicy(t *testing.T) {
	rc := retry.NewRetryContainer()
	a := &retryPolicyApplier{rc: rc}

	a.apply(`{"m1":{"enable":true,"type":0,"failure_policy":{"stop_policy":{"max_retry_times":2}}}}`)

	if !dumpRetryKeys(t, rc)["m1"] {
		t.Fatalf("expected m1 retryer registered")
	}

	// 第二轮配置删除 m1 → m1 策略必须被删除
	a.apply(`{"m2":{"enable":true,"type":0,"failure_policy":{"stop_policy":{"max_retry_times":1}}}}`)

	keys := dumpRetryKeys(t, rc)
	if keys["m1"] {
		t.Errorf("expected m1 policy deleted, got %v", keys)
	}
	if !keys["m2"] {
		t.Errorf("expected m2 policy registered, got %v", keys)
	}
}

// TestRetryApplier_BadJSONKeepsPrevState 验证解析失败时不改动上一轮已应用的策略。
func TestRetryApplier_BadJSONKeepsPrevState(t *testing.T) {
	rc := retry.NewRetryContainer()
	a := &retryPolicyApplier{rc: rc}

	a.apply(`{"m1":{"enable":true,"type":0,"failure_policy":{"stop_policy":{"max_retry_times":2}}}}`)
	if n := a.apply("not-json{"); n != -1 {
		t.Errorf("apply(bad json) = %d, want -1", n)
	}

	keys := dumpRetryKeys(t, rc)
	if !keys["m1"] {
		t.Errorf("expected m1 policy preserved after bad JSON, got %v", keys)
	}
}
