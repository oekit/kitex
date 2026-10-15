package kitex

import (
	"github.com/cloudwego/kitex/pkg/discovery"
	kitexdnsresolver "github.com/kitex-contrib/resolver-dns"
)

// DNSResolver 是 kitex-contrib/resolver-dns 的别名 type,kitex 客户端
// 把它当 discovery.Resolver 用(供 client.WithResolver(option) 接入)。
//
// 官方依据:https://github.com/kitex-contrib/resolver-dns (v0.1.0)
//
// 业务场景: K8s/容器编排下,服务注册用 Headless Service / ClusterIP
// 暴露,业务用 DNS 名做服务发现,ekit 把 DNSResolver 喂给 kitex client
// 替代静态 IP 列表。
//
// 业务用法:
//
//	cli, _ := myservice.NewClient("myservice",
//	    client.WithResolver(mw.NewDNSResolver()),
//	    ...
//	)
//
// 注意: DNS lookup 在 K8s 内网/Headless Service 下,每个 pod 有
// 独立 IP(轮询 round-robin),适合 stateless 微服务;stateful
// 服务(有状态)用 Headless Service 配 hostname 替代。
type DNSResolver = discovery.Resolver

// NewDNSResolver 桥接 kitex-contrib/resolver-dns.NewDNSResolver。
// 业务用 client.WithResolver(ekit.NewDNSResolver()) 即可。
//
// 局限: kitex-contrib/resolver-dns 只解析 A/AAAA 记录(SRV/Service
// 头解析需要业务自己实现),如需 SRV 记录可换 kitex-contrib/registry-zookeeper
// 或 ekit 内部 NNS。
func NewDNSResolver() DNSResolver {
	return kitexdnsresolver.NewDNSResolver()
}
