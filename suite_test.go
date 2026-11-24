package kitex

import (
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/common/test/assert"
	"github.com/cloudwego/kitex/server"
	"github.com/oekit/bootstrap"
	"github.com/oekit/core/lifecycle"
)

func TestRunServer_RegistersShutdownHook(t *testing.T) {
	mgr := lifecycle.New(100 * time.Millisecond)
	app := bootstrap.NewApp(mgr)

	svr := server.NewServer()
	assert.NotNil(t, svr)

	// Should not panic
	RunServer(app, "test-kitex", svr)
}

func TestRunServer_MultipleServers(t *testing.T) {
	mgr := lifecycle.New(100 * time.Millisecond)
	app := bootstrap.NewApp(mgr)

	svr1 := server.NewServer()
	svr2 := server.NewServer()

	RunServer(app, "kitex-1", svr1)
	RunServer(app, "kitex-2", svr2)
}

func TestClientSuite_OptionsReturnsSlice(t *testing.T) {
	s := &ClientSuite{}
	result := s.Options()
	assert.DeepEqual(t, 0, len(result))
}

func TestServerSuite_OptionsReturnsSlice(t *testing.T) {
	s := &ServerSuite{}
	result := s.Options()
	assert.DeepEqual(t, 0, len(result))
}

// TestNewClientSuite_NilGov_NoPolicies 未显式启用策略时 nil gov 仍可构造
// 空 Suite（不 panic）。
func TestNewClientSuite_NilGov_NoPolicies(t *testing.T) {
	suite := NewClientSuite("client-svc", "server-svc", nil)
	assert.NotNil(t, suite)
	assert.DeepEqual(t, 0, len(suite.Options()))
}

// TestNewClientSuite_NilGov_FailFast 回归（#315）：nil gov + 显式策略必须
// 构造期 panic，而不是旧实现的 Warn 后静默不装（治理层全裸奔）。
func TestNewClientSuite_NilGov_FailFast(t *testing.T) {
	cases := []struct {
		name string
		opt  ClientSuiteOption
	}{
		{"retry", WithRetry()},
		{"rpc_timeout", WithRPCTimeout()},
		{"circuit_breaker", WithCircuitBreaker()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatalf("NewClientSuite(nil, %s) must panic", tc.name)
				}
			}()
			NewClientSuite("client-svc", "server-svc", nil, tc.opt)
		})
	}
}

// TestNewServerSuite_NilGov_NoPolicies 未启用 limiter 时 nil gov 可构造空 Suite。
func TestNewServerSuite_NilGov_NoPolicies(t *testing.T) {
	suite := NewServerSuite("server-svc", nil)
	assert.NotNil(t, suite)
	assert.DeepEqual(t, 0, len(suite.Options()))
}

// TestNewServerSuite_NilGov_FailFast 回归（#315）：nil gov + WithLimiter 必须
// 构造期 panic。
func TestNewServerSuite_NilGov_FailFast(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("NewServerSuite(nil, WithLimiter()) must panic")
		}
	}()
	NewServerSuite("server-svc", nil, WithLimiter())
}

func TestWithRetry_SetsConfig(t *testing.T) {
	cfg := &clientSuiteConfig{}
	WithRetry()(cfg)
	if !cfg.retryEnabled {
		t.Error("expected retryEnabled to be true")
	}
}

func TestWithRPCTimeout_SetsConfig(t *testing.T) {
	cfg := &clientSuiteConfig{}
	WithRPCTimeout()(cfg)
	if !cfg.rpcTimeoutEnabled {
		t.Error("expected rpcTimeoutEnabled to be true")
	}
}

func TestWithCircuitBreaker_SetsConfig(t *testing.T) {
	cfg := &clientSuiteConfig{}
	WithCircuitBreaker()(cfg)
	if !cfg.circuitBreakEnabled {
		t.Error("expected circuitBreakEnabled to be true")
	}
}

func TestWithLimiter_SetsConfig(t *testing.T) {
	cfg := &serverSuiteConfig{}
	WithLimiter()(cfg)
	if !cfg.limiterEnabled {
		t.Error("expected limiterEnabled to be true")
	}
}
