package kitex

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	kregistry "github.com/cloudwego/kitex/pkg/registry"
	"github.com/oekit/core/network"
	"github.com/oekit/registry"
	registrymocks "github.com/oekit/registry/registrymocks"
)

func TestNewRegistry(t *testing.T) {
	mock := &registrymocks.Registrar{}
	r := NewRegistry(mock)
	if r == nil {
		t.Fatal("NewRegistry returned nil")
	}
}

func TestNewRegistry_WithDefaultWeight(t *testing.T) {
	mock := &registrymocks.Registrar{}
	r := NewRegistry(mock, WithDefaultWeight(20))
	if r == nil {
		t.Fatal("NewRegistry returned nil")
	}
	_ = r
}

func TestRegistry_Register(t *testing.T) {
	mock := &registrymocks.Registrar{}
	r := NewRegistry(mock)

	addr := &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 8080}
	info := &kregistry.Info{
		ServiceName: "test-service",
		Addr:        addr,
		Weight:      15,
		Tags:        map[string]string{"env": "prod"},
	}

	if err := r.Register(info); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	if len(mock.Registered) != 1 {
		t.Fatalf("expected 1 registered, got %d", len(mock.Registered))
	}

	inst := mock.Registered[0]
	if inst.Service != "test-service" {
		t.Errorf("Service = %q, want %q", inst.Service, "test-service")
	}
	if inst.IP != "10.0.0.1" {
		t.Errorf("IP = %q, want %q", inst.IP, "10.0.0.1")
	}
	if inst.Port != 8080 {
		t.Errorf("Port = %d, want %d", inst.Port, 8080)
	}
	if inst.Weight != 15 {
		t.Errorf("Weight = %f, want 15", inst.Weight)
	}
	if inst.Metadata["env"] != "prod" {
		t.Errorf("Metadata[env] = %q, want %q", inst.Metadata["env"], "prod")
	}
}

func TestRegistry_Register_DefaultWeight(t *testing.T) {
	mock := &registrymocks.Registrar{}
	r := NewRegistry(mock, WithDefaultWeight(25))

	addr := &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 8080}
	info := &kregistry.Info{
		ServiceName: "test-service",
		Addr:        addr,
		Weight:      0,
	}

	if err := r.Register(info); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	if mock.Registered[0].Weight != 25 {
		t.Errorf("Weight = %f, want 25", mock.Registered[0].Weight)
	}
}

func TestRegistry_Deregister(t *testing.T) {
	mock := &registrymocks.Registrar{}
	r := NewRegistry(mock)

	addr := &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 8080}
	info := &kregistry.Info{
		ServiceName: "test-service",
		Addr:        addr,
	}

	if err := r.Deregister(info); err != nil {
		t.Fatalf("Deregister() error = %v", err)
	}

	if len(mock.Deregistered) != 1 {
		t.Fatalf("expected 1 deregistered, got %d", len(mock.Deregistered))
	}
}

func TestRegistry_Register_NilInfo(t *testing.T) {
	mock := &registrymocks.Registrar{}
	r := NewRegistry(mock)

	if err := r.Register(nil); err == nil {
		t.Error("expected error for nil info, got nil")
	}
}

func TestRegistry_Register_NilAddr(t *testing.T) {
	mock := &registrymocks.Registrar{}
	r := NewRegistry(mock)

	info := &kregistry.Info{
		ServiceName: "test-service",
		Addr:        nil,
	}

	if err := r.Register(info); err == nil {
		t.Error("expected error for nil addr, got nil")
	}
}

func TestRegistry_Register_RegistrarError(t *testing.T) {
	mock := &registrymocks.Registrar{RegErr: errors.New("register failed")}
	r := NewRegistry(mock)

	addr := &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 8080}
	info := &kregistry.Info{
		ServiceName: "test-service",
		Addr:        addr,
	}

	if err := r.Register(info); err == nil {
		t.Error("expected error, got nil")
	}
}

