package kitex

import (
	"context"

	"github.com/cloudwego/kitex/pkg/discovery"
	"github.com/cloudwego/kitex/pkg/rpcinfo"
	"github.com/oekit/registry/registry"
	"github.com/oekit/registry/registry/sdbridge"
)

const resolverName = "ekit"

// resolverErrPrefix 是 Resolve 错误消息前缀。
const resolverErrPrefix = "kitex resolver"

// kitexResolver 将 ekit [registry.Discoverer] 适配为 Kitex [discovery.Resolver]。
//
// Kitex lbcache 默认每 5 秒调用一次 Resolve 拉取实例列表，
// 再通过 Diff 计算增量推送给负载均衡器。不需要 ekit Subscriber（推送）。
//
// 拉取 + 校验 + 错误包装的公共骨架委托给 [sdbridge.ResolverCore]，
// 本类型只负责 Kitex 实例类型转换与 discovery.Result 组装。
type kitexResolver struct {
	core *sdbridge.ResolverCore
}

var _ discovery.Resolver = (*kitexResolver)(nil)

// ResolverOption 是 [NewResolver] 的配置选项函数类型。
type ResolverOption func(*kitexResolver)

// NewResolver 将 ekit [registry.Discoverer] 适配为 Kitex [discovery.Resolver]。
//
// 返回的 Resolver 可直接传给 Kitex Client 的 [client.WithResolver] 选项。
func NewResolver(disc registry.Discoverer, opts ...ResolverOption) discovery.Resolver {
	fetch := func(ctx context.Context, service string) ([]registry.Instance, error) {
		return disc.Discover(ctx, service)
	}
	r := &kitexResolver{
		core: sdbridge.NewResolverCore(fetch, resolverName, resolverErrPrefix,
			sdbridge.WithFetchErr(sdbridge.DiscoverFetchErr)),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Target 返回用于缓存 key 的服务描述（即服务名）。
func (r *kitexResolver) Target(_ context.Context, target rpcinfo.EndpointInfo) string {
	return target.ServiceName()
}

// Resolve 根据服务名从 ekit 注册中心拉取实例列表。
func (r *kitexResolver) Resolve(ctx context.Context, desc string) (discovery.Result, error) {
	insts, err := r.core.Fetch(ctx, desc)
	if err != nil {
		return discovery.Result{}, err
	}
	return discovery.Result{
		Cacheable: r.core.Cacheable(),
		CacheKey:  desc,
		Instances: InstancesToKitex(insts),
	}, nil
}

// Diff 计算两次发现结果的增量变更。
func (r *kitexResolver) Diff(cacheKey string, prev, next discovery.Result) (discovery.Change, bool) {
	return discovery.DefaultDiff(cacheKey, prev, next)
}

// Name 返回 resolver 名称。
func (r *kitexResolver) Name() string {
	return r.core.Name()
}
