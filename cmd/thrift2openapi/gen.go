package main

// gen.go 是 thrift2openapi 的核心：thriftgo AST（真 parser，吃得下多行字段/
// 默认值/嵌套注解/include）→ OpenAPI 3.0 文档（#124）。
//
// 消费面锚点（替换各服务手搓的 gen_swagger.py 一类 regex 解析器）：
//   - api.<verb> 注解 → 路由（get/post/put/delete/patch 全支持，不止 get/post）；
//   - 字段绑定注解 api.query/path/header/cookie/body/json/form/none → OpenAPI
//     参数/请求体；未标注字段按 verb 兜底（GET→query，其余→JSON body）；
//   - struct 字段注解与 `//` 注释（ReservedComments）→ 参数/接口 description；
//   - 跨 IDL include 引用（module.Type）→ components/schemas 下的 $ref
//     （消手搓脚本的 $ref 死代码/覆写 bug）；
//   - 路由豁免：-exempt 配置 + api.skip 注解（消 MANUAL_ROUTES 硬编码双写）；
//   - 统一响应包络 -envelope：{code, msg, data: <原响应 schema>}；
//   - 鉴权标记：-auth 全局 security + api.noauth 注解/ -auth-exempt 配置
//     （免 JWT 标记）。

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cloudwego/thriftgo/parser"
)

// Options 是生成配置（main 由 CLI flag 填充，测试直接构造）。
type Options struct {
	// Title/Version/Description 写入 info；Title 缺省取第一个 service 名。
	Title       string
	Version     string
	Description string
	// BasePath 非空时输出 servers: [{url: BasePath}]。
	BasePath string
	// ExemptPaths 是路由豁免表：这些路径不出现在文档（如代码注册的健康检查）。
	ExemptPaths []string
	// AuthScheme 非空（如 "bearerAuth"）时输出 securitySchemes 并给全部路由
	// 挂 security 要求。
	AuthScheme string
	// AuthSchemeDescription 是 securitySchemes 的描述。
	AuthSchemeDescription string
	// AuthExemptPaths 是免鉴权路由（免 JWT 标记）：文档中显式 security: []。
	AuthExemptPaths []string
	// Envelope 开启统一响应包络 {code, msg, data}。
	Envelope bool
}

// httpVerbs 是识别的 api.<verb> 集合（手搓脚本只支持 get/post，这里全支持）。
var httpVerbs = []string{"get", "post", "put", "delete", "patch"}

type generator struct {
	ast     *parser.Thrift
	opts    Options
	doc     *OpenAPIDoc
	schemas *SchemaTable
	// building 防自引用 struct 无限递归（先占位再展开属性）。
	building map[string]bool
}

// Generate 把 thriftgo 解析结果转成 OpenAPI 3.0 文档。
func Generate(ast *parser.Thrift, opts Options) (*OpenAPIDoc, error) {
	g := &generator{
		ast:      ast,
		opts:     opts,
		schemas:  NewSchemaTable(),
		building: map[string]bool{},
	}
	if err := g.run(); err != nil {
		return nil, err
	}
	return g.doc, nil
}

func (g *generator) run() error {
	title := g.opts.Title
	if title == "" {
		if len(g.ast.Services) > 0 {
			title = g.ast.Services[0].Name
		} else {
			title = filepath.Base(g.ast.Filename)
		}
	}
	version := g.opts.Version
	if version == "" {
		version = "1.0.0"
	}
	g.doc = &OpenAPIDoc{
		OpenAPI: "3.0.3",
		Info: &Info{
			Title:       title,
			Description: g.opts.Description,
			Version:     version,
		},
		Paths: NewPathItems(),
	}
	if g.opts.BasePath != "" {
		g.doc.Servers = []*Server{{URL: g.opts.BasePath}}
	}
	for _, svc := range g.ast.Services {
		for _, fn := range svc.Functions {
			if err := g.genFunction(svc, fn); err != nil {
				return fmt.Errorf("%s.%s: %w", svc.Name, fn.Name, err)
			}
		}
	}
	// 路由豁免：文档中删除（#124：消 MANUAL_ROUTES 与注册知识双写）。
	for _, p := range g.opts.ExemptPaths {
		g.doc.Paths.Delete(p)
	}
	if g.schemas.len() > 0 || g.opts.AuthScheme != "" {
		g.doc.Components = &Components{Schemas: g.schemas}
	}
	if g.opts.AuthScheme != "" {
		ss := NewSecuritySchemeTable()
		desc := g.opts.AuthSchemeDescription
		if desc == "" {
			desc = "全局鉴权（免 JWT 路由见 api.noauth 注解 / -auth-exempt 配置）"
		}
		ss.Set(g.opts.AuthScheme, &SecurityScheme{
			Type:         "http",
			Scheme:       "bearer",
			BearerFormat: "JWT",
			Description:  desc,
		})
		g.doc.Components.SecuritySchemes = ss
	}
	return nil
}

