package dualcontract

// #123 防漂移测试：以 octopus 双传输漂移实证为场景（模拟 hz/kitex 生成物
// 的 tag 形态），钉住 diff 门禁抓漂移、豁免声明传输差异、幂等键形态对齐。

import (
	"fmt"
	"strings"
	"testing"
)

// httpSendReq 模拟 hz 生成物（octopus_send.thrift SendReq）叠加手写
// requiredness 注解（json tag ",required"）——真实 hz 生成物的 json tag
// 只有 name/omitempty，不标注 requiredness（见 #203 与真实生成物回归）。
type httpSendReq struct {
	Title          string   `json:"title,required"`           //nolint:staticcheck // SA5008: 手写 requiredness 注解形态（真实生成物无此标注，#203）
	UID            int64    `json:"uid"`                      // 网关注入（X-Auth-User-ID），单侧传输差异
	IdempotencyKey string   `json:"idempotency_key,required"` //nolint:staticcheck // SA5008: 手写 requiredness 注解形态（真实生成物无此标注，#203）
	Channels       []string `json:"channels"`
}

// httpSendReqNoIdem 是漂移形态：HTTP 侧缺幂等键（QuickSend 实证）。
type httpSendReqNoIdem struct {
	Title string `json:"title,required"` //nolint:staticcheck // SA5008: 手写 requiredness 注解形态（真实生成物无此标注，#203）
	UID   int64  `json:"uid"`
}

// rpcSendReq 模拟 kitex 生成物（octopus_rpc.thrift SendNotice 请求）：
// thrift + frugal tag；thrift 生成物字段常用指针可选。
type rpcSendReq struct {
	Title          *string  `thrift:"title,1,required" frugal:"1,required,string" json:"title"`
	UID            int64    `thrift:"uid,2,required" frugal:"2,required,i64" json:"uid"`
	IdempotencyKey *string  `thrift:"idempotency_key,3,required" frugal:"3,required,string" json:"idempotency_key"`
	Channels       []string `thrift:"channels,4,optional" frugal:"4,optional,list<string>" json:"channels"`
}

// TestDiffShapes_NoDrift 对齐形态：无差异（类型经指针解引用后一致）。
func TestDiffShapes_NoDrift(t *testing.T) {
	diffs := DiffShapes(httpSendReq{}, rpcSendReq{}, IgnoreFields("uid"))
	if len(diffs) != 0 {
		for _, d := range diffs {
			t.Errorf("对齐形态不应有差异：%s", d)
		}
	}
}

// TestDiffShapes_DetectsMissingIdempotencyKey QuickSend 式漂移：RPC 侧有
// idempotency_key、HTTP 侧缺 ⇒ 必须 diff 出 missing-on-http。
func TestDiffShapes_DetectsMissingIdempotencyKey(t *testing.T) {
	diffs := DiffShapes(httpSendReqNoIdem{}, rpcSendReq{}, IgnoreFields("uid"))
	found := false
	for _, d := range diffs {
		if d.Field == "idempotency_key" && d.Kind == DiffMissingHTTP {
			found = true
		}
	}
	if !found {
		t.Fatalf("RPC 侧幂等键字段缺失于 HTTP 侧未被检出，diffs = %v", diffs)
	}
}

// TestDiffShapes_TypeMismatch 同名字段类型漂移（i64 vs string）。
func TestDiffShapes_TypeMismatch(t *testing.T) {
	type badHTTPReq struct {
		Title string `json:"title,required"` //nolint:staticcheck // SA5008: 手写 requiredness 注解形态（真实生成物无此标注，#203）
		UID   int64  `json:"uid"`
	}
	type badRPCReq struct {
		Title *int64 `thrift:"title,1,required" frugal:"1,required,i64"`
		UID   int64  `thrift:"uid,2,required" frugal:"2,required,i64"`
	}
	diffs := DiffShapes(badHTTPReq{}, badRPCReq{}, IgnoreFields("uid"))
	found := false
	for _, d := range diffs {
		if d.Field == "title" && d.Kind == DiffTypeMismatch {
			found = true
		}
	}
	if !found {
		t.Fatalf("title 类型漂移（string vs i64）未被检出，diffs = %v", diffs)
	}
}

// TestDiffShapes_RequirednessMismatch requiredness 漂移（一侧 required 一侧
// optional）：消费方校验行为会漂移，必须检出。
func TestDiffShapes_RequirednessMismatch(t *testing.T) {
	type httpReq struct {
		A string `json:"a,required"` //nolint:staticcheck // SA5008: 手写 requiredness 注解形态（真实生成物无此标注，#203）
	}
	type rpcReq struct {
		A *string `thrift:"a,1,optional" frugal:"1,optional,string"`
	}
	diffs := DiffShapes(httpReq{}, rpcReq{})
	if len(diffs) != 1 || diffs[0].Kind != DiffRequirednessMismatch {
		t.Fatalf("requiredness 漂移未被检出，diffs = %v", diffs)
	}
}

