package transportx

// transportx 契约测试（#192 专项 + #201/#202 回归）。
//
// 钉住四组最易漂移的契约：
//  1. ctx 盒子指针原地写语义（#192）：Set/Add/Del 经 *context.Context 盒子
//     原地生效——同一 Transporter 后续取出的 header 实例可见，写透 metainfo
//     的非空值对 metainfo 直读者可见；
//  2. core/transport.Header 写语义（#201）：Add 真追加、Set 覆盖为单值
//     （含空串）、Del 删全部值——以 core 的 MapHeader 参考实现为对照；
//  3. 双桶读合并（#202）：persistent 桶（kitexmeta 透传通道）对 header
//     通道可见，同 ctx 同 key 与 kitexmeta 口径一致；
//  4. ServerContext 链内传递（#192）：FromServerContext 取出的 Transporter
//     与 ServerContext 注入的是同一条盒子链。

import (
	"context"
	"slices"
	"testing"

	"github.com/bytedance/gopkg/cloud/metainfo"
	"github.com/oekit/core/transport"
)

// newTestHeader 构造盒子 + header；返回的 ctx 变量即盒子（与生产
// NewServerTransport 同型——调用方持有 ctx 变量，header 经指针原地写）。
func newTestHeader() (box *context.Context, h transport.Header) {
	ctx := context.Background()
	return &ctx, (&serverTransport{box: &ctx}).RequestHeader()
}

// TestMetaHeader_SetGetAndKeys 钉住 Set→Get 与 Set→Keys 排序契约（#192）。
func TestMetaHeader_SetGetAndKeys(t *testing.T) {
	box, h := newTestHeader()

	h.Set("b-key", "v2")
	h.Set("a-key", "v1")
	h.Set("c-key", "v3")

	if got := h.Get("a-key"); got != "v1" {
		t.Errorf("Get after Set = %q, want v1", got)
	}
	keys := h.Keys()
	if !slices.Equal(keys, []string{"a-key", "b-key", "c-key"}) {
		t.Errorf("Keys must be sorted and deterministic, got %v", keys)
	}

	// 盒子原地写：写后从同一盒子重新取出的 header 实例可见（中间件链 A 写
	// B 读的复用语义）
	ctx := *box
	h2 := (&serverTransport{box: &ctx}).RequestHeader()
	if got := h2.Get("b-key"); got != "v2" {
		t.Errorf("fresh header from same box must see prior Set, got %q", got)
	}
}

// TestMetaHeader_SetOverwritesContract Set 覆盖为单值（对照 core MapHeader）。
func TestMetaHeader_SetOverwritesContract(t *testing.T) {
	box, h := newTestHeader()

	h.Set("k", "old")
	h.Set("k", "new")
	if got := h.Get("k"); got != "new" {
		t.Errorf("Set must overwrite: Get = %q, want new", got)
	}
	if vs := h.Values("k"); !slices.Equal(vs, []string{"new"}) {
		t.Errorf("Set must yield exactly one value, got %v", vs)
	}
	_ = box
}

// TestMetaHeader_SetEmptyValueOverwrites 回归（#201）：Set(k, "") 必须覆盖
// 为单值空串，旧值不得残留——「清掉一个头」（如脱敏 Authorization）在此前
// 的实现里是 no-op，旧凭据原样留到链内后续读取。
func TestMetaHeader_SetEmptyValueOverwrites(t *testing.T) {
	box, h := newTestHeader()

	h.Set("Authorization", "Bearer secret")
	h.Set("Authorization", "")

	if got := h.Get("Authorization"); got != "" {
		t.Errorf(`Set(k, "") must overwrite to empty: Get = %q, want ""`, got)
	}
	if vs := h.Values("Authorization"); !slices.Equal(vs, []string{""}) {
		t.Errorf(`Set(k, "") must hold single empty value, got %v`, vs)
	}
	// metainfo 侧不得留幽灵值：直读者也不可见
	if _, ok := metainfo.GetValue(*box, "Authorization"); ok {
		t.Error("metainfo must not retain old value after Set(k, \"\")")
	}

	// 对照：core MapHeader 参考实现同操作（契约单源口径）
	ref := transport.MapHeader{}
	ref.Set("Authorization", "Bearer secret")
	ref.Set("Authorization", "")
	if ref.Get("Authorization") != "" || !slices.Equal(ref.Values("Authorization"), []string{""}) {
		t.Fatal("precondition: core MapHeader contract drifted")
	}
}