// genFunction 处理单个 service 方法：识别 api.<verb> 路由并生成 operation。
// 无 api.<verb> 注解的方法（纯 RPC）不进 HTTP 文档。
func (g *generator) genFunction(svc *parser.Service, fn *parser.Function) error {
	// api.skip：IDL 内声明路由但明确不进文档（豁免注解，消配置双写）。
	if v, ok := annoValue(fn.Annotations, "api.skip"); ok && truthy(v) {
		return nil
	}
	for _, verb := range httpVerbs {
		route, ok := annoValue(fn.Annotations, "api."+verb)
		if !ok {
			continue
		}
		op, err := g.genOperation(svc, fn, verb, route)
		if err != nil {
			return err
		}
		pi := g.doc.Paths.Get(openAPIPath(route))
		switch verb {
		case "get":
			pi.Get = op
		case "post":
			pi.Post = op
		case "put":
			pi.Put = op
		case "delete":
			pi.Delete = op
		case "patch":
			pi.Patch = op
		}
	}
	return nil
}

func (g *generator) genOperation(svc *parser.Service, fn *parser.Function, verb, route string) (*Operation, error) {
	op := &Operation{
		Summary:     fn.Name,
		Description: cleanComment(fn.ReservedComments),
		Tags:        []string{svc.Name},
		OperationID: svc.Name + "." + fn.Name,
		Responses:   NewResponses(),
	}

	var params []*Param
	var jsonProps, formProps *Properties
	var jsonReq, formReq []string
	var wholeBody *Schema

	for _, arg := range fn.Arguments {
		if v, ok := annoValue(arg.Annotations, "api.body"); ok && v == "*" {
			// api.body="*"：整个参数体作为请求体。
			s, err := g.schemaForType(arg.Type)
			if err != nil {
				return nil, err
			}
			wholeBody = s
			continue
		}
		if arg.Type.Category != parser.Category_Struct {
			// 非 struct 参数（兜底简化）：按 query 参数输出。
			s, err := g.schemaForType(arg.Type)
			if err != nil {
				return nil, err
			}
			applyDefault(s, arg.Default)
			params = append(params, &Param{
				Name:        arg.Name,
				In:          "query",
				Description: cleanComment(arg.ReservedComments),
				Required:    arg.Requiredness == parser.FieldType_Required,
				Schema:      s,
			})
			continue
		}
		st, err := g.resolveStruct(arg.Type)
		if err != nil {
			return nil, err
		}
		for _, f := range st.Fields {
			if v, ok := annoValue(f.Annotations, "api.none"); ok && truthy(v) {
				continue
			}
			name := f.Name
			desc := cleanComment(f.ReservedComments)
			s, err := g.schemaForType(f.Type)
			if err != nil {
				return nil, fmt.Errorf("field %s: %w", f.Name, err)
			}
			applyDefault(s, f.Default)
			switch {
			case hasAnno(f.Annotations, "api.query"):
				if v, ok := annoValue(f.Annotations, "api.query"); ok && v != "" {
					name = v
				}
				params = append(params, &Param{Name: name, In: "query", Description: desc,
					Required: f.Requiredness == parser.FieldType_Required, Schema: s})
			case hasAnno(f.Annotations, "api.path"):
				if v, ok := annoValue(f.Annotations, "api.path"); ok && v != "" {
					name = v
				}
				params = append(params, &Param{Name: name, In: "path", Description: desc, Required: true, Schema: s})
			case hasAnno(f.Annotations, "api.header"):
				if v, ok := annoValue(f.Annotations, "api.header"); ok && v != "" {
					name = v
				}
				params = append(params, &Param{Name: name, In: "header", Description: desc,
					Required: f.Requiredness == parser.FieldType_Required, Schema: s})
			case hasAnno(f.Annotations, "api.cookie"):
				if v, ok := annoValue(f.Annotations, "api.cookie"); ok && v != "" {
					name = v
				}
				params = append(params, &Param{Name: name, In: "cookie", Description: desc,
					Required: f.Requiredness == parser.FieldType_Required, Schema: s})
			case hasAnno(f.Annotations, "api.form"), hasAnno(f.Annotations, "api.file"):
				if formProps == nil {
					formProps = NewProperties()
				}
				if v, ok := annoValue(f.Annotations, "api.form"); ok && v != "" {
					name = v
				}
				decorateSchema(s, desc, f.Default)
				formProps.Set(name, s)
				if f.Requiredness == parser.FieldType_Required {
					formReq = append(formReq, name)
				}
			default:
				// api.body / api.json / 未标注：GET 兜底 query，其余进 JSON body。
				if v, ok := annoValue(f.Annotations, "api.body"); ok && v != "" {
					name = v
				}
				if v, ok := annoValue(f.Annotations, "api.json"); ok && v != "" {
					name = v
				}
				if verb == "get" {
					params = append(params, &Param{Name: name, In: "query", Description: desc,
						Required: f.Requiredness == parser.FieldType_Required, Schema: s})
					continue
				}
				if jsonProps == nil {
					jsonProps = NewProperties()
				}
				decorateSchema(s, desc, f.Default)
				jsonProps.Set(name, s)
				if f.Requiredness == parser.FieldType_Required {
					jsonReq = append(jsonReq, name)
				}
			}
		}
	}

	// 请求体。
	switch {
	case wholeBody != nil:
		mt := NewMediaTypes()
		mt.Set("application/json", &MediaType{Schema: wholeBody})
		op.RequestBody = &RequestBody{Required: true, Content: mt}
	default:
		content := NewMediaTypes()
		if jsonProps != nil && jsonProps.Len() > 0 {
			content.Set("application/json", &MediaType{Schema: &Schema{
				Type: "object", Properties: jsonProps, Required: jsonReq,
			}})
		}
		if formProps != nil && formProps.Len() > 0 {
			content.Set("multipart/form-data", &MediaType{Schema: &Schema{
				Type: "object", Properties: formProps, Required: formReq,
			}})
		}
		if len(content.keys) > 0 {
			op.RequestBody = &RequestBody{Required: true, Content: content}
		}
	}

	// path 参数校验：模板里没有对应 {name} 的 path 参数直接报错——静默生成
	// 错误文档比失败更糟（手搓脚本的破损面之一）。
	tpl := openAPIPath(route)
	for _, p := range params {
		if p.In != "path" {
			continue
		}
		if !strings.Contains(tpl, "{"+p.Name+"}") {
			return nil, fmt.Errorf("path 参数 %q 未出现在路由模板 %q 中", p.Name, tpl)
		}
	}
	op.Parameters = params

	// 响应：返回类型 → 200 schema（可选统一包络）。
	if fn.FunctionType != nil && !fn.Void {
		s, err := g.schemaForType(fn.FunctionType)
		if err != nil {
			return nil, err
		}
		if g.opts.Envelope {
			data := s
			s = &Schema{
				Type: "object",
				Description: "统一响应包络（-envelope）：code=0 成功，" +
					"msg=错误信息，data=业务载荷",
				Properties: func() *Properties {
					p := NewProperties()
					p.Set("code", &Schema{Type: "integer", Format: "int32", Description: "业务码，0=成功"})
					p.Set("msg", &Schema{Type: "string", Description: "错误信息，成功时为空"})
					p.Set("data", data)
					return p
				}(),
				Required: []string{"code", "msg"},
			}
		}
		resp := &Response{Description: respDesc(fn)}
		mt := NewMediaTypes()
		mt.Set("application/json", &MediaType{Schema: s})
		resp.Content = mt
		op.Responses.Set("200", resp)
	} else {
		op.Responses.Set("200", &Response{Description: respDesc(fn)})
	}

	// 鉴权标记：全局 security（-auth）；免 JWT（api.noauth / -auth-exempt）
	// 显式置空，文档读者可分辨「无要求」与「未标记」。
	if g.opts.AuthScheme != "" {
		if noAuth, ok := annoValue(fn.Annotations, "api.noauth"); ok && truthy(noAuth) {
			op.Security = &SecurityReqs{}
		} else if containsPath(g.opts.AuthExemptPaths, tpl) {
			op.Security = &SecurityReqs{}
		} else {
			op.Security = &SecurityReqs{Schemes: []string{g.opts.AuthScheme}}
		}
	}
	return op, nil
}

