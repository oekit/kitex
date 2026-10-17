package kitex

import (
	"context"
	"fmt"

	kregistry "github.com/cloudwego/kitex/pkg/registry"
	nnsclient "github.com/oekit/nns/client"
	"github.com/oekit/registry/sdbridge"
)

// nnsRegistry 包装 nns ServiceClient，实现 Kitex registry.Registry。
//
// 使用 ServiceClient 的 registrar 接口注册/注销，
// 支持 mock 模式（registrar 为 nil 时静默跳过）。
type nnsRegistry struct {
	client        *nnsclient.ServiceClient
	defaultWeight int
}

var _ kregistry.Registry = (*nnsRegistry)(nil)

// RegistryOption 是 NewRegistry 的配置选项函数类型。
type RegistryOption func(*nnsRegistry)

// WithDefaultWeight 设置当 Kitex Info.Weight 为 0 时使用的默认权重。
func WithDefaultWeight(w int) RegistryOption {
	return func(r *nnsRegistry) { r.defaultWeight = w }
}

// NewRegistry 创建基于 NNS ServiceClient 的 Kitex Registry。
//
// 返回的 Registry 可直接传给 Kitex Server 的 server.WithRegistry 选项。
func NewRegistry(c *nnsclient.ServiceClient, opts ...RegistryOption) kregistry.Registry {
	r := &nnsRegistry{
		client:        c,
		defaultWeight: 10,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Register 将 Kitex 服务实例注册到 NNS 注册中心。
func (r *nnsRegistry) Register(info *kregistry.Info) error {
	if info == nil {
		return fmt.Errorf("nns-kitex registry: info is nil")
	}

	// weight <= 0 时使用 r.defaultWeight 兜底（委托 sdbridge.ToInstance）
	inst, err := sdbridge.ToInstance(info.ServiceName, info.Addr, info.Weight, r.defaultWeight, info.Tags)
	if err != nil {
		return fmt.Errorf("nns-kitex registry: %w", err)
	}

	// ServiceClient.Register 使用 Bootstrap.ServiceName 和 GetPodIP，
	// 但 Kitex 的 info.Addr 可能不同（如 0.0.0.0），这里用 info 的实际地址。
	if r.client.GetRegistrar() == nil {
		// mock 模式是有意跳过；非 mock 降级（启动时 Nacos 不可达）静默放行
		// 会让服务"正常启动但永不可被发现"且无当次调用信号——显式报错
		// 让 Required 注册语义生效（对齐 bootstrap Register 失败即回滚的行为）
		if r.client.MockMode() {
			return nil // mock 模式：静默跳过
		}
		return fmt.Errorf("nns-kitex registry: registrar unavailable (degraded, not mock mode); service would be undiscoverable")
	}
	return r.client.GetRegistrar().Register(context.Background(), inst)
}

// Deregister 从 NNS 注册中心注销 Kitex 服务实例。
// 与 Register 对称：保留 tags 并走 defaultWeight 兜底，
// 依赖 metadata/权重匹配的注册中心才能正确注销。
func (r *nnsRegistry) Deregister(info *kregistry.Info) error {
	if info == nil {
		return fmt.Errorf("nns-kitex registry: info is nil")
	}

	inst, err := sdbridge.ToInstance(info.ServiceName, info.Addr, info.Weight, r.defaultWeight, info.Tags)
	if err != nil {
		return fmt.Errorf("nns-kitex registry: %w", err)
	}

	if r.client.GetRegistrar() == nil {
		if r.client.MockMode() {
			return nil // mock 模式：静默跳过
		}
		return fmt.Errorf("nns-kitex registry: registrar unavailable (degraded, not mock mode)")
	}
	return r.client.GetRegistrar().Deregister(context.Background(), inst)
}
