package kitex

import (
	"testing"

	nnsfacade "github.com/oekit/nns"
	nacospkg "github.com/oekit/registry/nacos"
)

// TestFacade_ServiceClientMakesKitexResolver 验证 NNS 门面暴露的 ServiceClient
// 可直接构造本包 Resolver（原 ekit nns 包验收测试，拆分后随适配器落位本仓，
// 避免对 ekit-kitex 的跨模块测试反向依赖）。
func TestFacade_ServiceClientMakesKitexResolver(t *testing.T) {
	c := nnsfacade.New(nacospkg.Bootstrap{ServiceName: "svc"}, nnsfacade.WithMockMode(true))

	r := NewResolver(c.ServiceClient())
	if r == nil {
		t.Fatal("expected non-nil kitex resolver")
	}
}

// TestFacade_ServiceClientMakesKitexRegistry 验证 NNS 门面暴露的 ServiceClient
// 可直接构造本包 Registry。
func TestFacade_ServiceClientMakesKitexRegistry(t *testing.T) {
	c := nnsfacade.New(nacospkg.Bootstrap{ServiceName: "svc"}, nnsfacade.WithMockMode(true))

	reg := NewRegistry(c.ServiceClient())
	if reg == nil {
		t.Fatal("expected non-nil kitex registry")
	}
}
