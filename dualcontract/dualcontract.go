// Package dualcontract 提供 hz（HTTP 对外）/ kitex（RPC 对内）双传输契约的
// 防漂移原语（#123）。
//
// # 背景：双 IDL 人肉同步已实质漂移
//
// 同一业务能力在 hz 与 kitex 各维护一份 thrift IDL + 一份手写映射代码
// （octopus 实证）：uid（网关注入 header vs 显式 RPC 字段）、幂等键
// （HTTP header vs RPC 字段）、QuickSend HTTP 侧幂等能力整体缺失。
// 映射代码双写（toUintIDs/toUintIds 同形复制两处）。「双栈同 service 层」
// 是矩阵约定，但传输层契约此前没有对应的防漂移机制。
//
// 本包是 issue 建议 2（双 IDL 形状 diff 检查 + CI 门禁）的库侧原语：
// 服务在自己的契约测试里对「同一业务方法的 hz 生成请求 struct」与
// 「kitex 生成请求 struct」做结构 diff，字段集差异即 fail——漂移从
// review 纪律变成 CI 门禁。建议 1（单契约双传输，thrift 单源 + 生成器
// 桥接传输差异）是终态，本原语在其落地后仍可复用（对生成物做门禁）。
//
// 用法（服务仓的契约测试，CI 跑）：
//
//	type httpSendReq struct { ... }   // hz 生成物（idl/xxx_send.thrift）
//	type rpcSendReq struct { ... }    // kitex 生成物（idl/xxx_rpc.thrift）
//
//	func TestSendDualTransportContract(t *testing.T) {
//	    dualcontract.AssertSameShape(t, httpSendReq{}, rpcSendReq{},
//	        // 网关注入（X-Auth-User-ID → RPC 显式字段）是已声明的传输差异
//	        dualcontract.IgnoreFields("uid"),
//	    )
//	    // 幂等键形态对齐：RPC 带字段 ⇒ HTTP 必须带 header（QuickSend 式
//	    // 单侧缺失在此 fail）
//	    dualcontract.RequireIdempotencyKey(t, "Send", httpCarriesHeader, rpcHasField)
//	}
//
// 幂等键契约（#123 幂等键形态对齐）：HTTP 侧以 Idempotency-Key header 携带
// （常量 IdempotencyKeyHeader），RPC 侧以 thrift 字段 idempotency_key 携带
// （Go 侧 IdempotencyKey，常量 IdempotencyKeyField）；同一业务方法两侧
// 能力必须同时存在或同时不存在。
package dualcontract

import (
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// 幂等键双传输契约常量：HTTP header 名与 RPC thrift 字段名的绑定在此单源。
const (
	// IdempotencyKeyHeader 是 HTTP 侧幂等键 header 名（网关/客户端携带）。
	IdempotencyKeyHeader = "Idempotency-Key"
	// IdempotencyKeyField 是 RPC 侧幂等键 thrift 字段名（Go 字段
	// IdempotencyKey）。
	IdempotencyKeyField = "idempotency_key"
)

// FieldShape 是生成 struct 的单个字段在传输面上的形状。
type FieldShape struct {
	// Name 是归一化字段名：thrift tag（kitex 生成物）→ json tag（hz
	// 生成物）→ Go 字段名。命名风格差异（camelCase vs snake_case）不归一，
	// 双 IDL 应保持同名（漂移检测要的就是暴露不一致）。
	Name string
	// Type 是归一化类型（int64/string/[]int64/map[string]int64/自定义名；
	// 指针解引用后比较）。
	Type string
	// Required 表示 requiredness 是否 required（thrift tag "required"，或
	// json tag 手写 ",required" 注解）。
	Required bool
	// RequiredKnown 表示来源生成物是否为该字段**显式标注**了 requiredness。
	//
	// thrift tag（kitex/thriftgo 生成物）恒为 required/optional 二选一 ⇒
	// 恒 true；json tag 只有手写 ",required" 注解才表达 requiredness——
	// 真实 hz/protoc-gen-go 生成物的 json tag 只有 name/omitempty（#203：
	// proto3 无 required 关键字，thriftgo 也不往 json tag 写 requiredness），
	// 此时 false（Required=false 只代表「未表达」，不代表「可选」）。
	//
	// requiredness 比较只对**两侧都显式标注**的字段生效：单侧未表达是
	// 生成器能力差异（proto3 vs thrift），不是双 IDL 漂移，比较只会产生
	// 恒假阳性。
	RequiredKnown bool
}

// Shape 是 struct 的传输面形状（字段按声明序）。
type Shape struct {
	Fields []FieldShape
}

// ShapeOf 从生成 struct（或指向它的指针）提取传输面形状。
// 仅处理 struct；非 struct 输入返回空 Shape（Diff 会因此产生 missing 差异，
// 与「契约必须成对存在」的语义一致）。
func ShapeOf(v any) Shape {
	t := reflect.TypeOf(v)
	for t != nil && (t.Kind() == reflect.Pointer || t.Kind() == reflect.Interface) {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return Shape{}
	}
	var fields []FieldShape
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, required, known := tagNameAndRequired(f)
		fields = append(fields, FieldShape{
			Name:          name,
			Type:          normalizeType(f.Type),
			Required:      required,
			RequiredKnown: known,
		})
	}
	return Shape{Fields: fields}
}

