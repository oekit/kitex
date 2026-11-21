package main

// extra.go：#288 -extra-routes 结构化补录表 + #281 错误响应模板。
//
// 背景（消费实证，octopus）：每个服务都有有意留在 IDL 外的路由（探活×4
// 共享模块注册、网关身份回显等）——-exempt/api.skip 只有「豁免」没有
// 「补录」，消费者被迫在文档后处理脚本里手工注入（MANUAL_ROUTES 表 ~80
// 行）。本文件把这两项收编进工具：
//   - ExtraRoutes（-extra-routes <file>，YAML/JSON）：结构化补录表，
//     支持 plain（text/plain 非包络响应）与 data（包络 data 内联 schema）
//     两种消费方实测形态；security 缺省跟随全局 -auth。
//   - ErrorResponses（-error-responses "400=参数错误,401=登录失效"）：
//     统一错误响应模板；免鉴权路由（api.noauth / -auth-exempt）跳过 401。

import (
	"os"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	ekiterr "github.com/oekit/pkg/errors"
)

// ErrorResponse 是一条统一错误响应模板（code=description）。
type ErrorResponse struct {
	Code        int
	Description string
}

// parseErrorResponses 解析 "400=参数错误,401=未登录或 token 失效" 形态。
// 空串 → nil；坏段（无 '=' / 非数字 / 空描述）报错——静默忽略会让模板
// 静默缺项（文档契约面，宁炸不漂）。
func parseErrorResponses(s string) ([]ErrorResponse, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var out []ErrorResponse
	seen := map[int]bool{}
	for _, seg := range strings.Split(s, ",") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		codeStr, desc, ok := strings.Cut(seg, "=")
		if !ok {
			return nil, ekiterr.Newf("-error-responses 段 %q 缺 '='（形态 code=描述）", seg)
		}
		code, err := strconv.Atoi(strings.TrimSpace(codeStr))
		if err != nil {
			return nil, ekiterr.Wrapf(err, "-error-responses 段 %q 的 code 不是整数", seg)
		}
		desc = strings.TrimSpace(desc)
		if desc == "" {
			return nil, ekiterr.Newf("-error-responses 段 %q 描述为空", seg)
		}
		if seen[code] {
			return nil, ekiterr.Newf("-error-responses 重复的 code %d", code)
		}
		seen[code] = true
		out = append(out, ErrorResponse{Code: code, Description: desc})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}

// ExtraRoute 一条补录路由（-extra-routes 文件条目）。字段语义见 #288：
//   - plain：text/plain string 响应（非统一包络的探活形态）；
//   - data：包络 data 的内联 schema（字段名 → {type: string} 等）；
//   - security：nil=跟随全局 -auth；空列表=签发 security: []；非空=列出的
//     scheme 名（如 [{bearerAuth: []}]）。
type ExtraRoute struct {
	Method      string              `yaml:"method" json:"method"`
	Path        string              `yaml:"path" json:"path"`
	Tag         string              `yaml:"tag" json:"tag"`
	Summary     string              `yaml:"summary" json:"summary"`
	Description string              `yaml:"description" json:"description"`
	Security    *[]map[string][]any `yaml:"security" json:"security"`
	Plain       bool                `yaml:"plain" json:"plain"`
	Data        map[string]*Schema  `yaml:"data" json:"data"`
	Responses   map[string]string   `yaml:"responses" json:"responses"`
}

// loadExtraRoutes 读取并解析补录表（YAML 是 JSON 超集，两种形态同一入口）。
func loadExtraRoutes(path string) ([]ExtraRoute, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, ekiterr.Wrapf(err, "读取 -extra-routes 文件")
	}
	var routes []ExtraRoute
	if err := yaml.Unmarshal(raw, &routes); err != nil {
		return nil, ekiterr.Wrapf(err, "解析 -extra-routes 文件 %s", path)
	}
	return routes, nil
}

