package kitex

import (
	"context"
	"fmt"
	"time"

	kregistry "github.com/cloudwego/kitex/pkg/registry"
	nnsclient "github.com/oekit/nns/client"
	"github.com/oekit/registry/sdbridge"
)

// defaultRegisterTimeout 是 Register/Deregister 的缺省单次预算（#212，与
// registry/kitex 的 #191 同根因同口径）：kitex 的 kregistry.Registry 接口
// 无 ctx，框架在 server.Start 后自动 Register、server.Stop 时 Deregister
// ——不设预算则底层 Registrar（如 nacosimpl 经 invokeWithCtx 竞速）拿到的
// ctx 永无截止时间，注册中心不可达时（反）注册退化为 SDK 内部阻塞时长，
// 拖住整个优雅关停序列。缺省 3s，应显著小于 bootstrap 关停 closer 的预算。
const defaultRegisterTimeout = 3 * time.Second

// nnsRegistry 包装 nns ServiceClient，实现 Kitex registry.Registry。
//
// 使用 ServiceClient 的 registrar 接口注册/注销，
// 支持 mock 模式（registrar 为 nil 时静默跳过）。
type nnsRegistry struct {
	client             *nnsclient.ServiceClient
	defaultWeight      int
	defaultServiceName string
	registerTimeout    time.Duration
	deregisterTimeout  time.Duration
}

var _ kregistry.Registry = (*nnsRegistry)(nil)

// RegistryOption 是 NewRegistry 的配置选项函数类型。
type RegistryOption func(*nnsRegistry)

// WithDefaultWeight 设置当 Kitex Info.Weight 为 0 时使用的默认权重。
func WithDefaultWeight(w int) RegistryOption {
	return func(r *nnsRegistry) { r.defaultWeight = w }
}

// WithDefaultServiceName 设置 registry.Info.ServiceName 为空时的兜底服务名，
// 优先于 ServiceClient Init 声明的自身服务名。
//
// Kitex 填进 registry.Info.ServiceName 的值与 ServiceInfo.ServiceName 不同源，
// 实测会是空串（#93）；不兜底则 sdbridge.ToInstance 直接报
// "registry: service name is empty"，注册失败导致服务端进程起来就退出。
func WithDefaultServiceName(name string) RegistryOption {
	return func(r *nnsRegistry) { r.defaultServiceName = name }
}

// WithRegisterTimeout 设置 Register 的单次预算（#212）。缺省 3s
// （defaultRegisterTimeout）。d <= 0 构造期 panic（fail-fast：预算是
// 防拖死关停序列的护栏，非法值静默接受等于没护栏；与 registry/kitex
// 同款语义）。
func WithRegisterTimeout(d time.Duration) RegistryOption {
	return func(r *nnsRegistry) {
		if d <= 0 {
			panic(fmt.Sprintf("nns-kitex registry: WithRegisterTimeout requires d > 0, got %v", d))
		}
		r.registerTimeout = d
	}
}

// WithDeregisterTimeout 设置 Deregister 的单次预算（#212）。语义与约束同
// [WithRegisterTimeout]；本预算应小于 bootstrap 给关停 closer 的超时。
func WithDeregisterTimeout(d time.Duration) RegistryOption {
	return func(r *nnsRegistry) {
		if d <= 0 {
			panic(fmt.Sprintf("nns-kitex registry: WithDeregisterTimeout requires d > 0, got %v", d))
		}
		r.deregisterTimeout = d
	}
}

// NewRegistry 创建基于 NNS ServiceClient 的 Kitex Registry。
//
// 返回的 Registry 可直接传给 Kitex Server 的 server.WithRegistry 选项。
func NewRegistry(c *nnsclient.ServiceClient, opts ...RegistryOption) kregistry.Registry {
	r := &nnsRegistry{
		client:            c,
		defaultWeight:     10,
		registerTimeout:   defaultRegisterTimeout,
		deregisterTimeout: defaultRegisterTimeout,
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

	// weight <= 0 时使用 r.defaultWeight 兜底（委托 sdbridge.ToInstance）；
	// ServiceName 为空时按 resolveServiceName 兜底（#93）
	inst, err := sdbridge.ToInstance(r.resolveServiceName(info.ServiceName), info.Addr, info.Weight, r.defaultWeight, info.Tags)
	if err != nil {
		return fmt.Errorf("nns-kitex registry: %w", err)
	}

	// ServiceClient.Register 使用 Bootstrap.ServiceName 和 GetPodIP，
	// 但 Kitex 的 info.Addr 可能不同（如 0.0.0.0），这里用 info 的实际地址。
	if r.client.Registrar() == nil {
		// mock 模式是有意跳过；非 mock 降级（启动时 Nacos 不可达）静默放行
		// 会让服务"正常启动但永不可被发现"且无当次调用信号——显式报错
		// 让 Required 注册语义生效（对齐 bootstrap Register 失败即回滚的行为）
		if r.client.MockMode() {
			return nil // mock 模式：静默跳过
		}
		return fmt.Errorf("nns-kitex registry: registrar unavailable (degraded, not mock mode); service would be undiscoverable")
	}
	// 单次预算（#212，同 registry/kitex 的 #191）：接口无 ctx，这里以
	// WithTimeout(Background) 补上，底层 Registrar 的取消/超时语义自此生效。
	ctx, cancel := context.WithTimeout(context.Background(), r.registerTimeout)
	defer cancel()
	return r.client.Registrar().Register(ctx, inst)
}

// Deregister 从 NNS 注册中心注销 Kitex 服务实例。
// 与 Register 对称：保留 tags、走 defaultWeight 与服务名兜底，
// 依赖 metadata/权重匹配的注册中心才能正确注销。
func (r *nnsRegistry) Deregister(info *kregistry.Info) error {
	if info == nil {
		return fmt.Errorf("nns-kitex registry: info is nil")
	}

	inst, err := sdbridge.ToInstance(r.resolveServiceName(info.ServiceName), info.Addr, info.Weight, r.defaultWeight, info.Tags)
	if err != nil {
		return fmt.Errorf("nns-kitex registry: %w", err)
	}

	if r.client.Registrar() == nil {
		if r.client.MockMode() {
			return nil // mock 模式：静默跳过
		}
		return fmt.Errorf("nns-kitex registry: registrar unavailable (degraded, not mock mode)")
	}
	// 单次预算（#212）：server.Stop 关停路径的 Deregister 不再有无限预算，
	// 注册中心不可达时最多阻塞 deregisterTimeout。
	ctx, cancel := context.WithTimeout(context.Background(), r.deregisterTimeout)
	defer cancel()
	return r.client.Registrar().Deregister(ctx, inst)
}

// resolveServiceName 对空服务名兜底（#93）：Kitex 填进 registry.Info.ServiceName
// 的值与 ServiceInfo.ServiceName 不同源，实测会是空串，而 sdbridge.ToInstance
// 对空名硬报 "registry: service name is empty"（fail-closed 保留）。
// 兜底顺序：显式 WithDefaultServiceName → ServiceClient Init 声明的自身服务名
// （LocalServiceInit 记录，经 GetLocalService 可见）。均为空时保持 fail-closed。
func (r *nnsRegistry) resolveServiceName(name string) string {
	if name != "" {
		return name
	}
	if r.defaultServiceName != "" {
		return r.defaultServiceName
	}
	if ls := r.client.GetLocalService(); ls != nil {
		return ls.ServiceName.FullName
	}
	return ""
}