// tagNameAndRequired 取字段传输名与 requiredness 及其表达性：
// thrift tag（"name,id,required|optional"，kitex/thriftgo 生成物恒二选一）
// 优先；其次 json tag——**真实 hz/protoc-gen-go 生成物的 json tag 只有
// name/omitempty，不标注 requiredness**（#203：proto3 无 required 关键字），
// 只有手写 ",required" 注解才表达；最后 Go 字段名（无 requiredness 表达）。
// 返回的 known 表示本字段是否显式表达了 requiredness。
func tagNameAndRequired(f reflect.StructField) (name string, required, known bool) {
	if tv, ok := f.Tag.Lookup("thrift"); ok {
		parts := strings.Split(tv, ",")
		name = strings.TrimSpace(parts[0])
		for _, p := range parts[1:] {
			switch strings.TrimSpace(p) {
			case "required":
				required, known = true, true
			case "optional":
				known = true
			}
		}
		if name != "" {
			return name, required, known
		}
	}
	if jv, ok := f.Tag.Lookup("json"); ok {
		parts := strings.Split(jv, ",")
		name = strings.TrimSpace(parts[0])
		for _, p := range parts[1:] {
			if strings.TrimSpace(p) == "required" {
				// 手写 ",required" 注解（契约测试自标注形态）是 json tag
				// 唯一的 requiredness 表达面；omitempty 是序列化行为，
				// 与 requiredness 无关，不作为表达信号。
				required, known = true, true
			}
		}
		if name != "" {
			return name, required, known
		}
	}
	return f.Name, false, false
}

// normalizeType 归一化类型字符串（指针解引用；内建类型用 thrift 语义名，
// 其余用类型名）。
func normalizeType(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		return "[]" + normalizeType(t.Elem())
	case reflect.Map:
		return "map[" + normalizeType(t.Key()) + "]" + normalizeType(t.Elem())
	case reflect.Bool:
		return "bool"
	case reflect.Int8:
		return "i8"
	case reflect.Int16:
		return "i16"
	case reflect.Int32:
		return "i32"
	case reflect.Int, reflect.Int64:
		return "i64"
	case reflect.Float32, reflect.Float64:
		return "double"
	case reflect.String:
		return "string"
	case reflect.Struct:
		return t.Name()
	default:
		return t.String()
	}
}

// DiffKind 是差异类别。
type DiffKind string

const (
	// DiffMissingHTTP：RPC 侧有、HTTP 侧缺（能力单侧缺失，QuickSend 式漂移）。
	DiffMissingHTTP DiffKind = "missing-on-http"
	// DiffMissingRPC：HTTP 侧有、RPC 侧缺。
	DiffMissingRPC DiffKind = "missing-on-rpc"
	// DiffTypeMismatch：同名字段类型不一致。
	DiffTypeMismatch DiffKind = "type-mismatch"
	// DiffRequirednessMismatch：requiredness 不一致（一侧 required 一侧可选，
	// 消费方校验行为会漂移）。
	DiffRequirednessMismatch DiffKind = "requiredness-mismatch"
)

// FieldDiff 是单字段差异。
type FieldDiff struct {
	Field string
	Kind  DiffKind
	// HTTPType/RPCType 是两侧归一化类型（缺失侧为空）。
	HTTPType, RPCType string
	// HTTPRequired/RPCRequired 是两侧 requiredness。
	HTTPRequired, RPCRequired bool
}

func (d FieldDiff) String() string {
	var b strings.Builder
	b.WriteString(d.Field)
	b.WriteString(": ")
	b.WriteString(string(d.Kind))
	if d.HTTPType != "" || d.RPCType != "" {
		b.WriteString(" (http=" + d.HTTPType + ", rpc=" + d.RPCType + ")")
	}
	if d.Kind == DiffRequirednessMismatch {
		b.WriteString(" (httpRequired=" + strconv.FormatBool(d.HTTPRequired) +
			", rpcRequired=" + strconv.FormatBool(d.RPCRequired) + ")")
	}
	return b.String()
}

// config 是 diff 选项。
type config struct {
	ignore             map[string]bool
	ignoreRequiredness bool
}

// Option 配置 DiffShapes/AssertSameShape。
type Option func(*config)

// IgnoreFields 豁免字段（已声明的单侧传输差异，如网关注入的 uid：
// HTTP 侧由网关注入 header、RPC 侧为显式字段——形状不同是**设计**而非漂移）。
func IgnoreFields(names ...string) Option {
	return func(c *config) {
		for _, n := range names {
			c.ignore[n] = true
		}
	}
}