// applyExtraRoutes 把补录路由写进文档（在豁免删除之后调用：补录是显式
// 意图，允许与豁免并存 / 覆盖同名 method+path 的内联生成操作）。
func (g *generator) applyExtraRoutes() error {
	seen := map[string]bool{}
	for i, r := range g.opts.ExtraRoutes {
		method := strings.ToLower(strings.TrimSpace(r.Method))
		if !isHTTPVerb(method) {
			return ekiterr.Newf("extra-routes[%d]: 不支持的方法 %q（get/post/put/delete/patch）", i, r.Method)
		}
		if r.Path == "" || !strings.HasPrefix(r.Path, "/") {
			return ekiterr.Newf("extra-routes[%d]: path 必须以 / 开头（%q）", i, r.Path)
		}
		key := method + " " + r.Path
		if seen[key] {
			return ekiterr.Newf("extra-routes: 重复的 %s 补录条目", key)
		}
		seen[key] = true

		op := g.extraOperation(r)
		pi := g.doc.Paths.Get(r.Path)
		switch method {
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

// extraOperation 由补录条目构造 Operation。
func (g *generator) extraOperation(r ExtraRoute) *Operation {
	op := &Operation{
		Summary:     r.Summary,
		Description: r.Description,
		Responses:   NewResponses(),
	}
	if r.Tag != "" {
		op.Tags = []string{r.Tag}
	}

	desc := r.Summary
	if desc == "" {
		desc = "OK"
	}
	resp := &Response{Description: desc}
	switch {
	case r.Plain:
		// 探活形态：text/plain string，非统一包络。
		mt := NewMediaTypes()
		mt.Set("text/plain", &MediaType{Schema: &Schema{Type: "string"}})
		resp.Content = mt
	case r.Data != nil:
		// 包络回显形态：data 内联 schema（与 -envelope-fields 同口径）。
		mt := NewMediaTypes()
		mt.Set("application/json", &MediaType{Schema: buildEnvelope(r.dataSchema(), g.opts.EnvelopeFields)})
		resp.Content = mt
	}
	op.Responses.Set("200", resp)

	// 附加响应（code→描述，按数字升序保证逐字节稳定）。
	codes := make([]string, 0, len(r.Responses))
	for c := range r.Responses {
		codes = append(codes, c)
	}
	sort.Slice(codes, func(i, j int) bool {
		ci, ei := strconv.Atoi(codes[i])
		cj, ej := strconv.Atoi(codes[j])
		if ei == nil && ej == nil {
			return ci < cj
		}
		return codes[i] < codes[j]
	})
	for _, c := range codes {
		op.Responses.Set(c, &Response{Description: r.Responses[c]})
	}

	// security：nil=跟随全局 -auth；显式空=security: []；非空=列出的 scheme。
	switch {
	case r.Security == nil:
		if g.opts.AuthScheme != "" {
			op.Security = &SecurityReqs{Schemes: []string{g.opts.AuthScheme}}
		}
	case len(*r.Security) == 0:
		op.Security = &SecurityReqs{}
	default:
		var schemes []string
		for _, m := range *r.Security {
			for name := range m {
				schemes = append(schemes, name)
			}
		}
		sort.Strings(schemes)
		if len(schemes) > 0 {
			op.Security = &SecurityReqs{Schemes: schemes}
		}
	}
	return op
}

// dataSchema 把补录条目的 data 字段表转成对象 schema（键排序保证确定性）。
func (r ExtraRoute) dataSchema() *Schema {
	keys := make([]string, 0, len(r.Data))
	for k := range r.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	p := NewProperties()
	for _, k := range keys {
		s := r.Data[k]
		if s == nil {
			s = &Schema{Type: "string"}
		}
		p.Set(k, s)
	}
	return &Schema{Type: "object", Properties: p}
}

// isHTTPVerb 判断 method 是否受支持（与 gen.go 的 httpVerbs 同集合）。
func isHTTPVerb(m string) bool {
	for _, v := range httpVerbs {
		if v == m {
			return true
		}
	}
	return false
}

// applyErrorResponses 给操作追加统一错误响应模板（#281）。
// 免鉴权路由（api.noauth / -auth-exempt）跳过 401——无 token 可失效；
// IDL 已显式声明同码响应时不覆盖（显式优先）。
func (g *generator) applyErrorResponses(op *Operation, noAuth bool) {
	for _, er := range g.opts.ErrorResponses {
		if er.Code == 401 && noAuth {
			continue
		}
		code := strconv.Itoa(er.Code)
		if _, exists := op.Responses.Get(code); exists {
			continue
		}
		op.Responses.Set(code, &Response{Description: er.Description})
	}
}
