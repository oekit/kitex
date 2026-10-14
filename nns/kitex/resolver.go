package kitex

import (
	"context"

	"github.com/yi-nology/ekit-registry/registry/sdbridge"
	nnsclient "github.com/yi-nology/ekit-nns/nns/client"
	registrykitex "github.com/yi-nology/ekit-kitex/registry/kitex"
	"github.com/cloudwego/kitex/pkg/discovery"
	"github.com/cloudwego/kitex/pkg/rpcinfo"
)

const resolverName = "nns-kitex"

// resolverErrPrefix 是 Resolve 错误消息前缀。
const resolverErrPrefix = "nns-kitex resolver"

// nnsResolver 包装 nns ServiceClient，实现 Kitex discovery.Resolver。
//
// 与 registry/kitex.NewResolver（薄适配，直接调 discoverer.Discover）不同，
// 本 resolver 调用 ServiceClient.GetInstances，继承完整的降级链：
// 静态 → mock → 缓存 → discoverer(singleflight) → 降级缓存。
//
// 拉取 + 校验 + 错误包装的公共骨架委托给 [sdbridge.ResolverCore]，
// 本类型只负责 Kitex 实例类型转换与 discovery.Result 组装。
type nnsResolver struct {
	core *sdbridge.ResolverCore
}

var _ discovery.Resolver = (*nnsResolver)(nil)

// NewResolver 创建基于 NNS ServiceClient 的 Kitex Resolver。
//
// 返回的 Resolver 可直接传给 Kitex Client 的 client.WithResolver 选项。
func NewResolver(c *nnsclient.ServiceClient) discovery.Resolver {
	return &nnsResolver{
		core: sdbridge.NewResolverCore(c.GetInstances, resolverName, resolverErrPrefix),
	}
}

// Target 返回用于缓存 key 的服务描述（即服务名）。
func (r *nnsResolver) Target(_ context.Context, target rpcinfo.EndpointInfo) string {
	return target.ServiceName()
}

// Resolve 根据服务名从 NNS 获取实例列表（含 mock/静态/缓存/降级）。
func (r *nnsResolver) Resolve(ctx context.Context, desc string) (discovery.Result, error) {
	instances, err := r.core.Fetch(ctx, desc)
	if err != nil {
		return discovery.Result{}, err
	}
	return discovery.Result{
		Cacheable: r.core.Cacheable(),
		CacheKey:  desc,
		Instances: registrykitex.InstancesToKitex(instances),
	}, nil
}

// Diff 计算两次发现结果的增量变更。
func (r *nnsResolver) Diff(cacheKey string, prev, next discovery.Result) (discovery.Change, bool) {
	return discovery.DefaultDiff(cacheKey, prev, next)
}

// Name 返回 resolver 名称。
func (r *nnsResolver) Name() string {
	return r.core.Name()
}