// IgnoreRequiredness 跳过 requiredness 比较这一维度（与 IgnoreFields 同族）。
// 默认已经只对「两侧都显式标注 requiredness」的字段比较（#203：单侧未表达
// 是生成器能力差异——proto3 无 required、hz 生成物 json tag 只有 omitempty
// ——不比较，避免恒假阳性淹没门禁）；本 option 供两侧都显式标注、但
// requiredness 差异已被声明为设计取舍的契约整体关闭该维度。
func IgnoreRequiredness() Option {
	return func(c *config) {
		c.ignoreRequiredness = true
	}
}

// DiffShapes 比较同一业务方法的 HTTP（hz 生成物）与 RPC（kitex 生成物）
// 请求形状，返回全部差异（无差异返回 nil）。
func DiffShapes(httpReq, rpcReq any, opts ...Option) []FieldDiff {
	cfg := &config{ignore: map[string]bool{}}
	for _, o := range opts {
		o(cfg)
	}

	httpFields := indexByName(ShapeOf(httpReq))
	rpcFields := indexByName(ShapeOf(rpcReq))

	var diffs []FieldDiff
	seen := map[string]bool{}
	for name, hf := range httpFields {
		seen[name] = true
		if cfg.ignore[name] {
			continue
		}
		rf, ok := rpcFields[name]
		if !ok {
			diffs = append(diffs, FieldDiff{Field: name, Kind: DiffMissingRPC, HTTPType: hf.Type})
			continue
		}
		if hf.Type != rf.Type {
			diffs = append(diffs, FieldDiff{Field: name, Kind: DiffTypeMismatch,
				HTTPType: hf.Type, RPCType: rf.Type})
		}
		// requiredness 只在两侧都显式表达时比较（#203）：thrift tag 恒表达
		// required/optional，真实 hz/protoc-gen-go 生成物的 json tag 不表达
		// ——「RPC required / HTTP 未表达」是生成器能力差异而非漂移，比较
		// 会对每个 required 字段恒报假阳性，把 CI 门禁淹没成一片红。
		if !cfg.ignoreRequiredness && hf.RequiredKnown && rf.RequiredKnown &&
			hf.Required != rf.Required {
			diffs = append(diffs, FieldDiff{Field: name, Kind: DiffRequirednessMismatch,
				HTTPType: hf.Type, RPCType: rf.Type,
				HTTPRequired: hf.Required, RPCRequired: rf.Required})
		}
	}
	for name, rf := range rpcFields {
		if seen[name] || cfg.ignore[name] {
			continue
		}
		diffs = append(diffs, FieldDiff{Field: name, Kind: DiffMissingHTTP, RPCType: rf.Type})
	}
	sort.Slice(diffs, func(i, j int) bool { return diffList(diffs).Less(i, j) })
	return diffs
}

// diffs 是可排序差异列表（按字段名排序，保证 diff 输出确定性——CI 门禁
// 的错误信息必须稳定，map 遍历序不可用）。
type diffList []FieldDiff

func (d diffList) Len() int      { return len(d) }
func (d diffList) Swap(i, j int) { d[i], d[j] = d[j], d[i] }
func (d diffList) Less(i, j int) bool {
	if d[i].Field != d[j].Field {
		return d[i].Field < d[j].Field
	}
	return d[i].Kind < d[j].Kind
}

func indexByName(s Shape) map[string]FieldShape {
	m := make(map[string]FieldShape, len(s.Fields))
	for _, f := range s.Fields {
		m[f.Name] = f
	}
	return m
}

// AssertSameShape 是 CI 防漂移门禁：差异非空即 fail（列出全部差异），
// 同时返回差异供调用方附加处理。
func AssertSameShape(t testing.TB, httpReq, rpcReq any, opts ...Option) []FieldDiff {
	t.Helper()
	diffs := DiffShapes(httpReq, rpcReq, opts...)
	for _, d := range diffs {
		t.Errorf("双传输契约漂移 %s.%s（hz 生成物 vs kitex 生成物，#123）", d.Field, d.Kind)
	}
	return diffs
}

// RequireIdempotencyKey 是幂等键形态对齐断言（#123 实证漂移：RPC 侧有
// idempotency_key 字段、HTTP 侧却无 Idempotency-Key header 携带——QuickSend
// 式单侧缺失）。契约：同一业务方法两侧幂等能力必须同时存在或同时不存在。
//
// httpCarriesHeader：HTTP 侧是否以 IdempotencyKeyHeader 携带；
// rpcHasField：RPC 请求是否含 IdempotencyKeyField 字段。
func RequireIdempotencyKey(t testing.TB, method string, httpCarriesHeader, rpcHasField bool) {
	t.Helper()
	switch {
	case rpcHasField && !httpCarriesHeader:
		t.Errorf("双传输契约漂移 %s：RPC 侧有幂等键字段 %s，HTTP 侧未以 header %s 携带（#123 幂等键形态对齐）",
			method, IdempotencyKeyField, IdempotencyKeyHeader)
	case httpCarriesHeader && !rpcHasField:
		t.Errorf("双传输契约漂移 %s：HTTP 侧以 header %s 携带幂等键，RPC 侧无字段 %s（#123 幂等键形态对齐）",
			method, IdempotencyKeyHeader, IdempotencyKeyField)
	}
}
