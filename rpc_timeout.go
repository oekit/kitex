package kitex

import (
	"encoding/json"

	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/pkg/rpctimeout"
	"github.com/oekit/logx"
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
//
// rpctimeout.Container 的 NotifyPolicyChange 是整表替换（无逐方法 API），
// 走 fullSyncApplier 的 applyTable 模式（#185）。
func withRPCTimeout(clientService, serverService string, gov *GovernanceConfig) []client.Option {
	tc := rpctimeout.NewContainer()
	dataID := gov.clientDataID(clientService, serverService, "rpc_timeout")

	applier := newRPCTimeoutApplier(tc, dataID)
	if err := gov.subscribe(dataID, func(data string) {
		if n := applier.applyConfig(data); n >= 0 {
			govLog.Info("rpc_timeout config updated", "dataId", dataID, "methods", n)
		}
	}); err != nil {
		govLog.Warn("subscribe rpc_timeout config failed", "dataId", dataID, logx.ErrKey, err)
	}

	return []client.Option{client.WithTimeoutProvider(tc)}
}

// newRPCTimeoutApplier 构造 rpc_timeout 配置的全量同步 applier。
// null 值过滤：json.Unmarshal 对 {"m":null} 产出 nil 指针且不报错，而 kitex
// rpctimeout 对每项 DeepCopy 无 nil 保护——裸解引用 panic，且 nacos OnChange
// 走无 recover 的裸 goroutine，直接打死进程。null 按"删除该方法"处理
// （回退默认超时）。
func newRPCTimeoutApplier(tc *rpctimeout.Container, dataID string) *fullSyncApplier[*rpctimeout.RPCTimeout] {
	return &fullSyncApplier[*rpctimeout.RPCTimeout]{
		parse: func(data string) (map[string]*rpctimeout.RPCTimeout, error) {
			var raw map[string]*rpctimeout.RPCTimeout
			if err := json.Unmarshal([]byte(data), &raw); err != nil {
				return nil, err
			}
			return raw, nil
		},
		normalize: func(method string, v *rpctimeout.RPCTimeout) bool {
			if v == nil {
				govLog.Warn("rpc_timeout config null value treated as removal", "dataId", dataID, "method", method)
				return false
			}
			return true
		},
		applyTable: func(configs map[string]*rpctimeout.RPCTimeout) {
			tc.NotifyPolicyChange(configs)
		},
		onParseError: func(err error) {
			govLog.Error("parse rpc_timeout config failed", "dataId", dataID, logx.ErrKey, err)
		},
	}
}
