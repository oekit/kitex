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

// TestAnnoBool_BareAndValueForms 钉住布尔注解的完整语义面（#223）：
// annoValue 要求 len(Values) > 0，bare 形态（注解在、值列表空）此前 ok=false
// 被静默吞掉，与同文件 annoLookup「bare 返回 ("", true)」的语义分裂——
// api.skip/api.none/api.noauth 写成 bare 或 = "" 时生成的文档毫无效果且
// 不报错。annoBool 按「存在即真」兜底，非空值仍走 truthy 宽松解析。
func TestAnnoBool_BareAndValueForms(t *testing.T) {
	cases := []struct {
		name string
		// annos 直接构造：thriftgo 文法 Annotation <- Identifier EQUAL
		// Literal 要求带值，bare（Values 为空）只能来自 AST 构造/上游
		// 改动，正是要兜住的形态。
		annos parser.Annotations
		key   string
		want  bool
	}{
		{"注解不存在", parser.Annotations{{Key: "api.get", Values: []string{"/x"}}}, "api.skip", false},
		{"bare 无值", parser.Annotations{{Key: "api.skip"}}, "api.skip", true},
		{"bare 空值列表", parser.Annotations{{Key: "api.skip", Values: []string{}}}, "api.skip", true},
		{"显式空串", parser.Annotations{{Key: "api.skip", Values: []string{""}}}, "api.skip", true},
		{"true", parser.Annotations{{Key: "api.skip", Values: []string{"true"}}}, "api.skip", true},
		{"1", parser.Annotations{{Key: "api.skip", Values: []string{"1"}}}, "api.skip", true},
		{"yes", parser.Annotations{{Key: "api.skip", Values: []string{"yes"}}}, "api.skip", true},
		{"FALSE 显式否定", parser.Annotations{{Key: "api.skip", Values: []string{"false"}}}, "api.skip", false},
		{"0 显式否定", parser.Annotations{{Key: "api.skip", Values: []string{"0"}}}, "api.skip", false},
		{"大小写不敏感", parser.Annotations{{Key: "api.skip", Values: []string{"TRUE"}}}, "api.skip", true},
		{"首值生效", parser.Annotations{{Key: "api.skip", Values: []string{"true", "false"}}}, "api.skip", true},
		{"键不同", parser.Annotations{{Key: "api.skip", Values: []string{"true"}}}, "api.noauth", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := annoBool(tc.annos, tc.key); got != tc.want {
				t.Errorf("annoBool(%+v, %q) = %v, want %v", tc.annos, tc.key, got, tc.want)
			}
		})
	}
}

