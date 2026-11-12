package kitex

import (
	"strings"
	"sync"
)

// fullSyncApplier 是「JSON 整表 → 容器全量同步」类治理配置的泛型骨架，
// 收敛 circuit_break / retry / rpc_timeout 三份同构 copy-paste（#185）：
//
//	parse 解析 → normalize 逐项过滤（可选）→ diff 删除（remove）→
//	全量应用（apply / applyTable）→ 重建 prev
//
// diff/prev/mutex 收编在本类型，各策略文件只剩钩子与订阅约 20 行。
// 逐方法型容器（CBSuite / retry.Container）提供 apply+remove；
// 整表替换型容器（rpctimeout.Container 的 NotifyPolicyChange 无逐方法 API）
// 提供 applyTable，此时 apply/remove 留空，diff 仅用于维护 prev 语义。
type fullSyncApplier[T any] struct {
	mu   sync.Mutex // OnChange 异步触发，串行化对 prev 的访问
	prev map[string]struct{}

	// parse 把配置原文解析为 method→配置表；识别 JSON null 需用指针元素类型。
	parse func(data string) (map[string]T, error)
	// normalize 可选钩子：返回 false 表示该项按删除处理（null 过滤即此钩子）。
	normalize func(method string, val T) bool
	// apply 应用单个 method 的新配置（逐方法型容器）。
	apply func(method string, val T)
	// remove 配置中被移除的 method 回退默认行为（逐方法型容器）。
	remove func(method string)
	// applyTable 可选：一轮配置整表一次性提交（整表替换型容器），与 apply 互斥。
	applyTable func(configs map[string]T)
	// onParseError 解析失败时的日志钩子（可选）。
	onParseError func(err error)
}

// applyConfig 解析并应用一轮配置，返回配置中的 method 数；解析失败返回 -1，
// 并保留上一轮已应用的状态。
//
// Nacos 删除语义（#197）：空 content（配置在 Nacos 上被删除时 SDK 投递的
// 形态）不是坏 JSON，而是「整表删除」——按空表走全量同步路径，上一轮全部
// 方法 remove / 整表替换回默认，而不是保留旧策略。
func (a *fullSyncApplier[T]) applyConfig(data string) int {
	if strings.TrimSpace(data) == "" {
		data = "{}"
	}
	configs, err := a.parse(data)
	if err != nil {
		if a.onParseError != nil {
			a.onParseError(err)
		}
		return -1
	}
	if a.normalize != nil {
		for method, val := range configs {
			if !a.normalize(method, val) {
				delete(configs, method)
			}
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for method := range a.prev {
		if _, ok := configs[method]; !ok && a.remove != nil {
			a.remove(method)
		}
	}
	for method, val := range configs {
		if a.apply != nil {
			a.apply(method, val)
		}
	}
	if a.applyTable != nil {
		a.applyTable(configs)
	}
	a.prev = make(map[string]struct{}, len(configs))
	for method := range configs {
		a.prev[method] = struct{}{}
	}
	return len(configs)
}
