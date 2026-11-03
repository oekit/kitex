package kitex

import (
	"errors"
	"net"
	"testing"

	kregistry "github.com/cloudwego/kitex/pkg/registry"
	"github.com/oekit/core/network"
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