// TestDiffShapes_MissingRPC 反向漂移：HTTP 有、RPC 缺。
func TestDiffShapes_MissingRPC(t *testing.T) {
	type httpReq struct {
		Extra string `json:"extra"`
	}
	type rpcReq struct{}
	diffs := DiffShapes(httpReq{}, rpcReq{})
	if len(diffs) != 1 || diffs[0].Field != "extra" || diffs[0].Kind != DiffMissingRPC {
		t.Fatalf("HTTP 单侧字段未被检出，diffs = %v", diffs)
	}
}

// TestAssertSameShape_FailsOnDrift 门禁语义：漂移时 t.Errorf 列出差异。
func TestAssertSameShape_FailsOnDrift(t *testing.T) {
	t.Run("对齐通过", func(t *testing.T) {
		AssertSameShape(t, httpSendReq{}, rpcSendReq{}, IgnoreFields("uid"))
	})
	t.Run("漂移失败", func(t *testing.T) {
		ft := &fakeTB{}
		AssertSameShape(ft, httpSendReqNoIdem{}, rpcSendReq{}, IgnoreFields("uid"))
		if len(ft.errors) == 0 {
			t.Fatal("漂移形态必须 fail")
		}
		found := false
		for _, e := range ft.errors {
			if strings.Contains(e, "idempotency_key") {
				found = true
			}
		}
		if !found {
			t.Fatalf("错误信息应指明漂移字段 idempotency_key：%v", ft.errors)
		}
	})
}

// TestRequireIdempotencyKey 幂等键形态对齐断言的三态。
func TestRequireIdempotencyKey(t *testing.T) {
	t.Run("双侧都有 通过", func(t *testing.T) {
		RequireIdempotencyKey(t, "Send", true, true)
	})
	t.Run("双侧都无 通过", func(t *testing.T) {
		RequireIdempotencyKey(t, "List", false, false)
	})
	t.Run("RPC 有 HTTP 无（QuickSend 实证）必须失败", func(t *testing.T) {
		ft := &fakeTB{}
		RequireIdempotencyKey(ft, "QuickSend", false, true)
		if len(ft.errors) != 1 || !strings.Contains(ft.errors[0], IdempotencyKeyHeader) {
			t.Fatalf("单侧幂等能力缺失未按契约失败：%v", ft.errors)
		}
	})
	t.Run("HTTP 有 RPC 无 必须失败", func(t *testing.T) {
		ft := &fakeTB{}
		RequireIdempotencyKey(ft, "Send", true, false)
		if len(ft.errors) != 1 {
			t.Fatalf("反向单侧缺失未失败：%v", ft.errors)
		}
	})
}

// TestShapeOf_TagPrecedence 命名优先级：thrift tag 优先于 json tag。
func TestShapeOf_TagPrecedency(t *testing.T) {
	type both struct {
		A string `json:"a_json" thrift:"a_thrift,1,required"`
	}
	s := ShapeOf(both{})
	if len(s.Fields) != 1 || s.Fields[0].Name != "a_thrift" || !s.Fields[0].Required {
		t.Fatalf("thrift tag 应优先：%+v", s.Fields)
	}
}

// TestShapeOf_PointerAndNested 指针解引用与容器类型归一化。
func TestShapeOf_PointerAndNested(t *testing.T) {
	type inner struct{ X int64 }
	type outer struct {
		P  *inner           `thrift:"p,1,optional"`
		M  map[string]int64 `thrift:"m,2,optional"`
		SS [][]string       `thrift:"ss,3,optional"`
		I  int              `thrift:"i,4,optional"`
	}
	s := ShapeOf(outer{})
	want := map[string]string{"p": "inner", "m": "map[string]i64", "ss": "[][]string", "i": "i64"}
	if len(s.Fields) != len(want) {
		t.Fatalf("字段数 = %d, want %d", len(s.Fields), len(want))
	}
	for _, f := range s.Fields {
		if want[f.Name] != f.Type {
			t.Errorf("field %s type = %s, want %s", f.Name, f.Type, want[f.Name])
		}
	}
}

// fakeTB 捕获 Errorf（不中断），用于断言门禁失败形态。
type fakeTB struct {
	testing.TB
	errors []string
}

