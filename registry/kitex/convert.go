package kitex

import (
	"net"
	"strconv"

	kdiscovery "github.com/cloudwego/kitex/pkg/discovery"
	"github.com/oekit/registry"
)

// InstancesToKitex 将 ekit [registry.Instance] 列表转换为 Kitex [kdiscovery.Instance] 列表。
// 原位于 registry 核心包，因 SPI 不得依赖具体框架迁入本适配包。
//
// Weight <= 0 时回填 [kdiscovery.DefaultWeight]（与注册侧
// [sdbridge.ToInstance] / registry.InfoFrom 的 defaultWeight 兜底同口径，
// #92）：registry.Instance 的零值写法不带 Weight，透传 0 会被 kitex
// weighted_balancer 判 "invalid weight" 视作无可用实例（fail-closed 503）。
func InstancesToKitex(instances []registry.Instance) []kdiscovery.Instance {
	result := make([]kdiscovery.Instance, 0, len(instances))
	for _, inst := range instances {
		weight := int(inst.Weight)
		if weight <= 0 {
			weight = kdiscovery.DefaultWeight
		}
		result = append(result, kdiscovery.NewInstance(
			"tcp",
			net.JoinHostPort(inst.IP, strconv.Itoa(inst.Port)),
			weight,
			inst.Metadata,
		))
	}
	return result
}
