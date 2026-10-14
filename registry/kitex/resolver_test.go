package kitex

import (
	"context"
	"errors"
	"net"
	"testing"

	registrymocks "github.com/yi-nology/ekit-registry/registrymocks"
	"github.com/yi-nology/ekit-registry/registry"
	"github.com/cloudwego/kitex/pkg/discovery"
	"github.com/cloudwego/kitex/pkg/rpcinfo"
)

type mockEndpointInfo struct {
	serviceName string
}

func (m *mockEndpointInfo) ServiceName() string               { return m.serviceName }
func (m *mockEndpointInfo) Method() string                    { return "" }
func (m *mockEndpointInfo) Address() net.Addr                 { return nil }
func (m *mockEndpointInfo) Tag(key string) (string, bool)     { return "", false }
func (m *mockEndpointInfo) DefaultTag(key, def string) string { return def }

var _ rpcinfo.EndpointInfo = (*mockEndpointInfo)(nil)

func TestNewResolver(t *testing.T) {
	mock := &registrymocks.Discoverer{}
	r := NewResolver(mock)
	if r == nil {
		t.Fatal("NewResolver returned nil")
	}
}

func TestResolver_Target(t *testing.T) {
	mock := &registrymocks.Discoverer{}
	r := NewResolver(mock)

	target := &mockEndpointInfo{serviceName: "echo-service"}
	got := r.Target(context.Background(), target)
	if got != "echo-service" {
		t.Errorf("Target() = %q, want %q", got, "echo-service")
	}
}

func TestResolver_Resolve(t *testing.T) {
	mock := &registrymocks.Discoverer{
		Instances: []registry.Instance{
			{Service: "echo", IP: "10.0.0.1", Port: 8080, Weight: 10, Metadata: map[string]string{"v": "1"}},
			{Service: "echo", IP: "10.0.0.2", Port: 8081, Weight: 20, Metadata: map[string]string{"v": "2"}},
		},
	}
	r := NewResolver(mock)

	result, err := r.Resolve(context.Background(), "echo")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if !result.Cacheable {
		t.Error("Cacheable = false, want true")
	}
	if result.CacheKey != "echo" {
		t.Errorf("CacheKey = %q, want %q", result.CacheKey, "echo")
	}
	if len(result.Instances) != 2 {
		t.Fatalf("len(Instances) = %d, want 2", len(result.Instances))
	}

	inst0 := result.Instances[0]
	if inst0.Address().String() != "10.0.0.1:8080" {
		t.Errorf("Instances[0].Address() = %q, want %q", inst0.Address().String(), "10.0.0.1:8080")
	}
	if inst0.Weight() != 10 {
		t.Errorf("Instances[0].Weight() = %d, want 10", inst0.Weight())
	}
	v, ok := inst0.Tag("v")
	if !ok || v != "1" {
		t.Errorf("Instances[0].Tag(v) = (%q, %v), want (1, true)", v, ok)
	}

	inst1 := result.Instances[1]
	if inst1.Address().String() != "10.0.0.2:8081" {
		t.Errorf("Instances[1].Address() = %q, want %q", inst1.Address().String(), "10.0.0.2:8081")
	}
}

func TestResolver_Resolve_Empty(t *testing.T) {
	mock := &registrymocks.Discoverer{Instances: []registry.Instance{}}
	r := NewResolver(mock)

	_, err := r.Resolve(context.Background(), "echo")
	if err == nil {
		t.Error("expected error for empty instances, got nil")
	}
}

func TestResolver_Resolve_Error(t *testing.T) {
	mock := &registrymocks.Discoverer{Err: errors.New("nacos down")}
	r := NewResolver(mock)

	_, err := r.Resolve(context.Background(), "echo")
	if err == nil {
		t.Error("expected error, got nil")
	}
}

func TestResolver_Diff(t *testing.T) {
	mock := &registrymocks.Discoverer{}
	r := NewResolver(mock)

	prev := discovery.Result{
		Cacheable: true,
		CacheKey:  "echo",
		Instances: []discovery.Instance{
			discovery.NewInstance("tcp", "10.0.0.1:8080", 10, nil),
		},
	}
	next := discovery.Result{
		Cacheable: true,
		CacheKey:  "echo",
		Instances: []discovery.Instance{
			discovery.NewInstance("tcp", "10.0.0.1:8080", 10, nil),
			discovery.NewInstance("tcp", "10.0.0.2:8080", 10, nil),
		},
	}

	ch, changed := r.Diff("echo", prev, next)
	if !changed {
		t.Error("Diff() changed = false, want true")
	}
	if len(ch.Added) != 1 {
		t.Errorf("len(Added) = %d, want 1", len(ch.Added))
	}
}

func TestResolver_Name(t *testing.T) {
	mock := &registrymocks.Discoverer{}
	r := NewResolver(mock)

	if r.Name() != "ekit" {
		t.Errorf("Name() = %q, want %q", r.Name(), "ekit")
	}
}