// TestGenerate_BooleanAnnoEmptyValueForms 端到端：三个布尔注解写成空值形态
// （= ""，thriftgo 文法下唯一可从 IDL 写出的「无值」形态）必须生效——
// 修复前 api.skip = "" 路由照进文档、api.noauth = "" 不免鉴权、
// api.none = "" 字段照进 schema，三处全部静默失效（#223）。
func TestGenerate_BooleanAnnoEmptyValueForms(t *testing.T) {
	dir := t.TempDir()
	idl := filepath.Join(dir, "bool.thrift")
	content := `
struct Q {
    1: optional string keep
    2: optional string drop (api.none = "")
}
service Svc {
    void skipped(1: Q q) (api.post = "/skipped", api.skip = "")
    void open(1: Q q) (api.post = "/open", api.noauth = "")
    void normal(1: Q q) (api.post = "/normal")
}
`
	if err := os.WriteFile(idl, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	ast, err := parseIDL(idl)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Generate(ast, Options{AuthScheme: "bearerAuth"})
	if err != nil {
		t.Fatal(err)
	}

	if pathExists(doc, "/skipped") {
		t.Error("api.skip = \"\" 必须豁免路由（bare 形态静默失效，#223）")
	}
	if !pathExists(doc, "/open") || !pathExists(doc, "/normal") {
		t.Error("未豁免的路由必须保留")
	}
	if sec := doc.Paths.items["/open"].Post.Security; sec == nil || len(sec.Schemes) != 0 {
		t.Errorf("api.noauth = \"\" 必须显式 security: [], got %+v", sec)
	}
	if sec := doc.Paths.items["/normal"].Post.Security; sec == nil || len(sec.Schemes) != 1 || sec.Schemes[0] != "bearerAuth" {
		t.Errorf("普通路由应挂 bearerAuth, got %+v", sec)
	}

	body := doc.Paths.items["/open"].Post.RequestBody.Content.items["application/json"].Schema
	if body.Properties.Get("keep") == nil {
		t.Error("未标注字段必须保留")
	}
	if body.Properties.Get("drop") != nil {
		t.Error("api.none = \"\" 必须排除字段（bare 形态静默失效，#223）")
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

// ===== 第三轮审计 feat：#281 包络字段定制 + 错误响应模板 / #288 补录表 =====

// TestGenerate_EnvelopeCustomFields #281：-envelope-fields 有序字段定制——
// octopus 形态 {code, data, message, trace_id}（msg→message 改名 + trace_id）。
func TestGenerate_EnvelopeCustomFields(t *testing.T) {
	opts := goldenOpts
	opts.EnvelopeFields = []string{"code", "data", "message", "trace_id"}
	doc, err := Generate(parseDemo(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	env := doc.Paths.items["/api/v1/send"].Post.Responses.items["200"].
		Content.items["application/json"].Schema
	names := env.Properties.keys
	want := []string{"code", "data", "message", "trace_id"}
	if len(names) != len(want) {
		t.Fatalf("包络字段 = %v, want %v（有序定制，data 承载载荷）", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("包络字段顺序 = %v, want %v", names, want)
		}
	}
	if env.Properties.Get("msg") != nil {
		t.Error("定制字段后不应再出现缺省 msg")
	}
	if got := env.Properties.Get("data"); got == nil || got.Ref != "#/components/schemas/SendResp" {
		t.Errorf("data 仍应 $ref 业务 schema, got %+v", got)
	}
	// code/message 必填、trace_id 可选（与缺省 code,msg 必填口径一致）。
	req := strings.Join(env.Required, ",")
	if req != "code,message" {
		t.Errorf("required = %q, want code,message", req)
	}
}

// TestGenerate_ErrorResponses #281：统一错误响应模板；免鉴权路由（api.noauth
// / -auth-exempt）跳过 401；IDL 显式声明同码响应不被覆盖。
func TestGenerate_ErrorResponses(t *testing.T) {
	dir := t.TempDir()
	idl := filepath.Join(dir, "err.thrift")
	content := `
service Svc {
    void ping() (api.get = "/ping", api.noauth = "true")
    void login() (api.post = "/login")
}
`
	if err := os.WriteFile(idl, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	ast, err := parseIDL(idl)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Generate(ast, Options{
		AuthScheme:      "bearerAuth",
		AuthExemptPaths: []string{"/login"},
		ErrorResponses: []ErrorResponse{
			{Code: 400, Description: "参数错误"},
			{Code: 401, Description: "未登录或 token 失效"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	ping := doc.Paths.items["/ping"].Get
	if _, ok := ping.Responses.items["400"]; !ok {
		t.Error("ping 应带 400 模板")
	}
	if _, ok := ping.Responses.items["401"]; ok {
		t.Error("api.noauth 路由必须跳过 401")
	}
	login := doc.Paths.items["/login"].Post
	if _, ok := login.Responses.items["401"]; ok {
		t.Error("-auth-exempt 路由必须跳过 401")
	}
	if got := login.Responses.items["400"]; got == nil || got.Description != "参数错误" {
		t.Errorf("login 400 描述 = %+v", got)
	}
}

// TestParseErrorResponses 解析器：正常/坏段/重复。
func TestParseErrorResponses(t *testing.T) {
	got, err := parseErrorResponses(" 400=参数错误 , 401=未登录或 token 失效 ")
	if err != nil || len(got) != 2 || got[0].Code != 400 || got[1].Description != "未登录或 token 失效" {
		t.Fatalf("parse = %+v, %v", got, err)
	}
	for _, bad := range []string{"400", "abc=x", "400="} {
		if _, err := parseErrorResponses(bad); err == nil {
			t.Errorf("坏段 %q 必须报错", bad)
		}
	}
	if _, err := parseErrorResponses("400=a,400=b"); err == nil {
		t.Error("重复 code 必须报错")
	}
}

// TestGenerate_ExtraRoutes #288：补录表两种消费形态——plain（text/plain 非
// 包络探活）与 data（包络内联 schema）；security 三态；非法条目报错。
func TestGenerate_ExtraRoutes(t *testing.T) {
	dir := t.TempDir()
	routes := filepath.Join(dir, "extra.yaml")
	yamlSrc := `
- method: GET
  path: /ping
  tag: Infra
  summary: Ping
  plain: true
  security: []
- method: GET
  path: /api/v2/user/info
  tag: Identity
  summary: UserInfo
  data:
    userId: {type: string}
    username: {type: string}
  security:
    - bearerAuth: []
- method: POST
  path: /api/v2/extra
  summary: Extra
  responses:
    "400": 参数错误
`
	if err := os.WriteFile(routes, []byte(yamlSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadExtraRoutes(routes)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Generate(parseDemo(t), Options{
		AuthScheme:  "bearerAuth",
		ExtraRoutes: loaded,
	})
	if err != nil {
		t.Fatal(err)
	}

	// plain 探活：text/plain string、security: []。
	ping := doc.Paths.items["/ping"].Get
	pt := ping.Responses.items["200"].Content.items["text/plain"]
	if pt == nil || pt.Schema.Type != "string" {
		t.Errorf("plain 200 应为 text/plain string, got %+v", pt)
	}
	if ping.Security == nil || len(ping.Security.Schemes) != 0 {
		t.Errorf("显式空 security 应输出 security: [], got %+v", ping.Security)
	}

	// data 包络：application/json、data 内联 schema、security 显式 scheme。
	info := doc.Paths.items["/api/v2/user/info"].Get
	body := info.Responses.items["200"].Content.items["application/json"].Schema
	data := body.Properties.Get("data")
	if data == nil || data.Properties.Get("userId") == nil {
		t.Errorf("data 内联 schema 缺失, got %+v", data)
	}
	if info.Security == nil || len(info.Security.Schemes) != 1 || info.Security.Schemes[0] != "bearerAuth" {
		t.Errorf("显式 security 应为 bearerAuth, got %+v", info.Security)
	}

	// 无 data/plain + 附加 responses：仅描述 + 400。
	extra := doc.Paths.items["/api/v2/extra"].Post
	if extra.Responses.items["200"].Content != nil {
		t.Error("未给 data/plain 的补录不应生成 content")
	}
	if got := extra.Responses.items["400"]; got == nil || got.Description != "参数错误" {
		t.Errorf("补录 400 响应缺失, got %+v", got)
	}
	// security 缺省跟随全局 -auth。
	if extra.Security == nil || len(extra.Security.Schemes) != 1 {
		t.Errorf("security 缺省应跟随全局 -auth, got %+v", extra.Security)
	}

	// 非法条目：坏方法 / 坏 path / 重复条目。
	for name, bad := range map[string][]ExtraRoute{
		"badMethod": {{Method: "options", Path: "/x"}},
		"badPath":   {{Method: "get", Path: "x"}},
		"dup":       {{Method: "get", Path: "/x"}, {Method: "GET", Path: "/x"}},
	} {
		if _, err := Generate(parseDemo(t), Options{ExtraRoutes: bad}); err == nil {
			t.Errorf("%s 必须报错", name)
		}
	}
}
