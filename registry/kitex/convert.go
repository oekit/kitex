package kitex

import (
	kdiscovery "github.com/cloudwego/kitex/pkg/discovery"
	"github.com/oekit/registry"
	"github.com/oekit/registry/sdbridge"
)

// InstancesToKitex 将 ekit [registry.Instance] 列表转换为 Kitex [kdiscovery.Instance] 列表。
// 原位于 registry 核心包，因 SPI 不得依赖具体框架迁入本适配包。
//
// 端点与权重回填单源（#216）：network/addr 走 [sdbridge.InstanceEndpoint]
// （IP/Port→JoinHostPort，与此前本地的 net.JoinHostPort 拷贝逐值一致）；
// Weight <= 0 时回填 [kdiscovery.DefaultWeight]（与注册侧
// [sdbridge.ToInstance] / registry.InfoFrom 的 defaultWeight 兜底同口径，
// #92，回填口径单源 [sdbridge.WeightOrDefault]）：registry.Instance 的零值
// 写法不带 Weight，透传 0 会被 kitex weighted_balancer 判 "invalid weight"
// 视作无可用实例（fail-closed 503）。
func InstancesToKitex(instances []registry.Instance) []kdiscovery.Instance {
	result := make([]kdiscovery.Instance, 0, len(instances))
	for _, inst := range instances {
		network, addr := sdbridge.InstanceEndpoint(inst)
		// kitex 权重是 int：先按 int 截断，再以 WeightOrDefault 对 <=0 回填
		// ——若直接对 float64 判定后截断，(0,1) 区间的权重会截成 0 透传给
		// kitex weighted_balancer（invalid weight ⇒ fail-closed 503，#92）。
		weight := int(inst.Weight)
		weight = int(sdbridge.WeightOrDefault(float64(weight), float64(kdiscovery.DefaultWeight)))
		result = append(result, kdiscovery.NewInstance(
			network,
			addr,
			weight,
			inst.Metadata,
		))
	}
	return result
}