// schemaForType 把 thrift Type 转 OpenAPI Schema（struct/enum 进 components，
// 其余内联）。
func (g *generator) schemaForType(t *parser.Type) (*Schema, error) {
	if t == nil {
		return nil, fmt.Errorf("nil type")
	}
	// typedef 解引用（含跨文件 typedef）。
	if td, ok := g.resolveTypedef(t); ok {
		return g.schemaForType(td.Type)
	}
	switch t.Category {
	case parser.Category_Bool:
		return &Schema{Type: "boolean"}, nil
	case parser.Category_Byte:
		return &Schema{Type: "integer", Format: "int32"}, nil
	case parser.Category_I16:
		return &Schema{Type: "integer", Format: "int32"}, nil
	case parser.Category_I32:
		return &Schema{Type: "integer", Format: "int32"}, nil
	case parser.Category_I64:
		return &Schema{Type: "integer", Format: "int64"}, nil
	case parser.Category_Double:
		return &Schema{Type: "number", Format: "double"}, nil
	case parser.Category_String:
		return &Schema{Type: "string"}, nil
	case parser.Category_Binary:
		return &Schema{Type: "string", Format: "byte"}, nil
	case parser.Category_List, parser.Category_Set:
		items, err := g.schemaForType(t.ValueType)
		if err != nil {
			return nil, err
		}
		s := &Schema{Type: "array", Items: items}
		if t.Category == parser.Category_Set {
			s.UniqueItems = true
		}
		return s, nil
	case parser.Category_Map:
		vs, err := g.schemaForType(t.ValueType)
		if err != nil {
			return nil, err
		}
		s := &Schema{Type: "object", AdditionalProperties: vs}
		if t.KeyType != nil && t.KeyType.Category != parser.Category_String {
			s.Description = "thrift map 的 key 类型为 " + t.KeyType.Name +
				"（OpenAPI 3.0 仅表达 string key，按值类型输出）"
		}
		return s, nil
	case parser.Category_Enum:
		e, err := g.resolveEnum(t)
		if err != nil {
			return nil, err
		}
		return g.enumSchema(t.Name, e), nil
	case parser.Category_Struct:
		st, err := g.resolveStruct(t)
		if err != nil {
			return nil, err
		}
		return g.structSchema(t.Name, st), nil
	default:
		return nil, fmt.Errorf("不支持的 thrift 类型 %q (category=%d)", t.Name, t.Category)
	}
}

