package kitex

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
// listening 标志必须回滚（否则重试时因 listening=true 跳过监听器注册，
// 该 dataID 永久失去热更新）；回调保留供后台重试补注册（#315）。
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
	// 退避拉到 1h，隔离本用例对「手工二次 subscribe」的计数断言；
	// t.Cleanup 的 Close 会终止后台 goroutine。
	g, _ := NewGovernanceConfig(stub, WithGovernanceRetryBackoff(time.Hour, time.Hour))
	t.Cleanup(func() { _ = g.Close() })

	// 第一次：ListenConfig 失败
	if err := g.subscribe("data-id", func(_ string) {}); err == nil {
		t.Fatal("expected first subscribe to fail")
	}

	// listening 标记回滚，但回调保留（后台重试用）
	g.mu.RLock()
	remaining := len(g.callbacks["data-id"])
	listening := g.listening["data-id"]
	g.mu.RUnlock()
	if remaining != 1 {
		t.Errorf("expected callback retained for background retry, got %d", remaining)
	}
	if listening {
		t.Error("expected listening flag rolled back after ListenConfig error")
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

// waitSubscription 轮询等待订阅状态满足 cond（后台重试是异步的）。
func waitSubscription(t *testing.T, g *GovernanceConfig, dataID string, cond func(SubscriptionState) bool) SubscriptionState {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last SubscriptionState
	for time.Now().Before(deadline) {
		st, _ := g.SubscriptionState(dataID)
		last = st
		if cond(st) {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("subscription %s did not reach expected state, last=%+v", dataID, last)
	return last
}

// TestSubscriptionStates_ListenFailureThenRecovery 回归（#315）：ListenConfig
// 失败必须暴露可查降级状态，并由后台指数退避自动补注册恢复（含配置回调）。
func TestSubscriptionStates_ListenFailureThenRecovery(t *testing.T) {
	var listenCalls atomic.Int32
	stub := &stubConfigClient{
		listenConfigFunc: func(_ vo.ConfigParam) error {
			if listenCalls.Add(1) == 1 {
				return errors.New("nacos jitter")
			}
			return nil
		},
		getConfigFunc: func(_ vo.ConfigParam) (string, error) {
			return `{"qps_limit":10}`, nil
		},
	}
	g, err := NewGovernanceConfig(stub, WithGovernanceRetryBackoff(10*time.Millisecond, 40*time.Millisecond))
	if err != nil {
		t.Fatalf("NewGovernanceConfig: %v", err)
	}
	t.Cleanup(func() { _ = g.Close() })

	var updates atomic.Int32
	if err := g.subscribe("svc.retry", func(string) { updates.Add(1) }); err == nil {
		t.Fatal("first ListenConfig must fail")
	}

	st, ok := g.SubscriptionState("svc.retry")
	if !ok {
		t.Fatal("failure state must be observable")
	}
	if st.Active {
		t.Error("listener must be inactive after ListenConfig failure")
	}
	if st.LastError == nil || st.Attempts == 0 || st.NextRetry.IsZero() {
		t.Errorf("degraded state incomplete: %+v", st)
	}
	if !g.Degraded() {
		t.Error("Degraded() must report true while subscription is broken")
	}

	// 后台重试：第二次 ListenConfig 成功 + 首拉分发配置，状态自愈。
	st = waitSubscription(t, g, "svc.retry", func(s SubscriptionState) bool {
		return s.Active && s.LastError == nil
	})
	if st.Attempts != 0 {
		t.Errorf("Attempts must reset after recovery, got %d", st.Attempts)
	}
	if got := updates.Load(); got == 0 {
		t.Error("recovered subscription must pull and dispatch initial config")
	}
	deadline := time.Now().Add(2 * time.Second)
	for updates.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if updates.Load() == 0 {
		t.Error("initial config was not delivered after recovery")
	}
	if g.Degraded() {
		t.Error("Degraded() must clear after recovery")
	}
}

// TestSubscriptionStates_GetConfigFailureThenRecovery 首拉 GetConfig 失败
// （监听器已注册）同样计入降级状态并后台重试，而不是只打一条 Warn。
func TestSubscriptionStates_GetConfigFailureThenRecovery(t *testing.T) {
	var gets atomic.Int32
	stub := &stubConfigClient{
		listenConfigFunc: func(_ vo.ConfigParam) error { return nil },
		getConfigFunc: func(_ vo.ConfigParam) (string, error) {
			if gets.Add(1) == 1 {
				return "", errors.New("config service unavailable")
			}
			return `{"qps_limit":1}`, nil
		},
	}
	g, err := NewGovernanceConfig(stub, WithGovernanceRetryBackoff(10*time.Millisecond, 40*time.Millisecond))
	if err != nil {
		t.Fatalf("NewGovernanceConfig: %v", err)
	}
	t.Cleanup(func() { _ = g.Close() })

	var updates atomic.Int32
	if err := g.subscribe("svc.limit", func(string) { updates.Add(1) }); err != nil {
		t.Fatalf("GetConfig failure must not fail subscribe (listener is active), got %v", err)
	}
	st, _ := g.SubscriptionState("svc.limit")
	if !st.Active || st.LastError == nil {
		t.Fatalf("initial pull failure must be degraded but listener-active: %+v", st)
	}
	if !g.Degraded() {
		t.Error("Degraded() must report initial-pull failure")
	}

	waitSubscription(t, g, "svc.limit", func(s SubscriptionState) bool {
		return s.Active && s.LastError == nil && s.Attempts == 0
	})
	deadline := time.Now().Add(2 * time.Second)
	for updates.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if updates.Load() == 0 {
		t.Error("recovered initial pull must dispatch config to callbacks")
	}
}

// TestSubscriptionStates_CloseStopsRetry Close 必须终止后台重试，避免
// 对象关停后继续打 Nacos / 泄漏 goroutine。
func TestSubscriptionStates_CloseStopsRetry(t *testing.T) {
	var listenCalls atomic.Int32
	stub := &stubConfigClient{
		listenConfigFunc: func(_ vo.ConfigParam) error {
			listenCalls.Add(1)
			return errors.New("still down")
		},
	}
	g, err := NewGovernanceConfig(stub, WithGovernanceRetryBackoff(5*time.Millisecond, 10*time.Millisecond))
	if err != nil {
		t.Fatalf("NewGovernanceConfig: %v", err)
	}
	if err := g.subscribe("svc.down", func(string) {}); err == nil {
		t.Fatal("expected subscribe failure")
	}
	if err := g.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// 给在途重试一个收尾窗口，之后计数必须稳定。
	time.Sleep(30 * time.Millisecond)
	before := listenCalls.Load()
	time.Sleep(100 * time.Millisecond)
	if after := listenCalls.Load(); after != before {
		t.Fatalf("background retry continued after Close: %d -> %d", before, after)
	}
	if !g.Degraded() {
		t.Error("closed config must report degraded (no active subscriptions)")
	}
}

// TestNewGovernanceConfig_TemplateSubscriptionFailureRecorded 模板订阅失败
// （config.go 的 NewGovernanceConfig 路径）同样必须落入可查状态。
func TestNewGovernanceConfig_TemplateSubscriptionFailureRecorded(t *testing.T) {
	stub := &stubConfigClient{
		listenConfigFunc: func(_ vo.ConfigParam) error { return errors.New("nacos down") },
	}
	g, err := NewGovernanceConfig(stub,
		WithGovernanceTemplate("svc.governance"),
		WithGovernanceRetryBackoff(time.Hour, time.Hour),
	)
	if err != nil {
		t.Fatalf("NewGovernanceConfig: %v", err)
	}
	t.Cleanup(func() { _ = g.Close() })

	st, ok := g.SubscriptionState("svc.governance")
	if !ok {
		t.Fatal("template subscription failure must be recorded")
	}
	if st.Active || st.LastError == nil {
		t.Fatalf("template subscription must be degraded: %+v", st)
	}
}
