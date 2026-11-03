package kitex

// #93 回归:Kitex 填进 registry.Info.ServiceName 的值与 ServiceInfo.ServiceName
// 不同源,实测会是空串——sdbridge.ToInstance → registry.InfoFrom 对空名硬报
// "registry: service name is empty",服务端起来就退。nnsRegistry 需在交下来
// 之前把兜底做完:显式 WithDefaultServiceName → ServiceClient Init 声明的
// 自身服务名。空名 fail-closed 语义保留。

import (
	"context"
	"net"
	"testing"

	"github.com/cloudwego/kitex/pkg/registry"
	nnsclient "github.com/oekit/nns/client"
	registrymocks "github.com/oekit/registry/registrymocks"
)

func emptyNameInfo() *registry.Info {
	return &registry.Info{
		ServiceName: "",
		Addr:        &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 8080},
	}
}

// TestRegistry_Register_FallbackNameFromOption 显式 WithDefaultServiceName 兜底
// (Register/Deregister 对称)。
func TestRegistry_Register_FallbackNameFromOption(t *testing.T) {
	cap := &registrymocks.Registrar{}
	c := newTestClientWithRegistrar(cap, "", nil)
	r := NewRegistry(c, WithDefaultServiceName("opt-svc"))

	if err := r.Register(emptyNameInfo()); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if len(cap.Registered) != 1 || cap.Registered[0].Service != "opt-svc" {
		t.Errorf("Registered = %+v, want Service opt-svc", cap.Registered)
	}

	if err := r.Deregister(emptyNameInfo()); err != nil {
		t.Fatalf("Deregister() error = %v", err)
	}
	if len(cap.Deregistered) != 1 || cap.Deregistered[0].Service != "opt-svc" {
		t.Errorf("Deregistered = %+v, want Service opt-svc", cap.Deregistered)
	}
}

// TestRegistry_Register_FallbackNameFromInit 回退到 ServiceClient Init 声明的
// 自身服务名(LocalServiceInit 记录,经 GetLocalService 可见)——消费方不再需要
// 自包 namedRegistry 注名。非 mock 路径 + mock ConfigStore:走 LocalServiceInit
// 真实分支(BindServiceBasic 记录 FullName)。
func TestRegistry_Register_FallbackNameFromInit(t *testing.T) {
	cap := &registrymocks.Registrar{}
	c := nnsclient.New(
		nnsclient.WithRegistrar(cap),
		nnsclient.WithDiscoverer(&registrymocks.Discoverer{}),
		nnsclient.WithSubscriber(registrymocks.NewSubscriber()),
		nnsclient.WithConfigStore(registrymocks.NewConfigStore()),
	)
	if _, err := c.LocalServiceInit(context.Background(), "init-svc"); err != nil {
		t.Fatalf("LocalServiceInit: %v", err)
	}
	r := NewRegistry(c)

	if err := r.Register(emptyNameInfo()); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if len(cap.Registered) != 1 || cap.Registered[0].Service != "init-svc" {
		t.Errorf("Registered = %+v, want Service init-svc (Init 声明名兜底失效)", cap.Registered)
	}

	if err := r.Deregister(emptyNameInfo()); err != nil {
		t.Fatalf("Deregister() error = %v", err)
	}
	if len(cap.Deregistered) != 1 || cap.Deregistered[0].Service != "init-svc" {
		t.Errorf("Deregistered = %+v, want Service init-svc", cap.Deregistered)
	}
}

// TestRegistry_Register_EmptyNameNoFallback_FailClosed 无任何兜底来源时,
// InfoFrom 的空名 fail-closed 语义保留(报错而非静默注册空名)。
func TestRegistry_Register_EmptyNameNoFallback_FailClosed(t *testing.T) {
	cap := &registrymocks.Registrar{}
	c := newTestClientWithRegistrar(cap, "", nil)
	r := NewRegistry(c)

	if err := r.Register(emptyNameInfo()); err == nil {
		t.Error("expected error for empty service name without fallback, got nil")
	}
	if err := r.Deregister(emptyNameInfo()); err == nil {
		t.Error("expected error for empty service name without fallback, got nil")
	}
}

// TestRegistry_Register_OptionOverridesInitName 显式 option 优先于 Init 名
// (显式调用方意图优先)。
func TestRegistry_Register_OptionOverridesInitName(t *testing.T) {
	cap := &registrymocks.Registrar{}
	c := nnsclient.New(
		nnsclient.WithRegistrar(cap),
		nnsclient.WithDiscoverer(&registrymocks.Discoverer{}),
		nnsclient.WithSubscriber(registrymocks.NewSubscriber()),
		nnsclient.WithConfigStore(registrymocks.NewConfigStore()),
	)
	if _, err := c.LocalServiceInit(context.Background(), "init-svc"); err != nil {
		t.Fatalf("LocalServiceInit: %v", err)
	}
	r := NewRegistry(c, WithDefaultServiceName("opt-svc"))

	if err := r.Register(emptyNameInfo()); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if len(cap.Registered) != 1 || cap.Registered[0].Service != "opt-svc" {
		t.Errorf("Registered = %+v, want Service opt-svc (option 优先)", cap.Registered)
	}
}

// 空名兜底链不应影响显式服务名:info.ServiceName 非空时原样使用。
func TestRegistry_Register_ExplicitNameWins(t *testing.T) {
	cap := &registrymocks.Registrar{}
	c := newTestClientWithRegistrar(cap, "", nil)
	r := NewRegistry(c, WithDefaultServiceName("opt-svc"))

	info := emptyNameInfo()
	info.ServiceName = "explicit-svc"
	if err := r.Register(info); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if len(cap.Registered) != 1 || cap.Registered[0].Service != "explicit-svc" {
		t.Errorf("Registered = %+v, want Service explicit-svc", cap.Registered)
	}
}
