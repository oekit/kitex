package kitex

import (
	"context"
	"fmt"

	kregistry "github.com/cloudwego/kitex/pkg/registry"
	"github.com/oekit/registry"
	"github.com/oekit/registry/sdbridge"
)

// kitexRegistry 将 ekit [registry.Registrar] 适配为 Kitex [registry.Registry]。
type kitexRegistry struct {
	reg                registry.Registrar
	defaultWeight      int
	defaultServiceName string
}

var _ kregistry.Registry = (*kitexRegistry)(nil)

// RegistryOption 是 [NewRegistry] 的配置选项函数类型。
type RegistryOption func(*kitexRegistry)

// WithDefaultWeight 设置当 Kitex Info.Weight 为 0 时使用的默认权重。
// 默认值 10（与 Kitex [discovery.DefaultWeight] 一致）。
func WithDefaultWeight(w int) RegistryOption {
	return func(r *kitexRegistry) { r.defaultWeight = w }
}

// WithDefaultServiceName 设置当 Kitex Info.ServiceName 为空时使用的兜底服务名。
//
// Kitex 填进 registry.Info.ServiceName 的值与 ServiceInfo.ServiceName 不同源，
// 实测会是空串（#93）；不兜底则 sdbridge.ToInstance → registry.InfoFrom 硬报
// "registry: service name is empty"，注册失败导致服务端进程起来就退出。
// 不设置且 Info.ServiceName 为空时保持 fail-closed（原报错语义不变）。
func WithDefaultServiceName(name string) RegistryOption {
	return func(r *kitexRegistry) { r.defaultServiceName = name }
}

// NewRegistry 将 ekit [registry.Registrar] 适配为 Kitex [registry.Registry]。
//
// 返回的 Registry 可直接传给 Kitex Server 的 [server.WithRegistry] 选项。
// Kitex 在 server.Start 后约 1 秒自动调用 Register，server.Stop 时调用 Deregister。
func NewRegistry(reg registry.Registrar, opts ...RegistryOption) kregistry.Registry {
	r := &kitexRegistry{
		reg:           reg,
		defaultWeight: 10,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Register 将 Kitex 服务实例注册到 ekit 注册中心。
func (r *kitexRegistry) Register(info *kregistry.Info) error {
	inst, err := r.toInstance(info)
	if err != nil {
		return fmt.Errorf("kitex registry: register: %w", err)
	}
	if err := r.reg.Register(context.Background(), inst); err != nil {
		return fmt.Errorf("kitex registry: register %s: %w", inst.Service, err)
	}
	return nil
}

// Deregister 从 ekit 注册中心注销 Kitex 服务实例。
func (r *kitexRegistry) Deregister(info *kregistry.Info) error {
	inst, err := r.toInstance(info)
	if err != nil {
		return fmt.Errorf("kitex registry: deregister: %w", err)
	}
	if err := r.reg.Deregister(context.Background(), inst); err != nil {
		return fmt.Errorf("kitex registry: deregister %s: %w", inst.Service, err)
	}
	return nil
}

// toInstance 将 Kitex registry.Info 转换为 ekit registry.Instance
// （weight <= 0 时使用 defaultWeight 兜底，委托 [sdbridge.ToInstance]；
// ServiceName 为空时使用 defaultServiceName 兜底，#93）。
func (r *kitexRegistry) toInstance(info *kregistry.Info) (registry.Instance, error) {
	if info == nil {
		return registry.Instance{}, fmt.Errorf("info is nil")
	}
	name := info.ServiceName
	if name == "" {
		name = r.defaultServiceName
	}
	return sdbridge.ToInstance(name, info.Addr, info.Weight, r.defaultWeight, info.Tags)
}
