package kitex

import (
	"testing"
	"time"

	"github.com/yi-nology/ekit-bootstrap/bootstrap"
	"github.com/yi-nology/ekit-observability/shutdown"
	"github.com/cloudwego/hertz/pkg/common/test/assert"
	"github.com/cloudwego/kitex/server"
)

func TestRunServer_RegistersShutdownHook(t *testing.T) {
	mgr := shutdown.New(100 * time.Millisecond)
	app := bootstrap.NewApp(mgr)

	svr := server.NewServer()
	assert.NotNil(t, svr)

	// Should not panic
	RunServer(app, "test-kitex", svr)
}

func TestRunServer_MultipleServers(t *testing.T) {
	mgr := shutdown.New(100 * time.Millisecond)
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

func TestNewClientSuite_NilGov(t *testing.T) {
	suite := NewClientSuite("client-svc", "server-svc", nil,
		WithRetry(),
		WithRPCTimeout(),
		WithCircuitBreaker(),
	)
	assert.NotNil(t, suite)
	assert.DeepEqual(t, 0, len(suite.Options()))
}

func TestNewServerSuite_NilGov(t *testing.T) {
	suite := NewServerSuite("server-svc", nil, WithLimiter())
	assert.NotNil(t, suite)
	assert.DeepEqual(t, 0, len(suite.Options()))
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
