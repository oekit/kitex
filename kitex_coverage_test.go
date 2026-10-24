package kitex

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

// stubConfigClient is a partial mock of [config_client.IConfigClient].
// We embed the interface and override only the methods we exercise.
// The un-overridden methods stay nil and are never called by ekit's
// wrappers, so no nil deref happens.
type stubConfigClient struct {
	config_client.IConfigClient

	// getConfigFunc is invoked by GetConfig calls; if nil, returns
	// ("", nil) by default.
	getConfigFunc func(vo.ConfigParam) (string, error)

	// listenConfigFunc is invoked by ListenConfig calls; the
	// OnChange callback registered on the vo.ConfigParam is captured
	// so tests can simulate config changes.
	listenConfigFunc func(vo.ConfigParam) error

	// cancelListenFunc is invoked by CancelListenConfig calls.
	cancelListenFunc func(vo.ConfigParam) error
}

func (s *stubConfigClient) GetConfig(p vo.ConfigParam) (string, error) {
	if s.getConfigFunc != nil {
		return s.getConfigFunc(p)
	}
	return "", nil
}

func (s *stubConfigClient) ListenConfig(p vo.ConfigParam) error {
	if s.listenConfigFunc != nil {
		return s.listenConfigFunc(p)
	}
	return nil
}

func (s *stubConfigClient) CancelListenConfig(p vo.ConfigParam) error {
	if s.cancelListenFunc != nil {
		return s.cancelListenFunc(p)
	}
	return nil
}

// --- config.go: GovernanceConfig ---

// TestNewGovernanceConfig_NilClient covers the error branch.
func TestNewGovernanceConfig_NilClient(t *testing.T) {
	_, err := NewGovernanceConfig(nil)
	if err == nil {
		t.Fatal("expected error for nil client, got nil")
	}
}

// TestNewGovernanceConfig_DefaultGroup covers the default group
// when WithGovernanceGroup is not used.
func TestNewGovernanceConfig_DefaultGroup(t *testing.T) {
	g, err := NewGovernanceConfig(&stubConfigClient{})
	if err != nil {
		t.Fatalf("NewGovernanceConfig: %v", err)
	}
	if g.group != "DEFAULT_GROUP" {
		t.Errorf("expected default group=DEFAULT_GROUP, got %q", g.group)
	}
}

// TestNewGovernanceConfig_WithGroup covers WithGovernanceGroup.
func TestNewGovernanceConfig_WithGroup(t *testing.T) {
	g, err := NewGovernanceConfig(&stubConfigClient{}, WithGovernanceGroup("CUSTOM_G"))
	if err != nil {
		t.Fatalf("NewGovernanceConfig: %v", err)
	}
	if g.group != "CUSTOM_G" {
		t.Errorf("expected group=CUSTOM_G, got %q", g.group)
	}
}

// TestNewGovernanceConfig_WithDataIDTemplate covers WithDataIDTemplate.
func TestNewGovernanceConfig_WithDataIDTemplate(t *testing.T) {
	g, err := NewGovernanceConfig(&stubConfigClient{},
		WithDataIDTemplate("custom.{client}.{server}.{category}"),
	)
	if err != nil {
		t.Fatalf("NewGovernanceConfig: %v", err)
	}
	if g.clientTmpl == "" {
		t.Error("expected clientTmpl to be set")
	}
}

