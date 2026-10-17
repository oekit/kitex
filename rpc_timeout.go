package kitex

import (
	"encoding/json"

	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/pkg/rpctimeout"
)

// withRPCTimeout 订阅 Nacos rpc_timeout 配置并注入 rpctimeout.Container。
//
// dataId 格式: {clientService}.{serverService}.rpc_timeout
// 配置格式（JSON）:
//
//	{
//	  "*":    {"conn_timeout_ms":100, "rpc_timeout_ms":3000},
//	  "echo": {"conn_timeout_ms":50,  "rpc_timeout_ms":1000}
//	}
func withRPCTimeout(clientService, serverService string, gov *GovernanceConfig) []client.Option {
	tc := rpctimeout.NewContainer()
	dataID := gov.clientDataID(clientService, serverService, "rpc_timeout")

	if err := gov.subscribe(dataID, func(data string) {
		raw := parseRPCTimeoutConfig(data, dataID)
		if raw == nil {
			return
		}
		tc.NotifyPolicyChange(raw)
		govLog.Info("rpc_timeout config updated", "dataId", dataID, "methods", len(raw))
	}); err != nil {
		govLog.Warn("subscribe rpc_timeout config failed", "dataId", dataID, "err", err)
	}

	return []client.Option{client.WithTimeoutProvider(tc)}
}

// parseRPCTimeoutConfig 解析 rpc_timeout 配置。
// 返回 nil 表示解析失败（保留上一轮已应用配置）。
// null 值过滤：json.Unmarshal 对 {"m":null} 产出 nil 指针且不报错，而 kitex
// rpctimeout 对每项 DeepCopy 无 nil 保护——裸解引用 panic，且 nacos OnChange
// 走无 recover 的裸 goroutine，直接打死进程。null 按"删除该方法"处理
// （回退默认超时）。
func parseRPCTimeoutConfig(data, dataID string) map[string]*rpctimeout.RPCTimeout {
	var raw map[string]*rpctimeout.RPCTimeout
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		govLog.Error("parse rpc_timeout config failed", "dataId", dataID, "err", err)
		return nil
	}
	for k, v := range raw {
		if v == nil {
			delete(raw, k)
			govLog.Warn("rpc_timeout config null value treated as removal", "dataId", dataID, "method", k)
		}
	}
	return raw
}
