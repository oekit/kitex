package kitex

import (
	"encoding/json"
	"sync"

	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/pkg/circuitbreak"
	"github.com/cloudwego/kitex/pkg/rpcinfo"
	"github.com/oekit/metrics/resilience"
)

// methodCBKey 以 RPC 方法名作为服务级熔断配置 key，与 Nacos 配置格式（裸方法名）一致。
// kitex 默认的 RPCInfo2Key 生成 "from/to/method"，要求配置带双服务名前缀且依赖
// client 侧设置自身服务名；CBSuite 为每个 client 独享，方法名即唯一 key。
func methodCBKey(ri rpcinfo.RPCInfo) string {
	if ri == nil {
		return ""
	}
	return ri.To().Method()
}

// cbConfigApplier 把 Nacos circuit_break 配置全量应用到 CBSuite。
// kitex CBSuite 没有删除 API，配置中被移除的 method 用默认参数覆盖，
// 与 rpc_timeout 的整表替换保持一致的全量同步语义。
type cbConfigApplier struct {
	cbs *circuitbreak.CBSuite

	mu   sync.Mutex // OnChange 异步触发，串行化对 prev 的访问
	prev map[string]struct{}
}

// apply 解析并应用一轮配置，返回配置中的 method 数；解析失败返回 -1，
// 并保留上一轮已应用的状态。
func (a *cbConfigApplier) apply(data string) int {
	var rawConfigs map[string]*circuitbreak.CBConfig
	if err := json.Unmarshal([]byte(data), &rawConfigs); err != nil {
		govLog.Error("parse circuit_break config failed", "err", err)
		return -1
	}
	// 用指针解析以区分 null 与零值对象：null 曾被当成零值 CBConfig
	// （Enable=false）应用——等于对该方法关闭熔断；null 按删除处理。
	configs := make(map[string]circuitbreak.CBConfig, len(rawConfigs))
	for method, cfg := range rawConfigs {
		if cfg == nil {
			govLog.Warn("circuit_break config null value treated as removal", "method", method)
			continue
		}
		configs[method] = *cfg
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for method := range a.prev {
		if _, ok := configs[method]; !ok {
			a.cbs.UpdateServiceCBConfig(method, circuitbreak.GetDefaultCBConfig())
		}
	}
	for method, cfg := range configs {
		a.cbs.UpdateServiceCBConfig(method, cfg)
	}
	a.prev = make(map[string]struct{}, len(configs))
	for method := range configs {
		a.prev[method] = struct{}{}
	}
	return len(configs)
}

// withCircuitBreaker 订阅 Nacos circuit_break 配置并注入 circuitbreak.CBSuite。
//
// dataId 格式: {clientService}.{serverService}.circuit_break
// 配置格式（JSON），key 为 RPC 方法名：
//
//	{
//	  "echo": {"enable":true, "err_rate":0.3, "min_sample":100}
//	}
//
// 全量语义：配置中被移除的 method 恢复 kitex 默认熔断参数
// （enable=true, err_rate=0.5, min_sample=200）。
func withCircuitBreaker(clientService, serverService string, gov *GovernanceConfig, m *resilience.Metrics) ([]client.Option, func() error) {
	cbs := circuitbreak.NewCBSuite(methodCBKey)
	dataID := gov.clientDataID(clientService, serverService, "circuit_break")
	applier := &cbConfigApplier{cbs: cbs}

	if err := gov.subscribe(dataID, func(data string) {
		if n := applier.apply(data); n >= 0 {
			govLog.Info("circuit_break config updated", "dataId", dataID, "methods", n)
		}
	}); err != nil {
		govLog.Warn("subscribe circuit_break config failed", "dataId", dataID, "err", err)
	}

	opts := make([]client.Option, 0, 1+1)
	opts = append(opts, client.WithCircuitBreaker(cbs))
	opts = append(opts, cbMetricsOptions(serverService, m)...)
	return opts, startCBMetrics(cbs, serverService, m, cbPollInterval)
}
