package kitex

import (
	"net"
	"strconv"

	kdiscovery "github.com/cloudwego/kitex/pkg/discovery"
	"github.com/oekit/registry"
)

// InstancesToKitex 将 ekit [registry.Instance] 列表转换为 Kitex [kdiscovery.Instance] 列表。
// 原位于 registry 核心包，因 SPI 不得依赖具体框架迁入本适配包。
func InstancesToKitex(instances []registry.Instance) []kdiscovery.Instance {
	result := make([]kdiscovery.Instance, 0, len(instances))
	for _, inst := range instances {
		result = append(result, kdiscovery.NewInstance(
			"tcp",
			net.JoinHostPort(inst.IP, strconv.Itoa(inst.Port)),
			int(inst.Weight),
			inst.Metadata,
		))
	}
	return result
}