func (f *fakeTB) Helper() {}
func (f *fakeTB) Errorf(format string, args ...any) {
	f.errors = append(f.errors, fmt.Sprintf(format, args...))
}

// httpRealGenReq 复刻**真实生成物**形态（#203）：hz/protoc-gen-go 的 json
// tag 只有 name/omitempty，proto3 无 required 关键字 ⇒ HTTP 侧不表达
// requiredness；RPC 侧 thriftgo 恒标注 required/optional。
type httpRealGenReq struct {
	Name    string `protobuf:"bytes,1,opt,name=name,proto3" form:"name" json:"name,omitempty"`
	Version string `protobuf:"bytes,2,opt,name=version,proto3" form:"version" json:"version,omitempty"`
	UID     int64  `protobuf:"bytes,3,opt,name=uid,proto3" form:"uid" json:"uid,omitempty"`
}

type rpcRealGenReq struct {
	Name    *string `thrift:"name,1,required" frugal:"1,required,string" json:"name"`
	Version *string `thrift:"version,2,optional" frugal:"2,optional,string" json:"version"`
	UID     int64   `thrift:"uid,3,required" frugal:"3,required,i64" json:"uid"`
}

// TestDiffShapes_RealGeneratedArtifactsNoFalsePositive 回归（#203）：真实
// hz/protoc-gen-go 生成物（json tag 只有 omitempty、无 requiredness 标注）
// 对 kitex required 生成物，requiredness 比较必须跳过——「RPC required /
// HTTP 未表达」是生成器能力差异而非双 IDL 漂移，否则每个 required 字段恒报
// requiredness-mismatch，门禁被假阳性淹没。
func TestDiffShapes_RealGeneratedArtifactsNoFalsePositive(t *testing.T) {
	diffs := DiffShapes(httpRealGenReq{}, rpcRealGenReq{})
	if len(diffs) != 0 {
		for _, d := range diffs {
			t.Errorf("真实生成物形态不应产生差异：%s", d)
		}
	}
}

// TestIgnoreRequiredness_Option 钉住豁免 option：两侧都显式标注 requiredness
// 且不一致时，默认仍报（真实注解的差异是可判定的）；IgnoreRequiredness()
// 整维度关闭，供已声明为设计取舍的契约使用。
func TestIgnoreRequiredness_Option(t *testing.T) {
	type httpAnnotated struct {
		A string `json:"a,required"` //nolint:staticcheck // SA5008: 手写 requiredness 注解形态（真实生成物无此标注，#203）
	}
	type rpcOptional struct {
		A *string `thrift:"a,1,optional" frugal:"1,optional,string"`
	}

	if diffs := DiffShapes(httpAnnotated{}, rpcOptional{}); len(diffs) != 1 ||
		diffs[0].Kind != DiffRequirednessMismatch {
		t.Fatalf("两侧都标注时 requiredness 差异必须检出，diffs = %v", diffs)
	}
	if diffs := DiffShapes(httpAnnotated{}, rpcOptional{}, IgnoreRequiredness()); len(diffs) != 0 {
		t.Errorf("IgnoreRequiredness 必须跳过整个 requiredness 维度，diffs = %v", diffs)
	}
}

// TestShapeOf_RequiredKnown 三态表达性：thrift tag 恒表达（required/optional
// 二选一 ⇒ known=true）；json tag 仅手写 ",required" 才 known；Go 字段名
// 兜底不表达。known=false 时 Required=false 只代表「未表达」。
func TestShapeOf_RequiredKnown(t *testing.T) {
	type mixed struct {
		ThriftReq  string `thrift:"tr,1,required"`
		ThriftOpt  string `thrift:"to,2,optional"`
		JSONReq    string `json:"jr,required"` //nolint:staticcheck // SA5008: 手写 requiredness 注解形态（真实生成物无此标注，#203）
		JSONPlain  string `json:"jp,omitempty"`
		GoFallback string
	}
	s := ShapeOf(mixed{})
	got := make(map[string]FieldShape, len(s.Fields))
	for _, f := range s.Fields {
		got[f.Name] = f
	}
	want := map[string]struct {
		required bool
		known    bool
	}{
		"tr":         {true, true},
		"to":         {false, true},
		"jr":         {true, true},
		"jp":         {false, false}, // omitempty 不是 requiredness 表达
		"GoFallback": {false, false},
	}
	for name, w := range want {
		f, ok := got[name]
		if !ok {
			t.Fatalf("field %q missing from shape", name)
		}
		if f.Required != w.required || f.RequiredKnown != w.known {
			t.Errorf("field %s: required=%v known=%v, want required=%v known=%v",
				name, f.Required, f.RequiredKnown, w.required, w.known)
		}
	}
}