// TestMetaHeader_AddAppends 回归（#201）：Add 必须真追加（多值头 Vary /
// Set-Cookie / Warning 的表达面），而非转调 Set 静默顶掉先写的值。
func TestMetaHeader_AddAppends(t *testing.T) {
	box, h := newTestHeader()

	h.Add("vary", "Accept-Encoding")
	h.Add("vary", "Origin")

	if got := h.Get("vary"); got != "Accept-Encoding" {
		t.Errorf("Get must return the first value after Add: got %q", got)
	}
	if vs := h.Values("vary"); !slices.Equal(vs, []string{"Accept-Encoding", "Origin"}) {
		t.Errorf("Add must append, Values = %v, want [Accept-Encoding Origin]", vs)
	}

	// 对照：core MapHeader 同操作
	ref := transport.MapHeader{}
	ref.Add("vary", "Accept-Encoding")
	ref.Add("vary", "Origin")
	if got := h.Values("vary"); !slices.Equal(got, ref.Values("vary")) {
		t.Errorf("metaHeader Values = %v, core MapHeader = %v", got, ref.Values("vary"))
	}
	_ = box
}

// TestMetaHeader_AddAfterMetainfoState 叠加层与既有 metainfo 状态的衔接：
// key 已在 metainfo（如入站值）时 Add 以其当前解析态为初值再追加。
func TestMetaHeader_AddAfterMetainfoState(t *testing.T) {
	ctx := context.Background()
	ctx = metainfo.WithValue(ctx, "k", "wire-v")
	ctx = metainfo.WithPersistentValue(ctx, "k2", "persist-v")

	box := &ctx
	h := (&serverTransport{box: box}).RequestHeader()

	h.Add("k", "added-v")
	if vs := h.Values("k"); !slices.Equal(vs, []string{"wire-v", "added-v"}) {
		t.Errorf("Add must append to resolved metainfo state, got %v", vs)
	}
}

// TestMetaHeader_DelRemovesAllValues Del 删除 key 的全部值：叠加层多值与
// metainfo 两桶一并清除。
func TestMetaHeader_DelRemovesAllValues(t *testing.T) {
	ctx := context.Background()
	ctx = metainfo.WithValue(ctx, "k", "transient")
	ctx = metainfo.WithPersistentValue(ctx, "k", "persistent")

	box := &ctx
	h := (&serverTransport{box: box}).RequestHeader()

	h.Add("k", "overlay-1")
	h.Add("k", "overlay-2")
	h.Del("k")

	if got := h.Get("k"); got != "" {
		t.Errorf("Del must remove all values: Get = %q", got)
	}
	if vs := h.Values("k"); vs != nil {
		t.Errorf("Del must remove all values: Values = %v", vs)
	}
	if _, ok := metainfo.GetValue(*box, "k"); ok {
		t.Error("Del must remove metainfo transient bucket value")
	}
	if _, ok := metainfo.GetPersistentValue(*box, "k"); ok {
		t.Error("Del must remove metainfo persistent bucket value")
	}
	if slices.Contains(h.Keys(), "k") {
		t.Error("Del must remove key from Keys()")
	}
}

// TestMetaHeader_PersistentBucketVisible 回归（#202）：kitexmeta 经
// WithPersistentValue 透传的业务元信息（含 trace_id）必须对 header 通道
// 可见——此前 Get/Values/Keys 只搜 transient 桶，对同一个 ctx、同一个 key，
// kitexmeta.TraceIDFromContext 返回非空而 header.Get 返回空串（相反答案）。
func TestMetaHeader_PersistentBucketVisible(t *testing.T) {
	ctx := context.Background()
	// 模拟 kitex server 端：transmeta.MetainfoServerHandler 经
	// SetMetaInfoFromMap 把 RPC_PERSIST_ 前缀恢复进 persistent 桶
	ctx = metainfo.SetMetaInfoFromMap(ctx, map[string]string{"RPC_PERSIST_x-trace-id": "trace-abc"})
	// transient 桶放一个 transportx 写侧口径的键（对照可见性差异）
	ctx = metainfo.SetMetaInfoFromMap(ctx, map[string]string{"RPC_TRANSIT_x-user-id": "user-42"})

	box := &ctx
	h := (&serverTransport{box: box}).RequestHeader()

	if got := h.Get("x-trace-id"); got != "trace-abc" {
		t.Errorf("persistent value must be visible to header channel: Get = %q", got)
	}
	if vs := h.Values("x-trace-id"); !slices.Equal(vs, []string{"trace-abc"}) {
		t.Errorf("persistent value must be visible: Values = %v", vs)
	}
	if got := h.Get("x-user-id"); got != "user-42" {
		t.Errorf("transient value must remain visible: Get = %q", got)
	}
	keys := h.Keys()
	if !slices.Equal(keys, []string{"x-trace-id", "x-user-id"}) {
		t.Errorf("Keys must include persistent-only keys, got %v", keys)
	}
}

