package kitex

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/cloudwego/kitex/pkg/discovery"
	"github.com/cloudwego/kitex/pkg/registry"
	"github.com/cloudwego/kitex/pkg/rpcinfo"

	registrymocks "github.com/yi-nology/ekit-registry/registrymocks"
	nnsclient "github.com/yi-nology/ekit-nns/nns/client"
	regpkg "github.com/yi-nology/ekit-registry/registry"
)

// newTestClientWithRegistrar returns a client with a capturing registrar
// and (optionally) static instances for a service.
func newTestClientWithRegistrar(reg regpkg.Registrar, staticFor string, static []regpkg.Instance) *nnsclient.ServiceClient {
	opts := []nnsclient.ClientOption{
		nnsclient.WithMockMode(true),
		nnsclient.WithRegistrar(reg),
		nnsclient.WithDiscoverer(&registrymocks.Discoverer{}),
		nnsclient.WithSubscriber(registrymocks.NewSubscriber()),
		nnsclient.WithConfigStore(registrymocks.NewConfigStore()),
	}
	if staticFor != "" {
		opts = append(opts, nnsclient.WithStaticInstances(staticFor, static))
	}
	return nnsclient.New(opts...)
}

// makeInfo constructs a *registry.Info with the given service name,
// TCP addr (host:port), and weight. Tags is optional.
func makeInfo(service, addr string, weight int, tags map[string]string) *registry.Info {
	host, port, _ := net.SplitHostPort(addr)
	portNum := 0
	if port != "" {
		// Parse "8080" → 8080
		for _, c := range port {
			if c < '0' || c > '9' {
				break
			}
			portNum = portNum*10 + int(c-'0')
		}
	}
	_ = host
	return &registry.Info{
		ServiceName: service,
		Addr:        &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: portNum},
		Weight:      weight,
		Tags:        tags,
	}
}

func TestRegistry_WithDefaultWeight(t *testing.T) {
	c := newTestClient()
	r := NewRegistry(c, WithDefaultWeight(50))
	nr := r.(*nnsRegistry)
	if nr.defaultWeight != 50 {
		t.Errorf("defaultWeight = %d, want 50", nr.defaultWeight)
	}
}

func TestRegistry_Register_BadAddr(t *testing.T) {
	c := newTestClient()
	r := NewRegistry(c).(*nnsRegistry)
	// Build info with a non-TCP addr to force ParseAddr failure.
	info := &registry.Info{ServiceName: "svc", Addr: &weirdAddr{}}
	if err := r.Register(info); err == nil {
		t.Error("expected error for unparseable address")
	}
}

