// Package transportx 把 Kitex 上下文适配为 core/transport 抽象。
//
// Kitex 的 header 载体是 metainfo（bytedance/gopkg，kitex 标准元数据通道）。
// metainfo 的写 API 是不可变 ctx 链（WithValue/DelValue 返回新 ctx），
// 本包经 ctx 盒子指针（*context.Context）承载 Header 的原地写语义：
// Set/Add/Del 更新盒子，链内后续读取可见。
//
// 语义边界：
//   - 写操作仅影响链内后续读取；RPC 线上透传需在发起调用前用
//     metainfo.WithPersistentValue 构造 ctx（快照已定，本包装无法回写线上）；
//   - 服务端回写走 metainfo backward API（SetBackwardValue），不在本包；
//   - ReplyHeader 为只读空实现（无响应头通道）。
package transportx

import (
	"context"
	"sort"

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

// metaHeader 适配 metainfo（经 ctx 盒子指针承载原地写语义）。
type metaHeader struct {
	box *context.Context
}

func (m *metaHeader) Get(key string) string {
	v, _ := metainfo.GetValue(*m.box, key)
	return v
}

func (m *metaHeader) Values(key string) []string {
	if v := m.Get(key); v != "" {
		return []string{v}
	}
	return nil
}

func (m *metaHeader) Set(key, value string) {
	*m.box = metainfo.WithValue(*m.box, key, value)
}

func (m *metaHeader) Add(key, value string) { m.Set(key, value) }

func (m *metaHeader) Del(key string) {
	*m.box = metainfo.DelValue(*m.box, key)
}

func (m *metaHeader) Keys() []string {
	var keys []string
	metainfo.RangeValues(*m.box, func(k, _ string) bool {
		keys = append(keys, k)
		return true
	})
	sort.Strings(keys)
	return keys
}

// ServerContext 返回注入服务端 Transporter 的 context。
func ServerContext(ctx context.Context, endpoint, operation string) context.Context {
	return transport.NewServerContext(ctx, NewServerTransport(ctx, endpoint, operation))
}