// TestMetaHeader_MixedBucketsOrdering 同 key 双桶并存：transient（单跳值）
// 在前、persistent（全链路值）在后，同值去重——Get 稳定取单跳值。
func TestMetaHeader_MixedBucketsOrdering(t *testing.T) {
	ctx := context.Background()
	ctx = metainfo.WithPersistentValue(ctx, "k", "persist-v")
	ctx = metainfo.WithValue(ctx, "k", "transit-v")

	box := &ctx
	h := (&serverTransport{box: box}).RequestHeader()

	if got := h.Get("k"); got != "transit-v" {
		t.Errorf("Get must prefer transient bucket value, got %q", got)
	}
	if vs := h.Values("k"); !slices.Equal(vs, []string{"transit-v", "persist-v"}) {
		t.Errorf("Values must merge buckets transient-first, got %v", vs)
	}

	// 同值去重
	ctx2 := metainfo.WithPersistentValue(context.Background(), "k", "same")
	ctx2 = metainfo.WithValue(ctx2, "k", "same")
	box2 := &ctx2
	h2 := (&serverTransport{box: box2}).RequestHeader()
	if vs := h2.Values("k"); !slices.Equal(vs, []string{"same"}) {
		t.Errorf("identical values in both buckets must dedupe, got %v", vs)
	}
}

// TestServerContext_ChainSemantics ServerContext 注入链（#192）：注入的
// Transporter 经 transport.FromServerContext 取回，header 写在同一 ctx
// 变量的盒子上原地生效。
func TestServerContext_ChainSemantics(t *testing.T) {
	ctx := context.Background()
	sctx := ServerContext(ctx, "kitex.endpoint:9000", "/pkg.Svc/Method")

	tr, ok := transport.FromServerContext(sctx)
	if !ok {
		t.Fatal("FromServerContext must report injected transporter")
	}
	if tr == nil {
		t.Fatal("FromServerContext must return the injected transporter")
	}
	if tr.Kind() != transport.KindGRPC || tr.Operation() != "/pkg.Svc/Method" {
		t.Errorf("transporter mismatch: kind=%s op=%s", tr.Kind(), tr.Operation())
	}

	h := tr.RequestHeader()
	h.Set("chain-key", "chain-v")

	// 同一 Transporter 再次取 header：前一次的写可见（盒子共享）
	h2 := tr.RequestHeader()
	if got := h2.Get("chain-key"); got != "chain-v" {
		t.Errorf("in-place box write must be visible to subsequently fetched header, got %q", got)
	}
	// metainfo 写透：非空 Set 落入共享盒子的 metainfo transient 桶
	st, ok := tr.(*serverTransport)
	if !ok {
		t.Fatalf("FromServerContext transporter = %T, want *serverTransport", tr)
	}
	if v, ok := metainfo.GetValue(*st.box, "chain-key"); !ok || v != "chain-v" {
		t.Errorf("non-empty Set must write through to metainfo transient bucket, got (%q,%v)", v, ok)
	}

	// ReplyHeader 为只读空实现
	if _, ok := tr.ReplyHeader().(transport.NoopHeader); !ok {
		t.Errorf("ReplyHeader = %T, want transport.NoopHeader", tr.ReplyHeader())
	}
}

// TestMetaHeader_OverlayIsolatedPerBox 叠加层隔离：不同盒子的 header 互不可见。
func TestMetaHeader_OverlayIsolatedPerBox(t *testing.T) {
	_, h1 := newTestHeader()
	_, h2 := newTestHeader()

	h1.Set("k", "v1")
	if got := h2.Get("k"); got != "" {
		t.Errorf("overlay must be scoped to its own box, got %q", got)
	}
}

// 编译期契约钉子：metaHeader 必须满足 core/transport.Header。
var _ transport.Header = (*metaHeader)(nil)

// TestMetaHeader_BoxIsPointerToVariable 盒子指针语义（#192 最易漂移点）：
// Go 的 context 是值语义，盒子必须是指向 ctx **变量**的指针——Set 经指针
// 重赋值，持有同一指针者（后续取出的 header / metainfo 直读者）可见；
// 若有人把盒子改成值字段，此测试即红。
func TestMetaHeader_BoxIsPointerToVariable(t *testing.T) {
	ctx := context.Background()
	box := &ctx
	tr := &serverTransport{box: box, endpoint: "e", operation: "o"}

	tr.RequestHeader().Set("k", "v")

	if v, ok := metainfo.GetValue(*box, "k"); !ok || v != "v" {
		t.Errorf("box write must be visible via the shared box pointer, got (%q,%v)", v, ok)
	}
	if (*box).Value(headerStateKey{}) == nil {
		t.Error("overlay state must live in the box ctx")
	}
}
