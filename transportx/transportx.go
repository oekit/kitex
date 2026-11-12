// Package transportx 把 Kitex 上下文适配为 core/transport 抽象。
//
// Kitex 的 header 载体是 metainfo（bytedance/gopkg，kitex 标准元数据通道）。
// metainfo 的写 API 是不可变 ctx 链（WithValue/DelValue 返回新 ctx），
// 本包经 ctx 盒子指针（*context.Context）承载 Header 的原地写语义：
// Set/Add/Del 更新盒子，链内后续读取可见。
//
// metainfo 的两个原生限制由本包的叠加层（overlay）兜住，使 metaHeader
// 完整兑现 core/transport.Header 契约（#201/#202）：
//   - metainfo 是「键 → 单值」模型：同 key 写入是替换、空 value 写入被
//     丢弃（WithValue 对空串直接 no-op）——无法表达契约的「Add 追加多值」
//     与「Set 覆盖为单值（含空串）」。经本 Header 写入的键值同步落到叠加层
//     （ctx value 承载、copy-on-write），Header 读接口以叠加层为准；
//   - metainfo 分 persistent（全链路透传）/ transient（单跳）两桶，读 API
//     各只搜一个桶——契约的「这个键的全部值」必须两桶合并可见（kitexmeta
//     经 WithPersistentValue 透传的业务元信息落 persistent 桶，此前
//     transportx 只读 transient 桶 ⇒ 同 ctx 同 key 与 kitexmeta 给出相反
//     答案）。
//
// 语义边界：
//   - 写操作仅影响链内后续读取；RPC 线上透传需在发起调用前用
//     metainfo.WithPersistentValue 构造 ctx（快照已定，本包装无法回写线上）；
//   - 非空单值写入同时写透 metainfo transient 桶（保持既有 metainfo 直读方
//     的链内可见性）；空值与多值是 metainfo 无法承载的契约超额状态，仅存
//     叠加层；
//   - 服务端回写走 metainfo backward API（SetBackwardValue），不在本包；
//   - ReplyHeader 为只读空实现（无响应头通道）。
package transportx

import (
	"context"
	"slices"

	"github.com/bytedance/gopkg/cloud/metainfo"
	"github.com/oekit/core/transport"
)

// NewServerTransport 把服务端 ctx 适配为 Transporter。
// Kind=grpc；endpoint 为发现目标；operation 为方法名。
func NewServerTransport(ctx context.Context, endpoint, operation string) transport.Transporter {
	return &serverTransport{box: &ctx, endpoint: endpoint, operation: operation}
}

type serverTransport struct {
	box       *context.Context
	endpoint  string
	operation string
}

func (t *serverTransport) Kind() transport.Kind { return transport.KindGRPC }

func (t *serverTransport) Endpoint() string { return t.endpoint }

func (t *serverTransport) Operation() string { return t.operation }

func (t *serverTransport) RequestHeader() transport.Header {
	return &metaHeader{box: t.box}
}

func (t *serverTransport) ReplyHeader() transport.Header {
	// metainfo 无服务端响应头通道（回写走 backward API），只读空实现。
	return transport.NoopHeader{}
}

// headerStateKey 是叠加层在 ctx 里的存储键。叠加层放盒子 ctx（而非
// metaHeader 实例字段）：同一 Transporter 的多次 RequestHeader() 调用共享
// 同一盒子 ⇒ 经一个 header 实例的写入对后续取出的 header 实例可见——
// 中间件链 A 写、B 读的复用语义（core/chainmw）依赖这一点。
type headerStateKey struct{}

// metaHeader 适配 metainfo（经 ctx 盒子指针承载原地写语义；叠加层兜住
// metainfo 单值模型与双桶分桶这两个原生限制）。
type metaHeader struct {
	box *context.Context
}

// overlay 取当前叠加层快照（nil = 无经本 Header 写入的状态）。
func (m *metaHeader) overlay() map[string][]string {
	st, _ := (*m.box).Value(headerStateKey{}).(map[string][]string)
	return st
}

// writeOverlay 以 copy-on-write 更新叠加层并写回盒子。vals 为 nil 表示
// 删除该键。每次写克隆整表：盒子共享给任意多个 header 实例，原地改共享
// map 会让已取出的快照读到大括号内并发变异。
func (m *metaHeader) writeOverlay(key string, vals []string) {
	cur := m.overlay()
	next := make(map[string][]string, len(cur)+1)
	for k, v := range cur {
		next[k] = v
	}
	if vals == nil {
		delete(next, key)
	} else {
		next[key] = slices.Clone(vals)
	}
	*m.box = context.WithValue(*m.box, headerStateKey{}, next)
}

func (m *metaHeader) Get(key string) string {
	vs := m.Values(key)
	if len(vs) == 0 {
		return ""
	}
	return vs[0]
}

// Values 返回 key 的全部值（契约：叠加层优先；metainfo 回退时 transient
// 桶在前、persistent 桶在后——单跳值遮蔽全链路值，两桶同值去重）。
func (m *metaHeader) Values(key string) []string {
	if st := m.overlay(); st != nil {
		if vs, ok := st[key]; ok {
			return slices.Clone(vs)
		}
	}
	var out []string
	if v, ok := metainfo.GetValue(*m.box, key); ok {
		out = append(out, v)
	}
	if v, ok := metainfo.GetPersistentValue(*m.box, key); ok && !slices.Contains(out, v) {
		out = append(out, v)
	}
	return out
}

// Set 覆盖 key 的值为单值（契约含空串：Set(k, "") 后 Get 返回空串，旧值
// 不残留）。非空值同时写透 metainfo transient 桶；空值 metainfo 无法承载
// （WithValue 对空串 no-op），须显式 DelValue 清掉旧值防幽灵残留。
func (m *metaHeader) Set(key, value string) {
	m.writeOverlay(key, []string{value})
	if value != "" {
		*m.box = metainfo.WithValue(*m.box, key, value)
	} else {
		*m.box = metainfo.DelValue(*m.box, key)
	}
}

// Add 追加 key 的一个值（契约语义，非覆盖）。key 尚无叠加层记录时先以
// 当前解析态（含 metainfo 两桶）为初值再追加；追加值非空时写透 transient
// 桶保持 metainfo 直读者的可见性（metainfo 单值模型取最后一个追加值）。
func (m *metaHeader) Add(key, value string) {
	m.writeOverlay(key, append(m.Values(key), value))
	if value != "" {
		*m.box = metainfo.WithValue(*m.box, key, value)
	}
}

// Del 删除 key 的全部值：叠加层与 metainfo 两桶（transient + persistent）
// 一并清除。
func (m *metaHeader) Del(key string) {
	m.writeOverlay(key, nil)
	*m.box = metainfo.DelValue(*m.box, key)
	*m.box = metainfo.DelPersistentValue(*m.box, key)
}

// Keys 返回全部 key（叠加层 ∪ metainfo transient/stale 桶 ∪ persistent
// 桶，排序去重）。
func (m *metaHeader) Keys() []string {
	var keys []string
	if st := m.overlay(); st != nil {
		for k := range st {
			keys = append(keys, k)
		}
	}
	for k := range metainfo.GetAllValues(*m.box) {
		keys = append(keys, k)
	}
	for k := range metainfo.GetAllPersistentValues(*m.box) {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}

// ServerContext 返回注入服务端 Transporter 的 context。
func ServerContext(ctx context.Context, endpoint, operation string) context.Context {
	return transport.NewServerContext(ctx, NewServerTransport(ctx, endpoint, operation))
}
