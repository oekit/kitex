package kitex

import (
	"context"
	"net"
	"testing"

	kregistry "github.com/cloudwego/kitex/pkg/registry"
	registrykitex "github.com/oekit/kitex/registry/kitex"
	nnsclient "github.com/oekit/nns/client"
	"github.com/oekit/registry"
	registrymocks "github.com/oekit/registry/registrymocks"
)

// 注意：nns/client.New 每次返回独立实例（不再使用 sync.Once 单例）。
// 测试中通过 newTestClient 统一构造带 mock 接口的客户端，避免依赖 Nacos。

// newTestClient 创建一个带 mock 接口的独立 ServiceClient 实例。
func newTestClient() *nnsclient.ServiceClient {
	return nnsclient.New(
		nnsclient.WithMockMode(true),
		nnsclient.WithRegistrar(&registrymocks.Registrar{}),
		nnsclient.WithDiscoverer(&registrymocks.Discoverer{}),
		nnsclient.WithSubscriber(registrymocks.NewSubscriber()),
		nnsclient.WithConfigStore(registrymocks.NewConfigStore()),
	)
}

func TestNewResolver(t *testing.T) {
	c := newTestClient()
	r := NewResolver(c)
	if r == nil {
		t.Fatal("NewResolver returned nil")
	}
}

func TestResolver_Name(t *testing.T) {
	c := newTestClient()
	r := NewResolver(c)
	if r.Name() != "nns-kitex" {
		t.Errorf("Name() = %q, want %q", r.Name(), "nns-kitex")
	}
}

func TestResolver_Resolve_Empty(t *testing.T) {
	c := newTestClient()
	r := NewResolver(c)

	_, err := r.Resolve(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for empty instances, got nil")
	}
}

func TestNewRegistry(t *testing.T) {
	c := newTestClient()
	r := NewRegistry(c)
	if r == nil {
		t.Fatal("NewRegistry returned nil")
	}
}

func TestRegistry_Register_NilInfo(t *testing.T) {
	c := newTestClient()
	r := NewRegistry(c)

	if err := r.Register(nil); err == nil {
		t.Error("expected error for nil info, got nil")
	}
}

func TestRegistry_Deregister_NilInfo(t *testing.T) {
	c := newTestClient()
	r := NewRegistry(c)

	if err := r.Deregister(nil); err == nil {
		t.Error("expected error for nil info, got nil")
	}
}

// TestNnsRegistry_DeregisterKeepsTags 验证 Deregister 与 Register 对称：
// 保留 tags 并走 defaultWeight 兜底——否则依赖 metadata/权重匹配的
// 注册中心无法正确注销实例。
func TestNnsRegistry_DeregisterKeepsTags(t *testing.T) {
	capt := &registrymocks.Registrar{}
	c := newTestClientWithRegistrar(capt, "", nil)
	r := NewRegistry(c, WithDefaultWeight(7))

	tcpAddr, _ := net.ResolveTCPAddr("tcp", "10.0.0.5:9090")
	info := &kregistry.Info{
		ServiceName: "echo-svc",
		Addr:        tcpAddr,
		Weight:      0,
		Tags:        map[string]string{"ver": "v2"},
	}
	if err := r.Deregister(info); err != nil {
		t.Fatalf("Deregister: %v", err)
	}

	if len(capt.Deregistered) != 1 {
		t.Fatalf("expected 1 deregister call, got %d", len(capt.Deregistered))
	}
	inst := capt.Deregistered[0]
	if inst.Metadata["ver"] != "v2" {
		t.Errorf("Deregister lost tags: %+v", inst)
	}
	if inst.Weight != 7 {
		t.Errorf("Deregister weight = %v, want defaultWeight 7", inst.Weight)
	}
}

func TestToKitexInstances(t *testing.T) {
	instances := []registry.Instance{
		{IP: "10.0.0.1", Port: 8080, Weight: 10, Metadata: map[string]string{"k": "v"}},
	}
	result := registrykitex.InstancesToKitex(instances)
	if len(result) != 1 {
		t.Fatalf("len = %d, want 1", len(result))
	}
	if result[0].Address().String() != "10.0.0.1:8080" {
		t.Errorf("Address() = %q, want %q", result[0].Address().String(), "10.0.0.1:8080")
	}
	if result[0].Weight() != 10 {
		t.Errorf("Weight() = %d, want 10", result[0].Weight())
	}
	v, ok := result[0].Tag("k")
	if !ok || v != "v" {
		t.Errorf("Tag(k) = (%q, %v), want (v, true)", v, ok)
	}
}

// TestRegistry_DegradedRegistrar_Fails 回归：非 mock 降级（启动时 Nacos
// 不可达 → registrar 为 nil）时 Register 曾静默返回 nil——服务"正常启动
// 但永不可被发现"且无当次调用信号。钉住：mock 静默跳过，降级显式报错。
func TestRegistry_DegradedRegistrar_Fails(t *testing.T) {
	// 非 mock 且未注入 registrar 的 ServiceClient = 降级态
	c := nnsclient.New()
	if c.MockMode() {
		t.Fatal("expected non-mock degraded client")
	}
	r := NewRegistry(c)

	tcpAddr, _ := net.ResolveTCPAddr("tcp", "10.0.0.5:9090")
	info := &kregistry.Info{ServiceName: "echo-svc", Addr: tcpAddr}

	if err := r.Register(info); err == nil {
		t.Error("Register on degraded (non-mock) client must fail loudly, got nil")
	}
	if err := r.Deregister(info); err == nil {
		t.Error("Deregister on degraded (non-mock) client must fail loudly, got nil")
	}
}
