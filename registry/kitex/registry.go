package kitex

import (
	"context"
	"fmt"
	"time"

	kregistry "github.com/cloudwego/kitex/pkg/registry"
	ekiterr "github.com/oekit/pkg/errors"
	"github.com/oekit/registry"
	"github.com/oekit/registry/sdbridge"
)

// Register/Deregister 的缺省单次预算：kitex 的 kregistry.Registry 接口无
// ctx，框架在 server.Start 后自动 Register、server.Stop 时 Deregister——
// 不设预算则底层 Registrar（如 nacosimpl 经 invokeWithCtx 竞速）拿到的 ctx
// 永无截止时间，注册中心不可达时（反）注册退化为 SDK 内部阻塞时长，拖住
// 整个优雅关停序列。缺省 3s（#191 口径），单源
// [sdbridge.DefaultRegisterBudget]（#216），应显著小于 bootstrap 关停 closer
// 的预算（defaultTimeout），给错误处理与后续 closer 留出余量。

// kitexRegistry 将 ekit [registry.Registrar] 适配为 Kitex [registry.Registry]。
type kitexRegistry struct {
	reg                registry.Registrar
	defaultWeight      int
	defaultServiceName string
	registerTimeout    time.Duration
	deregisterTimeout  time.Duration
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

// WithRegisterTimeout 设置 Register 的单次预算（#191）。缺省 3s
// （[sdbridge.DefaultRegisterBudget]）。d <= 0 构造期 panic（fail-fast：预算是
// 防拖死关停序列的护栏，非法值静默接受等于没护栏）。
func WithRegisterTimeout(d time.Duration) RegistryOption {
	return func(r *kitexRegistry) {
		if d <= 0 {
			panic(fmt.Sprintf("kitex registry: WithRegisterTimeout requires d > 0, got %v", d))
		}
		r.registerTimeout = d
	}
}

// WithDeregisterTimeout 设置 Deregister 的单次预算（#191）。语义与约束同
// [WithRegisterTimeout]；本预算应小于 bootstrap 给关停 closer 的超时。
func WithDeregisterTimeout(d time.Duration) RegistryOption {
	return func(r *kitexRegistry) {
		if d <= 0 {
			panic(fmt.Sprintf("kitex registry: WithDeregisterTimeout requires d > 0, got %v", d))
		}
		r.deregisterTimeout = d
	}
}

// NewRegistry 将 ekit [registry.Registrar] 适配为 Kitex [registry.Registry]。
//
// 返回的 Registry 可直接传给 Kitex Server 的 [server.WithRegistry] 选项。
// Kitex 在 server.Start 后约 1 秒自动调用 Register，server.Stop 时调用
// Deregister——两者的单次预算缺省 3s（#191 口径，单源
// [sdbridge.DefaultRegisterBudget]），经 WithRegisterTimeout /
// WithDeregisterTimeout 覆盖；预算应小于 bootstrap 给关停 closer 的超时，
// 否则注册中心不可达时反注册会把关停序列拖满。
//
// reg 为 nil 时构造期 panic fail-fast（hertz#219 孪生口径，单源
// [sdbridge.EnsureNotNil]，#216）——配置错误启动即暴露，避免拖到框架内部
// goroutine 才炸出「未知来源 panic」。
func NewRegistry(reg registry.Registrar, opts ...RegistryOption) kregistry.Registry {
	sdbridge.EnsureNotNil(reg, "kitex: NewRegistry", "registrar")
	r := &kitexRegistry{
		reg:               reg,
		defaultWeight:     10,
		registerTimeout:   sdbridge.DefaultRegisterBudget,
		deregisterTimeout: sdbridge.DefaultRegisterBudget,
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
		return ekiterr.Wrapf(err, "kitex registry: register")
	}
	// 单次预算（#191 → #216 单源收口 [sdbridge.RegisterWithBudget]）：接口无
	// ctx，这里补 Background + 预算，底层 Registrar 的取消/超时语义自此生效
	// （budget <= 0 回退缺省预算；错误经 sdbridge 原样透传，前缀按本 adapter
	// 口径补齐）。
	if err := sdbridge.RegisterWithBudget(context.Background(), r.reg, inst, r.registerTimeout); err != nil {
		return ekiterr.Wrapf(err, "kitex registry: register %s", inst.Service)
	}
	return nil
}

// Deregister 从 ekit 注册中心注销 Kitex 服务实例。
func (r *kitexRegistry) Deregister(info *kregistry.Info) error {
	inst, err := r.toInstance(info)
	if err != nil {
		return ekiterr.Wrapf(err, "kitex registry: deregister")
	}
	// 单次预算（#191 → #216 单源收口 [sdbridge.DeregisterWithBudget]）：
	// server.Stop 关停路径的 Deregister 不再有无限预算，注册中心不可达时
	// 最多阻塞 deregisterTimeout。
	if err := sdbridge.DeregisterWithBudget(context.Background(), r.reg, inst, r.deregisterTimeout); err != nil {
		return ekiterr.Wrapf(err, "kitex registry: deregister %s", inst.Service)
	}
	return nil
}

// toInstance 将 Kitex registry.Info 转换为 ekit registry.Instance
// （weight <= 0 时使用 defaultWeight 兜底，委托 [sdbridge.ToInstance]；
// ServiceName 为空时使用 defaultServiceName 兜底，#93）。
func (r *kitexRegistry) toInstance(info *kregistry.Info) (registry.Instance, error) {
	if info == nil {
		return registry.Instance{}, ekiterr.New("info is nil")
	}
	name := info.ServiceName
	if name == "" {
		name = r.defaultServiceName
	}
	return sdbridge.ToInstance(name, info.Addr, info.Weight, r.defaultWeight, info.Tags)
}
