package kitex

import (
	"context"
	"net"
	"testing"

	"github.com/cloudwego/kitex/pkg/discovery"
	"github.com/cloudwego/kitex/pkg/rpcinfo"
)

// TestNewDNSResolver_ReturnsNonNil 验证 NewDNSResolver 返回非 nil。
func TestNewDNSResolver_ReturnsNonNil(t *testing.T) {
	r := NewDNSResolver()
	if r == nil {
		t.Fatal("NewDNSResolver returned nil")
	}
}

// TestDNSResolver_TypeAlias 验证 type alias 编译期兼容。
func TestDNSResolver_TypeAlias(t *testing.T) {
	var _ = NewDNSResolver()
}

// TestNewDNSResolver_TargetReturnsServiceName 验证 Target() 返回服务名。
func TestNewDNSResolver_TargetReturnsServiceName(t *testing.T) {
	r := NewDNSResolver()
	// 用 mock endpoint info 测 Target
	mockEI := &mockEndpointInfo{serviceName: "test-svc"}
	got := r.Target(context.Background(), mockEI)
	if got != "test-svc" {
		t.Errorf("Target() = %q, want test-svc", got)
	}
}

// TestNewDNSResolver_NameReturnsDNS 验证 Name() = "dns"。
func TestNewDNSResolver_NameReturnsDNS(t *testing.T) {
	r := NewDNSResolver()
	if name := r.Name(); name != "dns" {
		t.Errorf("Name() = %q, want dns", name)
	}
}

// TestNewDNSResolver_ResolveInvalidHost 验证 Resolve 在无效 host 上返回 error
// (而不是 panic)。
func TestNewDNSResolver_ResolveInvalidHost(t *testing.T) {
	r := NewDNSResolver()
	// 故意用不存在的 host,期望返回 error
	_, err := r.Resolve(context.Background(), "this-host-does-not-exist.invalid:9999")
	if err == nil {
		t.Error("expected error for invalid host")
	}
}

// mockEndpointInfo 是 rpcinfo.EndpointInfo 的最小子集实现,只覆盖
// ServiceName()(其他方法未调用)。
type mockEndpointInfo struct {
	serviceName string
}

func (m *mockEndpointInfo) ServiceName() string                    { return m.serviceName }
func (m *mockEndpointInfo) Method() string                         { return "" }
func (m *mockEndpointInfo) Address() net.Addr                      { return nil }
func (m *mockEndpointInfo) DefaultTag(_ string, def string) string { return def }
func (m *mockEndpointInfo) Tag(string) (string, bool)              { return "", false }

// 确保 mock 满足 rpcinfo.EndpointInfo interface(编译期检查)
var _ rpcinfo.EndpointInfo = (*mockEndpointInfo)(nil)
var _ discovery.Resolver = NewDNSResolver()
