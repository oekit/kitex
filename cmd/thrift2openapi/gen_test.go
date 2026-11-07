package main

// 端到端（golden）与单元测试：样例 IDL → 期望 YAML/JSON 对比（#124）。
// golden 文件由生成器自身产出后人工审阅钉住——后续任何行为变化都会以
// diff 形式暴露，防止生成语义悄悄漂移（手搓脚本的腐烂形态）。

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/cloudwego/thriftgo/parser"
)

// goldenOpts 是 golden 用例的固定配置（与 testdata/*.thrift 组合钉住全量行为）。
var goldenOpts = Options{
	Title:           "Demo API",
	Version:         "v2",
	BasePath:        "https://demo.example.com",
	ExemptPaths:     []string{"/healthz"},
	AuthScheme:      "bearerAuth",
	AuthExemptPaths: []string{"/api/v1/login"},
	Envelope:        true,
}

func parseDemo(t *testing.T) *parser.Thrift {
	t.Helper()
	ast, err := parseIDL("testdata/demo.thrift")
	if err != nil {
		t.Fatalf("parseIDL: %v", err)
	}
	return ast
}

// TestGolden_YAML 端到端：样例 IDL → 期望 YAML 逐字节对比。
func TestGolden_YAML(t *testing.T) {
	doc, err := Generate(parseDemo(t), goldenOpts)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	got, err := marshalYAMLDoc(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want, err := os.ReadFile("testdata/demo.openapi.yaml")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("YAML 输出与 golden 不一致（生成语义漂移），diff:\n%s", diffHint(string(want), string(got)))
	}
}