func TestToInstance(t *testing.T) {
	tests := []struct {
		name          string
		info          *kregistry.Info
		defaultName   string
		defaultWeight int
		wantService   string
		wantIP        string
		wantPort      int
		wantWeight    float64
		wantErr       bool
	}{
		{
			name:          "normal",
			info:          &kregistry.Info{ServiceName: "svc", Addr: &net.TCPAddr{IP: net.ParseIP("1.2.3.4"), Port: 9090}, Weight: 20},
			defaultWeight: 10,
			wantService:   "svc",
			wantIP:        "1.2.3.4",
			wantPort:      9090,
			wantWeight:    20,
		},
		{
			name:          "zero weight uses default",
			info:          &kregistry.Info{ServiceName: "svc", Addr: &net.TCPAddr{IP: net.ParseIP("1.2.3.4"), Port: 9090}, Weight: 0},
			defaultWeight: 30,
			wantService:   "svc",
			wantIP:        "1.2.3.4",
			wantPort:      9090,
			wantWeight:    30,
		},
		{
			name:          "empty service name uses default",
			info:          &kregistry.Info{ServiceName: "", Addr: &net.TCPAddr{IP: net.ParseIP("1.2.3.4"), Port: 9090}},
			defaultName:   "fallback-svc",
			defaultWeight: 10,
			wantService:   "fallback-svc",
			wantIP:        "1.2.3.4",
			wantPort:      9090,
			wantWeight:    10,
		},
		{
			name:    "empty service name without default fails closed",
			info:    &kregistry.Info{ServiceName: "", Addr: &net.TCPAddr{IP: net.ParseIP("1.2.3.4"), Port: 9090}},
			wantErr: true,
		},
		{
			name:    "nil info",
			info:    nil,
			wantErr: true,
		},
		{
			name:    "nil addr",
			info:    &kregistry.Info{ServiceName: "svc", Addr: nil},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &kitexRegistry{defaultServiceName: tt.defaultName, defaultWeight: tt.defaultWeight}
			inst, err := r.toInstance(tt.info)
			if (err != nil) != tt.wantErr {
				t.Fatalf("toInstance() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if inst.Service != tt.wantService {
				t.Errorf("Service = %q, want %q", inst.Service, tt.wantService)
			}
			if inst.IP != tt.wantIP {
				t.Errorf("IP = %q, want %q", inst.IP, tt.wantIP)
			}
			if inst.Port != tt.wantPort {
				t.Errorf("Port = %d, want %d", inst.Port, tt.wantPort)
			}
			if inst.Weight != tt.wantWeight {
				t.Errorf("Weight = %f, want %f", inst.Weight, tt.wantWeight)
			}
		})
	}
}

func TestGetLocalIP(t *testing.T) {
	ip := network.GetPodIP()
	if net.ParseIP(ip) == nil {
		t.Errorf("GetPodIP() returned invalid IP: %q", ip)
	}
}

// TestRegistry_Register_DefaultServiceName #93 回归:Kitex 填进
// registry.Info.ServiceName 的值与 ServiceInfo.ServiceName 不同源,实测会是
// 空串;WithDefaultServiceName 兜底后 Register/Deregister 不再被
// "registry: service name is empty" 卡死(注销对称)。
func TestRegistry_Register_DefaultServiceName(t *testing.T) {
	mock := &registrymocks.Registrar{}
	r := NewRegistry(mock, WithDefaultServiceName("fallback-svc"))

	addr := &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 8080}
	info := &kregistry.Info{ServiceName: "", Addr: addr}

	if err := r.Register(info); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if len(mock.Registered) != 1 || mock.Registered[0].Service != "fallback-svc" {
		t.Errorf("Registered = %+v, want Service fallback-svc", mock.Registered)
	}

	if err := r.Deregister(info); err != nil {
		t.Fatalf("Deregister() error = %v", err)
	}
	if len(mock.Deregistered) != 1 || mock.Deregistered[0].Service != "fallback-svc" {
		t.Errorf("Deregistered = %+v, want Service fallback-svc", mock.Deregistered)
	}
}