// TestClientDataID covers the default client-side dataId rendering.
func TestClientDataID(t *testing.T) {
	g, _ := NewGovernanceConfig(&stubConfigClient{})
	got := g.clientDataID("client-a", "server-b", "retry")
	want := "client-a.server-b.retry"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

// TestServerDataID covers the default server-side dataId rendering.
func TestServerDataID(t *testing.T) {
	g, _ := NewGovernanceConfig(&stubConfigClient{})
	got := g.serverDataID("server-b", "limit")
	want := "server-b.limit"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

// TestSubscribe_HappyPath covers the subscribe function's main flow:
// initial pull + register callback for future updates.
func TestSubscribe_HappyPath(t *testing.T) {
	called := atomic.Int32{}
	stub := &stubConfigClient{
		getConfigFunc: func(p vo.ConfigParam) (string, error) {
			if p.DataId != "my-svc.cfg" {
				t.Errorf("expected DataId=my-svc.cfg, got %q", p.DataId)
			}
			return `{"key":"value"}`, nil
		},
		listenConfigFunc: func(p vo.ConfigParam) error {
			// Simulate a config change.
			if p.OnChange != nil {
				p.OnChange("ns", "g", p.DataId, `{"key":"new"}`)
			}
			return nil
		},
	}
	g, _ := NewGovernanceConfig(stub, WithGovernanceGroup("g"))

	if err := g.subscribe("my-svc.cfg", func(data string) {
		called.Add(1)
	}); err != nil {
		t.Errorf("subscribe: %v", err)
	}

	// The callback should have been invoked twice: once for the
	// initial pull, once for the simulated OnChange.
	if got := called.Load(); got != 2 {
		t.Errorf("expected 2 callback invocations, got %d", got)
	}
}

// TestSubscribe_InitialGetConfigError covers the path where the
// initial GetConfig fails — the error is logged but subscribe
// continues (no fatal failure).
func TestSubscribe_InitialGetConfigError(t *testing.T) {
	stub := &stubConfigClient{
		getConfigFunc: func(_ vo.ConfigParam) (string, error) {
			return "", errors.New("nacos unavailable")
		},
		listenConfigFunc: func(_ vo.ConfigParam) error { return nil },
	}
	g, _ := NewGovernanceConfig(stub)
	called := atomic.Int32{}
	if err := g.subscribe("data-id", func(_ string) { called.Add(1) }); err != nil {
		t.Errorf("subscribe: %v", err)
	}
	// The callback should NOT have been called (empty content on error).
	if got := called.Load(); got != 0 {
		t.Errorf("expected 0 callbacks (initial fetch failed), got %d", got)
	}
}

// TestSubscribe_ListenConfigError covers the error path where the
// SDK returns an error from ListenConfig. subscribe returns the
// error to the caller.
func TestSubscribe_ListenConfigError(t *testing.T) {
	stub := &stubConfigClient{
		getConfigFunc: func(_ vo.ConfigParam) (string, error) {
			return "", nil
		},
		listenConfigFunc: func(_ vo.ConfigParam) error {
			return errors.New("listen failed")
		},
	}
	g, _ := NewGovernanceConfig(stub)
	if err := g.subscribe("data-id", func(_ string) {}); err == nil {
		t.Fatal("expected error from ListenConfig, got nil")
	}
}

// TestSubscribe_EmptyContent covers the path where GetConfig returns
// empty content (key not yet set in Nacos). The callback must NOT
// be called.
func TestSubscribe_EmptyContent(t *testing.T) {
	called := atomic.Int32{}
	stub := &stubConfigClient{
		getConfigFunc: func(_ vo.ConfigParam) (string, error) {
			return "", nil // empty
		},
		listenConfigFunc: func(_ vo.ConfigParam) error { return nil },
	}
	g, _ := NewGovernanceConfig(stub)
	if err := g.subscribe("data-id", func(_ string) { called.Add(1) }); err != nil {
		t.Errorf("subscribe: %v", err)
	}
	if got := called.Load(); got != 0 {
		t.Errorf("expected 0 callbacks (empty content), got %d", got)
	}
}

// TestSubscribe_MultipleSubscribers verifies that the same dataId
// can be subscribed to by multiple callbacks. The OnChange handler
// must invoke ALL registered callbacks.
func TestSubscribe_MultipleSubscribers(t *testing.T) {
	var (
		mu    sync.Mutex
		count int
	)
	increment := func() {
		mu.Lock()
		count++
		mu.Unlock()
	}

	var onChangeCapture func(string)
	stub := &stubConfigClient{
		getConfigFunc: func(_ vo.ConfigParam) (string, error) { return "", nil },
		listenConfigFunc: func(p vo.ConfigParam) error {
			onChangeCapture = func(data string) { p.OnChange("ns", "g", "data-id", data) }
			return nil
		},
	}
	g, _ := NewGovernanceConfig(stub)

	if err := g.subscribe("data-id", func(_ string) { increment() }); err != nil {
		t.Fatalf("subscribe 1: %v", err)
	}
	if err := g.subscribe("data-id", func(_ string) { increment() }); err != nil {
		t.Fatalf("subscribe 2: %v", err)
	}

	onChangeCapture(`{"key":"v"}`)

	mu.Lock()
	defer mu.Unlock()
	if count != 2 {
		t.Errorf("expected 2 callback invocations, got %d", count)
	}
}

// TestGovernanceConfig_Close 验证 Close 取消全部 Nacos 监听并清空订阅状态，
// 且 Close 后可重新 subscribe（重新注册监听）。
func TestGovernanceConfig_Close(t *testing.T) {
	var mu sync.Mutex
	var cancelled []string
	stub := &stubConfigClient{
		getConfigFunc:    func(_ vo.ConfigParam) (string, error) { return "", nil },
		listenConfigFunc: func(_ vo.ConfigParam) error { return nil },
		cancelListenFunc: func(p vo.ConfigParam) error {
			mu.Lock()
			defer mu.Unlock()
			cancelled = append(cancelled, p.DataId)
			return nil
		},
	}
	g, _ := NewGovernanceConfig(stub)
	if err := g.subscribe("a.retry", func(string) {}); err != nil {
		t.Fatalf("subscribe a.retry: %v", err)
	}
	if err := g.subscribe("b.limit", func(string) {}); err != nil {
		t.Fatalf("subscribe b.limit: %v", err)
	}

	g.mu.RLock()
	nCallbacks, nListening := len(g.callbacks), len(g.listening)
	g.mu.RUnlock()
	if nCallbacks != 2 || nListening != 2 {
		t.Fatalf("expected 2 callbacks/listening before Close, got %d/%d", nCallbacks, nListening)
	}

	if err := g.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	mu.Lock()
	if len(cancelled) != 2 {
		t.Errorf("expected 2 CancelListenConfig calls, got %v", cancelled)
	}
	mu.Unlock()

	g.mu.RLock()
	nCallbacks, nListening = len(g.callbacks), len(g.listening)
	g.mu.RUnlock()
	if nCallbacks != 0 || nListening != 0 {
		t.Errorf("expected state cleared after Close, got %d callbacks / %d listening", nCallbacks, nListening)
	}

	// Close 后重新 subscribe 必须可用（重新注册监听器）
	if err := g.subscribe("a.retry", func(string) {}); err != nil {
		t.Errorf("re-subscribe after Close: %v", err)
	}
}

// TestGovernanceConfig_Close_NoListener 验证无监听时 Close 不报错、不触发取消。
func TestGovernanceConfig_Close_NoListener(t *testing.T) {
	cancels := 0
	var mu sync.Mutex
	stub := &stubConfigClient{
		cancelListenFunc: func(_ vo.ConfigParam) error {
			mu.Lock()
			defer mu.Unlock()
			cancels++
			return nil
		},
	}
	g, _ := NewGovernanceConfig(stub)
	if err := g.Close(); err != nil {
		t.Errorf("Close with no listener: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if cancels != 0 {
		t.Errorf("expected 0 CancelListenConfig calls, got %d", cancels)
	}
}

// TestGovernanceConfig_Close_CancelError 验证 CancelListenConfig 失败时返回错误。
func TestGovernanceConfig_Close_CancelError(t *testing.T) {
	stub := &stubConfigClient{
		cancelListenFunc: func(_ vo.ConfigParam) error { return errors.New("cancel failed") },
	}
	g, _ := NewGovernanceConfig(stub)
	g.mu.Lock()
	g.listening["x.retry"] = true
	g.mu.Unlock()
	if err := g.Close(); err == nil {
		t.Error("expected error when CancelListenConfig fails, got nil")
	}
}

// --- retry.go / rpc_timeout.go / circuit_breaker.go / limiter.go ---

// TestWithRetry verifies withRetry returns kitex client.Options.
// We don't have a real server, but the function must not panic and
// must return at least one option.
func TestWithRetry(t *testing.T) {
	stub := &stubConfigClient{
		getConfigFunc:    func(_ vo.ConfigParam) (string, error) { return "", nil },
		listenConfigFunc: func(_ vo.ConfigParam) error { return nil },
	}
	g, _ := NewGovernanceConfig(stub)
	opts, _ := withRetry("client-svc", "server-svc", g)
	if len(opts) == 0 {
		t.Error("expected at least one client.Option from withRetry")
	}
}

// TestWithRetry_BadJSON covers the "config is unparseable JSON"
// branch. The callback returns early; no panic, no error propagation.
func TestWithRetry_BadJSON(t *testing.T) {
	var onChangeCapture func(string)
	stub := &stubConfigClient{
		getConfigFunc: func(_ vo.ConfigParam) (string, error) { return "", nil },
		listenConfigFunc: func(p vo.ConfigParam) error {
			onChangeCapture = func(data string) { p.OnChange("ns", "g", "data-id", data) }
			return nil
		},
	}
	g, _ := NewGovernanceConfig(stub)
	opts, _ := withRetry("c", "s", g)
	if len(opts) == 0 {
		t.Fatal("expected non-empty options")
	}

	// Simulate a config change with bad JSON — must not panic.
	onChangeCapture("not-json{")

	// Test the happy path: valid JSON with one method.
	onChangeCapture(`{"echo":{"enable":true,"type":0}}`)
}

// TestWithRPCTimeout covers the happy path.
func TestWithRPCTimeout(t *testing.T) {
	stub := &stubConfigClient{
		getConfigFunc:    func(_ vo.ConfigParam) (string, error) { return "", nil },
		listenConfigFunc: func(_ vo.ConfigParam) error { return nil },
	}
	g, _ := NewGovernanceConfig(stub)
	opts := withRPCTimeout("c", "s", g)
	if len(opts) == 0 {
		t.Error("expected at least one client.Option from withRPCTimeout")
	}
}

// TestWithCircuitBreaker covers the happy path.
func TestWithCircuitBreaker(t *testing.T) {
	stub := &stubConfigClient{
		getConfigFunc:    func(_ vo.ConfigParam) (string, error) { return "", nil },
		listenConfigFunc: func(_ vo.ConfigParam) error { return nil },
	}
	g, _ := NewGovernanceConfig(stub)
	opts, closer := withCircuitBreaker("c", "s", g, nil)
	if len(opts) == 0 {
		t.Error("expected at least one client.Option from withCircuitBreaker")
	}
	if closer != nil {
		t.Error("expected nil closer when metrics disabled")
	}
}

// TestWithLimiter covers the happy path.
func TestWithLimiter(t *testing.T) {
	stub := &stubConfigClient{
		getConfigFunc:    func(_ vo.ConfigParam) (string, error) { return "", nil },
		listenConfigFunc: func(_ vo.ConfigParam) error { return nil },
	}
	g, _ := NewGovernanceConfig(stub)
	opts := withLimiter("server-svc", g, nil)
	if len(opts) == 0 {
		t.Error("expected at least one server.Option from withLimiter")
	}
}

// TestWithLimiter_BadJSON covers the bad-JSON branch in limiter.
// The callback must not panic, no error propagation.
func TestWithLimiter_BadJSON(t *testing.T) {
	var onChangeCapture func(string)
	stub := &stubConfigClient{
		getConfigFunc: func(_ vo.ConfigParam) (string, error) { return "", nil },
		listenConfigFunc: func(p vo.ConfigParam) error {
			onChangeCapture = func(data string) { p.OnChange("ns", "g", "data-id", data) }
			return nil
		},
	}
	g, _ := NewGovernanceConfig(stub)
	opts := withLimiter("s", g, nil)
	if len(opts) == 0 {
		t.Fatal("expected non-empty options")
	}

	// Simulate bad JSON — must not panic.
	onChangeCapture("not-json{")
	// Happy path: valid JSON.
	onChangeCapture(`{"connection_limit":100,"qps_limit":2000}`)
}

// TestGovernance_SubscribeFailure_LogsAndReturns covers the path where
// gov.subscribe returns an error inside withRetry/withRPCTimeout/withCircuitBreaker.
// The wrapper function logs the error and still returns the (now unused)
// kitex option. This is by design — the limiter/container is built
// before the subscription attempt.
func TestGovernance_SubscribeFailure_LogsAndReturns(t *testing.T) {
	stub := &stubConfigClient{
		getConfigFunc:    func(_ vo.ConfigParam) (string, error) { return "", nil },
		listenConfigFunc: func(_ vo.ConfigParam) error { return errors.New("listen failed") },
	}
	g, _ := NewGovernanceConfig(stub)

	// All wrappers must return at least one option even on subscribe failure.
	if opts, _ := withRetry("c", "s", g); len(opts) == 0 {
		t.Error("withRetry returned no options")
	}
	if opts := withRPCTimeout("c", "s", g); len(opts) == 0 {
		t.Error("withRPCTimeout returned no options")
	}
	if opts, _ := withCircuitBreaker("c", "s", g, nil); len(opts) == 0 {
		t.Error("withCircuitBreaker returned no options")
	}
	if opts := withLimiter("s", g, nil); len(opts) == 0 {
		t.Error("withLimiter returned no options")
	}
}

// --- observability.go ---
// ⚠️ v0.7.0 起 NewServerTracingSuite / NewClientTracingSuite 随嵌套模块
// kitexotel 迁出根包（根包依赖面不得含 apache/thrift v0.16.0，#37）——
// 对应构造器测试随之迁至
// observability/tracing/instrumentation/kitexotel/tracing_suite_test.go。

// --- log.go: SetOutput 覆盖随实现迁至 logx/kitexlog（kitexlog.TestKclogAdapter_SetOutput_NoOp） ---

// (helper import block, kept at the bottom for clarity)
var (
	_ = context.Background // suppress unused warning if tests are pruned
	_ atomic.Int32
)

// TestSubscribe_ListenErrorSkipsInitialPull 验证先听后拉语义：ListenConfig
// 失败时不做首拉（fail-fast），避免「已应用配置但无热更通道」的中间状态。
func TestSubscribe_ListenErrorSkipsInitialPull(t *testing.T) {
	var pulls atomic.Int32
	stub := &stubConfigClient{
		getConfigFunc: func(_ vo.ConfigParam) (string, error) {
			pulls.Add(1)
			return "", nil
		},
		listenConfigFunc: func(_ vo.ConfigParam) error {
			return errors.New("listen failed")
		},
	}
	g, _ := NewGovernanceConfig(stub)
	if err := g.subscribe("data-id", func(_ string) {}); err == nil {
		t.Fatal("expected subscribe to fail")
	}
	if got := pulls.Load(); got != 0 {
		t.Errorf("expected 0 GetConfig calls when ListenConfig fails, got %d", got)
	}
}

// TestSubscribe_ListenConfigErrorThenRetry 回归测试：ListenConfig 失败后
// listening 标志与回调注册必须回滚——否则重试时因 listening=true 跳过
// 监听器注册，该 dataID 永久失去热更新。
func TestSubscribe_ListenConfigErrorThenRetry(t *testing.T) {
	var listenCalls atomic.Int32
	var mu sync.Mutex
	fail := true
	stub := &stubConfigClient{
		listenConfigFunc: func(_ vo.ConfigParam) error {
			listenCalls.Add(1)
			mu.Lock()
			defer mu.Unlock()
			if fail {
				return errors.New("listen failed")
			}
			return nil
		},
	}
	g, _ := NewGovernanceConfig(stub)

	// 第一次：ListenConfig 失败
	if err := g.subscribe("data-id", func(_ string) {}); err == nil {
		t.Fatal("expected first subscribe to fail")
	}

	// 失败后回调注册应已回滚
	g.mu.RLock()
	remaining := len(g.callbacks["data-id"])
	g.mu.RUnlock()
	if remaining != 0 {
		t.Errorf("expected callback rolled back after ListenConfig error, got %d", remaining)
	}

	// 第二次：恢复后重试，监听器必须重新注册
	mu.Lock()
	fail = false
	mu.Unlock()
	if err := g.subscribe("data-id", func(_ string) {}); err != nil {
		t.Fatalf("retry subscribe: %v", err)
	}
	if got := listenCalls.Load(); got != 2 {
		t.Errorf("expected ListenConfig called twice (flag rolled back), got %d", got)
	}
}