func TestRegistry_Register_Happy(t *testing.T) {
	cap := &registrymocks.Registrar{}
	c := newTestClientWithRegistrar(cap, "", nil)
	r := NewRegistry(c)

	err := r.Register(makeInfo("svc", "10.0.0.1:8080", 0, nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cap.Registered) != 1 {
		t.Fatalf("expected 1 registration, got %d", len(cap.Registered))
	}
	inst := cap.Registered[0]
	if inst.Service != "svc" || inst.IP != "10.0.0.1" || inst.Port != 8080 {
		t.Errorf("unexpected instance: %+v", inst)
	}
	if inst.Weight != 10 {
		t.Errorf("default weight = %v, want 10", inst.Weight)
	}
}

func TestRegistry_Register_UsesInfoWeight(t *testing.T) {
	cap := &registrymocks.Registrar{}
	c := newTestClientWithRegistrar(cap, "", nil)
	r := NewRegistry(c, WithDefaultWeight(1))

	err := r.Register(makeInfo("svc", "10.0.0.1:8080", 77, nil))
	if err != nil {
		t.Fatal(err)
	}
	if cap.Registered[0].Weight != 77 {
		t.Errorf("weight = %v, want 77 (from info.Weight)", cap.Registered[0].Weight)
	}
}

func TestRegistry_Register_TagsPropagate(t *testing.T) {
	cap := &registrymocks.Registrar{}
	c := newTestClientWithRegistrar(cap, "", nil)
	r := NewRegistry(c)

	tags := map[string]string{"region": "us-west", "version": "v1"}
	if err := r.Register(makeInfo("svc", "10.0.0.1:8080", 0, tags)); err != nil {
		t.Fatal(err)
	}
	if got := cap.Registered[0].Metadata["region"]; got != "us-west" {
		t.Errorf("metadata region = %q, want us-west", got)
	}
	if got := cap.Registered[0].Metadata["version"]; got != "v1" {
		t.Errorf("metadata version = %q, want v1", got)
	}
}

func TestRegistry_Register_NoRegistrar(t *testing.T) {
	// Client without WithRegistrar; mock mode is on. GetRegistrar returns
	// nil → Register returns nil silently.
	c := nnsclient.New(
		nnsclient.WithMockMode(true),
	)
	r := NewRegistry(c)
	if err := r.Register(makeInfo("svc", "10.0.0.1:8080", 0, nil)); err != nil {
		t.Errorf("expected nil for mock mode, got %v", err)
	}
}

func TestRegistry_Register_RegistrarError(t *testing.T) {
	cap := &registrymocks.Registrar{RegErr: errors.New("register failed")}
	c := newTestClientWithRegistrar(cap, "", nil)
	r := NewRegistry(c)
	if err := r.Register(makeInfo("svc", "10.0.0.1:8080", 0, nil)); err == nil {
		t.Error("expected error from underlying registrar")
	}
}

func TestRegistry_Deregister_Happy(t *testing.T) {
	cap := &registrymocks.Registrar{}
	c := newTestClientWithRegistrar(cap, "", nil)
	r := NewRegistry(c)

	if err := r.Deregister(makeInfo("svc", "10.0.0.1:8080", 0, nil)); err != nil {
		t.Fatal(err)
	}
	if len(cap.Deregistered) != 1 {
		t.Errorf("expected 1 deregistration, got %d", len(cap.Deregistered))
	}
}

func TestRegistry_Deregister_BadAddr(t *testing.T) {
	c := newTestClient()
	r := NewRegistry(c)
	info := &registry.Info{ServiceName: "svc", Addr: &weirdAddr{}}
	if err := r.Deregister(info); err == nil {
		t.Error("expected error for unparseable address")
	}
}

func TestRegistry_Deregister_NoRegistrar(t *testing.T) {
	c := nnsclient.New(
		nnsclient.WithMockMode(true),
	)
	r := NewRegistry(c)
	if err := r.Deregister(makeInfo("svc", "10.0.0.1:8080", 0, nil)); err != nil {
		t.Errorf("expected nil for mock mode, got %v", err)
	}
}

func TestRegistry_Deregister_RegistrarError(t *testing.T) {
	cap := &registrymocks.Registrar{DeregErr: errors.New("deregister failed")}
	c := newTestClientWithRegistrar(cap, "", nil)
	r := NewRegistry(c)
	if err := r.Deregister(makeInfo("svc", "10.0.0.1:8080", 0, nil)); err == nil {
		t.Error("expected error from underlying registrar")
	}
}

func TestResolver_Target(t *testing.T) {
	c := newTestClient()
	r := NewResolver(c).(*nnsResolver)
	endpoint := rpcinfo.NewEndpointInfo("my-svc", "my-method", nil, nil)
	if got := r.Target(context.Background(), endpoint); got != "my-svc" {
		t.Errorf("Target() = %q, want my-svc", got)
	}
}

func TestResolver_Resolve_Happy(t *testing.T) {
	static := []regpkg.Instance{
		{IP: "10.0.0.1", Port: 8080, Weight: 10},
		{IP: "10.0.0.2", Port: 8080, Weight: 20, Metadata: map[string]string{"k": "v"}},
	}
	c := newTestClientWithRegistrar(&registrymocks.Registrar{}, "target-svc", static)
	r := NewResolver(c)

	res, err := r.Resolve(context.Background(), "target-svc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Cacheable || res.CacheKey != "target-svc" {
		t.Errorf("result metadata wrong: cacheable=%v key=%q", res.Cacheable, res.CacheKey)
	}
	if len(res.Instances) != 2 {
		t.Fatalf("expected 2 instances, got %d", len(res.Instances))
	}
	if res.Instances[0].Weight() != 10 {
		t.Errorf("first instance weight = %d, want 10", res.Instances[0].Weight())
	}
	v, ok := res.Instances[1].Tag("k")
	if !ok || v != "v" {
		t.Errorf("second instance tag k = (%q, %v), want (v, true)", v, ok)
	}
}

func TestResolver_Resolve_DiscoverError(t *testing.T) {
	// Build a client where the discoverer returns an error (not mock).
	c := nnsclient.New(
		nnsclient.WithRegistrar(&registrymocks.Registrar{}),
		nnsclient.WithDiscoverer(&registrymocks.Discoverer{Err: errors.New("nacos down")}),
		nnsclient.WithSubscriber(registrymocks.NewSubscriber()),
		nnsclient.WithConfigStore(registrymocks.NewConfigStore()),
	)
	r := NewResolver(c)
	_, err := r.Resolve(context.Background(), "target-svc")
	if err == nil {
		t.Error("expected error when underlying discover fails")
	}
}

func TestResolver_Diff(t *testing.T) {
	c := newTestClient()
	r := NewResolver(c).(*nnsResolver)

	prev := discovery.Result{
		Instances: []discovery.Instance{
			discovery.NewInstance("tcp", "10.0.0.1:8080", 10, nil),
		},
	}
	next := discovery.Result{
		Instances: []discovery.Instance{
			discovery.NewInstance("tcp", "10.0.0.1:8080", 10, nil),
			discovery.NewInstance("tcp", "10.0.0.2:8080", 10, nil),
		},
	}
	change, ok := r.Diff("key", prev, next)
	if !ok {
		t.Error("Diff should return ok=true when there are changes")
	}
	_ = change
}

// weirdAddr is a net.Addr whose Network/Address strings cause ParseAddr
// to fail (no colon → ParseAddr requires "host:port" format).
type weirdAddr struct{}

func (weirdAddr) Network() string { return "weird" }
func (weirdAddr) String() string  { return "no-colon-here" }
