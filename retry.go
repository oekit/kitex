package kitex

import (
	"encoding/json"

	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/pkg/retry"
	"github.com/oekit/logx"
)

// newRetryPolicyApplier 构造 retry 配置的全量同步 applier：配置中被移除的
// method 调 DeletePolicy 删除（回退为不重试），与 rpc_timeout 的整表替换
// 保持一致的全量同步语义；diff/prev/mutex 骨架收编在 fullSyncApplier（#185）。
//
// null 值过滤（#210）：与兄弟实现（rpc_timeout/circuit_break）同款——用指针
// 元素类型解析以区分 null 与零值对象。值类型下 {"echo":null} 被解成零值
// Policy（Enable=false）并被当成真策略下发：零值策略不重试，遮蔽 "*" 通配
// 策略使该方法重试持续失效，且 JSON 语法上根本无法区分「显式禁用」与「null
// 删除」。null 按"删除该方法"处理（回落 "*" 通配或容器默认）。
func newRetryPolicyApplier(rc *retry.Container) *fullSyncApplier[*retry.Policy] {
	return &fullSyncApplier[*retry.Policy]{
		parse: func(data string) (map[string]*retry.Policy, error) {
			var policies map[string]*retry.Policy
			if err := json.Unmarshal([]byte(data), &policies); err != nil {
				return nil, err
			}
			return policies, nil
		},
		normalize: func(method string, p *retry.Policy) bool {
			if p == nil {
				govLog.Warn("retry config null value treated as removal", "method", method)
				return false
			}
			return true
		},
		apply: func(method string, p *retry.Policy) {
			rc.NotifyPolicyChange(method, *p)
		},
		remove: func(method string) {
			rc.DeletePolicy(method)
		},
		onParseError: func(err error) {
			govLog.Error("parse retry config failed", logx.ErrKey, err)
		},
	}
}

// withRetry 订阅 Nacos retry 配置并注入 retry.Container。
//
// dataId 格式: {clientService}.{serverService}.retry
// 配置格式（JSON），key 为 RPC 方法名（"*" 为通配默认策略，容器原生支持）：
//
//	{
//	  "*":    {"enable":true, "type":0, "failure_policy":{...}},
//	  "echo": {"enable":true, "type":1, "backup_policy":{...}}
//	}
//
// 全量语义：配置中被移除的 method 回退为不重试。
//
// ⚠️ 使用本 Suite 时不要再通过代码级 client.WithFailureRetry/WithBackupRequest/
// WithMixedRetry 配置重试：那些配置会让容器进入 hasCodeCfg 状态，远端热更
// 将被整体忽略（kitex retry.Container 语义）。
//
// 第二个返回值是容器资源释放函数（client.Close() 不管理用户注入的容器），
// 由 [ClientSuite.Close] 在全部 client 关闭后调用。
func withRetry(clientService, serverService string, gov *GovernanceConfig) ([]client.Option, func() error) {
	// 官方推荐初始化器：内置重试比例熔断，故障风暴时按错误率停止重试
	rc := retry.NewRetryContainerWithPercentageLimit()
	dataID := gov.clientDataID(clientService, serverService, "retry")
	applier := newRetryPolicyApplier(rc)

	if err := gov.subscribe(dataID, func(data string) {
		if n := applier.applyConfig(data); n >= 0 {
			govLog.Info("retry config updated", "dataId", dataID, "methods", n)
		}
	}); err != nil {
		govLog.Warn("subscribe retry config failed", "dataId", dataID, logx.ErrKey, err)
	}

	return []client.Option{client.WithRetryContainer(rc)}, rc.Close
}