// structSchema 注册 struct schema 并返回 $ref。自引用安全：先登记占位再展开。
func (g *generator) structSchema(name string, st *parser.StructLike) *Schema {
	if existing := g.schemas.Get(name); existing != nil {
		return refSchema(name)
	}
	if g.building[name] {
		return refSchema(name)
	}
	g.building[name] = true
	props := NewProperties()
	var required []string
	for _, f := range st.Fields {
		s, err := g.schemaForType(f.Type)
		if err == nil {
			decorateSchema(s, cleanComment(f.ReservedComments), f.Default)
			props.Set(f.Name, s)
			if f.Requiredness == parser.FieldType_Required {
				required = append(required, f.Name)
			}
		}
	}
	schema := &Schema{
		Type:        "object",
		Description: cleanComment(st.ReservedComments),
		Properties:  props,
		Required:    required,
	}
	g.schemas.Set(name, schema)
	return refSchema(name)
}

// enumSchema 注册 enum schema（thrift enum 在线上按 i32 传输）。
func (g *generator) enumSchema(name string, e *parser.Enum) *Schema {
	if existing := g.schemas.Get(name); existing != nil {
		return refSchema(name)
	}
	vals := make([]any, 0, len(e.Values))
	var pairs []string
	for _, v := range e.Values {
		vals = append(vals, v.Value)
		pairs = append(pairs, fmt.Sprintf("%s=%d", v.Name, v.Value))
	}
	g.schemas.Set(name, &Schema{
		Type:        "integer",
		Format:      "int32",
		Description: strings.TrimSpace(cleanComment(e.ReservedComments) + "\n取值：" + strings.Join(pairs, ", ")),
		Enum:        vals,
	})
	return refSchema(name)
}

// resolveStruct 解析 struct 引用（本文件或 include）。
func (g *generator) resolveStruct(t *parser.Type) (*parser.StructLike, error) {
	cu, name := g.splitRef(t.Name)
	if st, ok := cu.GetStruct(name); ok {
		return st, nil
	}
	if st, ok := cu.GetUnion(name); ok {
		return st, nil
	}
	return nil, fmt.Errorf("找不到 struct %q", t.Name)
}

// resolveEnum 解析 enum 引用（本文件或 include）。
func (g *generator) resolveEnum(t *parser.Type) (*parser.Enum, error) {
	cu, name := g.splitRef(t.Name)
	if e, ok := cu.GetEnum(name); ok {
		return e, nil
	}
	return nil, fmt.Errorf("找不到 enum %q", t.Name)
}