// TestRegistry_Register_EmptyServiceName_FailClosed 空服务名且无兜底时保持
// fail-closed(InfoFrom 的空名报错语义不变)。
func TestRegistry_Register_EmptyServiceName_FailClosed(t *testing.T) {
	mock := &registrymocks.Registrar{}
	r := NewRegistry(mock)

	info := &kregistry.Info{ServiceName: "", Addr: &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 8080}}
	if err := r.Register(info); err == nil {
		t.Error("expected error for empty service name without fallback, got nil")
	}
	if err := r.Deregister(info); err == nil {
		t.Error("expected error for empty service name without fallback, got nil")
	}
}

// blockingRegistrar 注册/注销阻塞到 ctx 到期或自身 delay——用于钉 #191 预算：
// kitex 接口层注入的 WithTimeout 必须真正传到底层 Registrar。
type blockingRegistrar struct {
	delay      time.Duration
	regCtx     context.Context
	deregCtx   context.Context
	regCalls   int
	deregCalls int
}

func (b *blockingRegistrar) Register(ctx context.Context, _ registry.Instance) error {
	b.regCalls++
	b.regCtx = ctx
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(b.delay):
		return nil
	}
}

func (b *blockingRegistrar) Deregister(ctx context.Context, _ registry.Instance) error {
	b.deregCalls++
	b.deregCtx = ctx
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(b.delay):
		return nil
	}
}

// TestRegistry_Register_TimeoutEnforced 钉 #191：Register/Deregister 的 ctx
// 带预算且到期即失败，不再是无截止的 Background。
func TestRegistry_Register_TimeoutEnforced(t *testing.T) {
	blk := &blockingRegistrar{delay: 5 * time.Second}
	r := NewRegistry(blk, WithRegisterTimeout(50*time.Millisecond), WithDeregisterTimeout(50*time.Millisecond))

	info := &kregistry.Info{ServiceName: "timeout-svc", Addr: &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 8080}}

	start := time.Now()
	err := r.Register(info)
	elapsed := time.Since(start)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Register err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed > time.Second {
		t.Fatalf("Register took %v, timeout budget not enforced", elapsed)
	}
	if blk.regCtx == nil || blk.regCtx.Err() == nil {
		t.Error("Registrar did not observe a deadline-bound ctx")
	}

	start = time.Now()
	err = r.Deregister(info)
	elapsed = time.Since(start)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Deregister err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed > time.Second {
		t.Fatalf("Deregister took %v, timeout budget not enforced", elapsed)
	}
}

// TestRegistry_Register_DefaultBudget 缺省构造即带 3s 预算（#191）：
// 未配选项时底层拿到的 ctx 也应有截止时间。
func TestRegistry_Register_DefaultBudget(t *testing.T) {
	blk := &blockingRegistrar{delay: 0}
	r := NewRegistry(blk)

	info := &kregistry.Info{ServiceName: "budget-svc", Addr: &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 8080}}
	if err := r.Register(info); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if dl, ok := blk.regCtx.Deadline(); !ok || dl.Before(time.Now()) {
		t.Error("default Register ctx has no future deadline (budget missing)")
	}
}

// TestRegistry_WithTimeout_Negative_Panics 非法预算构造期 fail-fast（#191）。
func TestRegistry_WithTimeout_Negative_Panics(t *testing.T) {
	mock := &registrymocks.Registrar{}
	assertPanic(t, func() { NewRegistry(mock, WithRegisterTimeout(-1)) })
	assertPanic(t, func() { NewRegistry(mock, WithDeregisterTimeout(0)) })
}

func assertPanic(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Error("expected panic, got none")
		}
	}()
	fn()
}