// TestGolden_JSON 端到端：样例 IDL → 期望 JSON 逐字节对比。
func TestGolden_JSON(t *testing.T) {
	doc, err := Generate(parseDemo(t), goldenOpts)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	got, err := marshalJSONDoc(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want, err := os.ReadFile("testdata/demo.openapi.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if !bytes.Equal(bytes.TrimRight(got, "\n"), bytes.TrimRight(want, "\n")) {
		t.Fatalf("JSON 输出与 golden 不一致")
	}
}

// TestCLI_Run_WritesFile CLI 入口端到端：run() 直接可调（不起子进程），
// 输出文件与 golden 一致。
func TestCLI_Run_WritesFile(t *testing.T) {
	out := filepath.Join(t.TempDir(), "api.yaml")
	args := []string{
		"-idl", "testdata/demo.thrift",
		"-o", out,
		"-title", "Demo API",
		"-version", "v2",
		"-base-path", "https://demo.example.com",
		"-exempt", "/healthz",
		"-auth", "bearerAuth",
		"-auth-exempt", "/api/v1/login",
		"-envelope",
	}
	if err := run(args); err != nil {
		t.Fatalf("run: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile("testdata/demo.openapi.yaml")
	if !bytes.Equal(got, want) {
		t.Fatal("CLI 输出与 golden 不一致")
	}
}

// TestCLI_Run_MissingIDL 必填参数校验。
func TestCLI_Run_MissingIDL(t *testing.T) {
	if err := run([]string{}); err == nil {
		t.Fatal("缺 -idl 必须报错")
	}
}

// TestParse_MultiLineFieldDefaultNestedAnno 钉住「regex 破损面」的解析正确性：
// 多行字段默认值（channels = [\n "email"\n]）、嵌套注解、跨文件引用——
// 手搓 FIELD_RE/METHOD_RE 在这些形态下即破，真 parser 必须全部吃下。
func TestParse_MultiLineFieldDefaultNestedAnno(t *testing.T) {
	ast := parseDemo(t)
	st, ok := ast.GetStruct("SendReq")
	if !ok {
		t.Fatal("SendReq not found")
	}
	wantFields := map[string]bool{"title": false, "content": false, "channels": false, "page": false, "trace": false}
	for _, f := range st.Fields {
		if _, ok := wantFields[f.Name]; !ok {
			t.Errorf("意外字段 %q", f.Name)
			continue
		}
		wantFields[f.Name] = true
		switch f.Name {
		case "title":
			if v, _ := annoValue(f.Annotations, "api.body"); v != "title" {
				t.Errorf("title api.body = %q", v)
			}
		case "content":
			if v, _ := annoValue(f.Annotations, "api.json"); v != "content" {
				t.Errorf("content api.json = %q", v)
			}
		case "channels":
			if f.Default == nil {
				t.Error("channels 默认值丢失（regex 破损面）")
			}
		case "trace":
			if v, _ := annoValue(f.Annotations, "api.header"); v != "X-Request-ID" {
				t.Errorf("trace api.header = %q", v)
			}
		}
	}
	for name, seen := range wantFields {
		if !seen {
			t.Errorf("字段 %q 未解析到（多行默认值把 regex 解析器带偏的典型形态）", name)
		}
	}
}

// TestGenerate_RefDedupAndSelfReference $ref 生成：include 引用
// （common.Page/common.Status）与自引用（User.manager）都以 $ref 收敛，
// 且 components 只登记一份——手搓脚本的 $ref 覆写死代码在这里必须不存在。
func TestGenerate_RefDedupAndSelfReference(t *testing.T) {
	doc, err := Generate(parseDemo(t), goldenOpts)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Components == nil || doc.Components.Schemas == nil {
		t.Fatal("components.schemas 缺失")
	}
	user := doc.Components.Schemas.Get("User")
	if user == nil {
		t.Fatal("User schema 缺失")
	}
	mgr := user.Properties.Get("manager")
	if mgr == nil || mgr.Ref != "#/components/schemas/User" {
		t.Errorf("User.manager 应为自引用 $ref, got %+v", mgr)
	}
	if doc.Components.Schemas.Get("common.Page") == nil {
		t.Error("include 引用 common.Page 未登记 $ref")
	}
	if doc.Components.Schemas.Get("common.Status") == nil {
		t.Error("include 引用 common.Status 未登记 $ref")
	}
}

// TestGenerate_ExemptAndSkip 路由豁免两形态：-exempt 配置（/healthz）与
// api.skip 注解（/api/v1/internal）都不出现在文档；普通路由保留。
func TestGenerate_ExemptAndSkip(t *testing.T) {
	doc, err := Generate(parseDemo(t), goldenOpts)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/healthz", "/api/v1/internal"} {
		if pathExists(doc, p) {
			t.Errorf("豁免路由 %s 不应出现在文档中", p)
		}
	}
	if !pathExists(doc, "/api/v1/send") {
		t.Error("/api/v1/send 应在文档中")
	}
}

func pathExists(doc *OpenAPIDoc, path string) bool {
	return doc.Paths.items[path] != nil
}

// TestGenerate_NoAuthMarks 免 JWT 标记：api.noauth 注解与 -auth-exempt 配置
// 都显式 security: []（区分「无要求」与「未标记」）；普通路由挂全局方案。
func TestGenerate_NoAuthMarks(t *testing.T) {
	doc, err := Generate(parseDemo(t), goldenOpts)
	if err != nil {
		t.Fatal(err)
	}
	login := doc.Paths.items["/api/v1/login"].Post
	if login.Security == nil || len(login.Security.Schemes) != 0 {
		t.Errorf("login 应显式免鉴权 security: [], got %+v", login.Security)
	}
	send := doc.Paths.items["/api/v1/send"].Post
	if send.Security == nil || len(send.Security.Schemes) != 1 || send.Security.Schemes[0] != "bearerAuth" {
		t.Errorf("sendNotice 应挂 bearerAuth, got %+v", send.Security)
	}
	if doc.Components.SecuritySchemes.Get("bearerAuth") == nil {
		t.Error("securitySchemes.bearerAuth 缺失")
	}
}

// TestGenerate_Envelope 统一响应包络：data 引用原响应 schema。
func TestGenerate_Envelope(t *testing.T) {
	doc, err := Generate(parseDemo(t), goldenOpts)
	if err != nil {
		t.Fatal(err)
	}
	body := doc.Paths.items["/api/v1/send"].Post.Responses.items["200"]
	data := body.Content.items["application/json"].Schema.Properties.Get("data")
	if data == nil || data.Ref != "#/components/schemas/SendResp" {
		t.Errorf("envelope.data 应 $ref SendResp, got %+v", data)
	}
}

// TestGenerate_PathParamMismatchFails path 参数与路由模板不匹配时**报错**，
// 不静默生成错误文档。
func TestGenerate_PathParamMismatchFails(t *testing.T) {
	dir := t.TempDir()
	idl := filepath.Join(dir, "bad.thrift")
	content := `
struct Req {
    1: required i64 id (api.path = "id")
}
service Svc {
    void get(1: Req req) (api.get = "/api/v1/no-template")
}
`
	if err := os.WriteFile(idl, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	ast, err := parseIDL(idl)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(ast, Options{}); err == nil {
		t.Fatal("path 参数不在模板中必须报错")
	}
}

// TestGenerate_HertzColonPathToBraces hertz 风格 ":id" 转成 OpenAPI "{id}"。
func TestGenerate_HertzColonPathToBraces(t *testing.T) {
	doc, err := Generate(parseDemo(t), goldenOpts)
	if err != nil {
		t.Fatal(err)
	}
	if !pathExists(doc, "/api/v1/notice/{id}") {
		t.Error("hertz 风格 :id 应转成 {id}")
	}
}

// TestGenerate_VerbCoverage verb 覆盖面：get/post/put/delete 全部生效
// （手搓脚本只支持 get/post）。
func TestGenerate_VerbCoverage(t *testing.T) {
	doc, err := Generate(parseDemo(t), goldenOpts)
	if err != nil {
		t.Fatal(err)
	}
	notice := doc.Paths.items["/api/v1/notice/{id}"]
	if notice.Get == nil || notice.Delete == nil {
		t.Error("get/delete verb 未生成")
	}
	user := doc.Paths.items["/api/v1/user"]
	if user.Get == nil || user.Put == nil {
		t.Error("get/put verb 未生成")
	}
}

// TestGenerate_DefaultsCarried 字段默认值进 OpenAPI default（regex 破损面）。
func TestGenerate_DefaultsCarried(t *testing.T) {
	doc, err := Generate(parseDemo(t), goldenOpts)
	if err != nil {
		t.Fatal(err)
	}
	body := doc.Paths.items["/api/v1/send"].Post.RequestBody.Content.items["application/json"].Schema
	ch := body.Properties.Get("channels")
	if ch == nil {
		t.Fatal("channels 属性缺失")
	}
	def, ok := ch.Default.([]any)
	if !ok || len(def) != 1 || def[0] != "email" {
		t.Errorf("channels default = %#v, want [email]", ch.Default)
	}
}

// TestOpenAPIPath 单元：":param" → "{param}"。
func TestOpenAPIPath(t *testing.T) {
	cases := map[string]string{
		"/api/v1/user/:id/profile": "/api/v1/user/{id}/profile",
		"/api/v1/send":             "/api/v1/send",
		"/:a/:b":                   "/{a}/{b}",
	}
	for in, want := range cases {
		if got := openAPIPath(in); got != want {
			t.Errorf("openAPIPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCSV flag 逗号拆分。
func TestCSV(t *testing.T) {
	got := csv(" /a, b ,,c ")
	if len(got) != 3 || got[0] != "/a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("csv = %#v", got)
	}
}

// diffHint 输出首个差异上下文（golden 对比失败时定位用）。
func diffHint(want, got string) string {
	wl := strings.Split(want, "\n")
	gl := strings.Split(got, "\n")
	for i := 0; i < len(wl) && i < len(gl); i++ {
		if wl[i] != gl[i] {
			return strings.Join([]string{
				"--- want (line " + strconv.Itoa(i+1) + ")",
				wl[i],
				"+++ got",
				gl[i],
			}, "\n")
		}
	}
	return "行数不同：want=" + strconv.Itoa(len(wl)) + " got=" + strconv.Itoa(len(gl))
}