// resolveTypedef 解析 typedef 引用（本文件或 include）。
func (g *generator) resolveTypedef(t *parser.Type) (*parser.Typedef, bool) {
	cu, name := g.splitRef(t.Name)
	if td, ok := cu.GetTypedef(name); ok {
		return td, true
	}
	return nil, false
}

// splitRef 把 "module.Type" 拆成（对应 include 的 AST, 本地名）；无前缀则
// 返回主 IDL。
func (g *generator) splitRef(name string) (*parser.Thrift, string) {
	if i := strings.LastIndex(name, "."); i > 0 {
		mod, local := name[:i], name[i+1:]
		for _, inc := range g.ast.Includes {
			base := strings.TrimSuffix(filepath.Base(inc.Path), ".thrift")
			if base == mod && inc.Reference != nil {
				return inc.Reference, local
			}
		}
	}
	return g.ast, name
}

// annoValue 取注解首个值。
func annoValue(annos parser.Annotations, key string) (string, bool) {
	for _, a := range annos {
		if a.Key == key && len(a.Values) > 0 {
			return a.Values[0], true
		}
	}
	return "", false
}

// hasAnno 判断注解是否存在（值可为空）。
func hasAnno(annos parser.Annotations, key string) bool {
	for _, a := range annos {
		if a.Key == key {
			return true
		}
	}
	return false
}

// truthy 宽松布尔："true"/"1"/"yes"。
func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes":
		return true
	}
	return false
}

// openAPIPath 把 hertz 风格 ":param" 转成 OpenAPI "{param}"。
func openAPIPath(route string) string {
	segs := strings.Split(route, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, ":") {
			segs[i] = "{" + s[1:] + "}"
		}
	}
	return strings.Join(segs, "/")
}

// cleanComment 把 thrift `//` 注释块转成 description。
func cleanComment(s string) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(l)
		l = strings.TrimPrefix(l, "//")
		l = strings.TrimSpace(l)
		out = append(out, l)
	}
	// 去掉尾部空行。
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

// containsPath 判断路径是否在列表中（支持忽略尾斜杠差异）。
func containsPath(list []string, path string) bool {
	norm := func(p string) string { return strings.TrimSuffix(p, "/") }
	for _, p := range list {
		if norm(p) == norm(path) {
			return true
		}
	}
	return false
}

// respDesc 生成响应描述。
func respDesc(fn *parser.Function) string {
	if c := cleanComment(fn.ReservedComments); c != "" {
		return strings.SplitN(c, "\n", 2)[0]
	}
	return "成功响应"
}

// len 返回 schema 数量。
func (t *SchemaTable) len() int { return len(t.keys) }

// decorateSchema 给请求体属性 schema 补 description 与 default（参数的
// description 在 Param 层，不重复）。$ref schema 不加描述（OpenAPI 3.0 中
// $ref 与 siblings 互斥）。
func decorateSchema(s *Schema, desc string, def *parser.ConstValue) {
	if s == nil || s.Ref != "" {
		return
	}
	if s.Description == "" && desc != "" {
		s.Description = desc
	}
	applyDefault(s, def)
}

// applyDefault 只补 default（参数路径用：description 已在 Param 层）。
func applyDefault(s *Schema, def *parser.ConstValue) {
	if s == nil || s.Ref != "" {
		return
	}
	if v := constToAny(def); v != nil {
		s.Default = v
	}
}

// constToAny 把 thrift ConstValue 转 OpenAPI default（标量与列表；map 默认值
// 罕见，跳过）。
func constToAny(cv *parser.ConstValue) any {
	if cv == nil || cv.TypedValue == nil {
		return nil
	}
	tv := cv.TypedValue
	switch cv.Type {
	case parser.ConstType_ConstInt:
		if tv.Int != nil {
			return *tv.Int
		}
	case parser.ConstType_ConstDouble:
		if tv.Double != nil {
			return *tv.Double
		}
	case parser.ConstType_ConstLiteral:
		if tv.Literal != nil {
			return unquote(*tv.Literal)
		}
	case parser.ConstType_ConstIdentifier:
		if tv.Identifier != nil {
			return unquote(*tv.Identifier)
		}
	case parser.ConstType_ConstList:
		out := make([]any, 0, len(tv.List))
		for _, item := range tv.List {
			if v := constToAny(item); v != nil {
				out = append(out, v)
			}
		}
		return out
	}
	return nil
}

// unquote 去掉字面量的引号（thriftgo 保留原文）。
func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
