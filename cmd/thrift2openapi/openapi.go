// Package main 实现 thrift2openapi：thrift IDL（api.* 注解体系）→ OpenAPI 3.0
// 文档生成器（oekit/kitex #124）。
//
// 本文件定义 OpenAPI 3.0 文档的最小类型层。刻意不引第三方 OpenAPI 库：
// 输出面的结构由本包显式拥有，schema/路径一律用**有序**切片而非 map，
// 保证同一 IDL 的生成结果逐字节稳定（golden 测试可钉）。
package main

import "fmt"

// OpenAPIDoc 是 OpenAPI 3.0.3 文档的骨架。
type OpenAPIDoc struct {
	OpenAPI    string      `yaml:"openapi" json:"openapi"`
	Info       *Info       `yaml:"info" json:"info"`
	Servers    []*Server   `yaml:"servers,omitempty" json:"servers,omitempty"`
	Paths      *PathItems  `yaml:"paths" json:"paths"`
	Components *Components `yaml:"components,omitempty" json:"components,omitempty"`
}

// Info 文档元信息。
type Info struct {
	Title          string `yaml:"title" json:"title"`
	Description    string `yaml:"description,omitempty" json:"description,omitempty"`
	Version        string `yaml:"version" json:"version"`
	TermsOfService string `yaml:"termsOfService,omitempty" json:"termsOfService,omitempty"`
}

// Server 服务地址（-base-path 注入）。
type Server struct {
	URL string `yaml:"url" json:"url"`
}

// PathItems 是有序路径表：map 会让 YAML key 随机序，golden 测试无法钉。
type PathItems struct {
	items map[string]*PathItem
	keys  []string
}

func NewPathItems() *PathItems { return &PathItems{items: map[string]*PathItem{}} }

// Get 取路径项，不存在时创建（保持首次插入序）。
func (p *PathItems) Get(path string) *PathItem {
	if pi, ok := p.items[path]; ok {
		return pi
	}
	pi := &PathItem{}
	p.items[path] = pi
	p.keys = append(p.keys, path)
	return pi
}

// Delete 移除路径项（路由豁免）。
func (p *PathItems) Delete(path string) {
	if _, ok := p.items[path]; ok {
		delete(p.items, path)
		for i, k := range p.keys {
			if k == path {
				p.keys = append(p.keys[:i], p.keys[i+1:]...)
				break
			}
		}
	}
}

// Len 路径数量。
func (p *PathItems) Len() int { return len(p.keys) }

// MarshalYAML 按插入序输出 map。
func (p *PathItems) MarshalYAML() (interface{}, error) {
	out := &yamlMapSlice{}
	for _, k := range p.keys {
		out.set(k, p.items[k])
	}
	return out, nil
}

// MarshalJSON 按插入序输出 object。
func (p *PathItems) MarshalJSON() ([]byte, error) {
	return marshalOrderedJSON(func(set func(k string, v any) error) error {
		for _, k := range p.keys {
			if err := set(k, p.items[k]); err != nil {
				return err
			}
		}
		return nil
	})
}

// PathItem 单路径下的方法集。
type PathItem struct {
	Get    *Operation `yaml:"get,omitempty" json:"get,omitempty"`
	Post   *Operation `yaml:"post,omitempty" json:"post,omitempty"`
	Put    *Operation `yaml:"put,omitempty" json:"put,omitempty"`
	Delete *Operation `yaml:"delete,omitempty" json:"delete,omitempty"`
	Patch  *Operation `yaml:"patch,omitempty" json:"patch,omitempty"`
}

// Operation 单个 API 操作。
type Operation struct {
	Summary     string        `yaml:"summary,omitempty" json:"summary,omitempty"`
	Description string        `yaml:"description,omitempty" json:"description,omitempty"`
	Tags        []string      `yaml:"tags,omitempty" json:"tags,omitempty"`
	OperationID string        `yaml:"operationId,omitempty" json:"operationId,omitempty"`
	Parameters  []*Param      `yaml:"parameters,omitempty" json:"parameters,omitempty"`
	RequestBody *RequestBody  `yaml:"requestBody,omitempty" json:"requestBody,omitempty"`
	Responses   *Responses    `yaml:"responses" json:"responses"`
	Security    *SecurityReqs `yaml:"security,omitempty" json:"security,omitempty"`
}

// Param 参数（query/path/header/cookie）。
type Param struct {
	Name        string  `yaml:"name" json:"name"`
	In          string  `yaml:"in" json:"in"`
	Description string  `yaml:"description,omitempty" json:"description,omitempty"`
	Required    bool    `yaml:"required" json:"required"`
	Schema      *Schema `yaml:"schema,omitempty" json:"schema,omitempty"`
}

// RequestBody 请求体。
type RequestBody struct {
	Description string      `yaml:"description,omitempty" json:"description,omitempty"`
	Required    bool        `yaml:"required,omitempty" json:"required,omitempty"`
	Content     *MediaTypes `yaml:"content" json:"content"`
}

// Responses 有序响应表（通常只有 200 与 default）。
type Responses struct {
	items map[string]*Response
	keys  []string
}

func NewResponses() *Responses { return &Responses{items: map[string]*Response{}} }

// Set 写入响应（保持首次插入序）。
func (r *Responses) Set(code string, resp *Response) {
	if _, ok := r.items[code]; !ok {
		r.keys = append(r.keys, code)
	}
	r.items[code] = resp
}

// MarshalYAML 按插入序输出 map。
func (r *Responses) MarshalYAML() (interface{}, error) {
	out := &yamlMapSlice{}
	for _, k := range r.keys {
		out.set(k, r.items[k])
	}
	return out, nil
}

// MarshalJSON 按插入序输出 object。
func (r *Responses) MarshalJSON() ([]byte, error) {
	return marshalOrderedJSON(func(set func(k string, v any) error) error {
		for _, k := range r.keys {
			if err := set(k, r.items[k]); err != nil {
				return err
			}
		}
		return nil
	})
}

// Response 单个响应。
type Response struct {
	Description string      `yaml:"description" json:"description"`
	Content     *MediaTypes `yaml:"content,omitempty" json:"content,omitempty"`
}

// MediaTypes 有序 content 表（application/json → schema）。
type MediaTypes struct {
	items map[string]*MediaType
	keys  []string
}

func NewMediaTypes() *MediaTypes { return &MediaTypes{items: map[string]*MediaType{}} }

// Set 写入媒体类型。
func (m *MediaTypes) Set(name string, mt *MediaType) {
	if _, ok := m.items[name]; !ok {
		m.keys = append(m.keys, name)
	}
	m.items[name] = mt
}

// MarshalYAML 按插入序输出 map。
func (m *MediaTypes) MarshalYAML() (interface{}, error) {
	out := &yamlMapSlice{}
	for _, k := range m.keys {
		out.set(k, m.items[k])
	}
	return out, nil
}

// MarshalJSON 按插入序输出 object。
func (m *MediaTypes) MarshalJSON() ([]byte, error) {
	return marshalOrderedJSON(func(set func(k string, v any) error) error {
		for _, k := range m.keys {
			if err := set(k, m.items[k]); err != nil {
				return err
			}
		}
		return nil
	})
}

// MediaType 媒体类型载荷。
type MediaType struct {
	Schema *Schema `yaml:"schema" json:"schema"`
}

// SecurityReqs 安全要求（全局鉴权标记 / 免鉴权置空）。
type SecurityReqs struct {
	Schemes []string `yaml:"-" json:"-"`
}

// MarshalYAML 非空时输出 [{scheme: []}]，空（免鉴权）时输出 []。
func (s *SecurityReqs) MarshalYAML() (interface{}, error) {
	out := []yamlMapSlice{}
	for _, scheme := range s.Schemes {
		m := yamlMapSlice{}
		m.set(scheme, []string{})
		out = append(out, m)
	}
	return out, nil
}

// MarshalJSON 与 MarshalYAML 同语义。
func (s *SecurityReqs) MarshalJSON() ([]byte, error) {
	parts := []string{}
	for _, scheme := range s.Schemes {
		k, err := marshalOrderedJSON(func(set func(k string, v any) error) error {
			return set(scheme, []string{})
		})
		if err != nil {
			return nil, err
		}
		parts = append(parts, string(k))
	}
	if len(parts) == 0 {
		return []byte("[]"), nil
	}
	out := []byte("[")
	for i, p := range parts {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, p...)
	}
	return append(out, ']'), nil
}

// Components 组件表（schemas + securitySchemes）。
type Components struct {
	Schemas         *SchemaTable         `yaml:"schemas,omitempty" json:"schemas,omitempty"`
	SecuritySchemes *SecuritySchemeTable `yaml:"securitySchemes,omitempty" json:"securitySchemes,omitempty"`
}

// SchemaTable 有序 schema 表。
type SchemaTable struct {
	items map[string]*Schema
	keys  []string
}

func NewSchemaTable() *SchemaTable { return &SchemaTable{items: map[string]*Schema{}} }

// Get 取 schema（不存在返回 nil）。
func (t *SchemaTable) Get(name string) *Schema { return t.items[name] }

// Set 写入 schema（保持首次插入序）。
func (t *SchemaTable) Set(name string, s *Schema) {
	if _, ok := t.items[name]; !ok {
		t.keys = append(t.keys, name)
	}
	t.items[name] = s
}

// MarshalYAML 按插入序输出 map。
func (t *SchemaTable) MarshalYAML() (interface{}, error) {
	out := &yamlMapSlice{}
	for _, k := range t.keys {
		out.set(k, t.items[k])
	}
	return out, nil
}

// MarshalJSON 按插入序输出 object。
func (t *SchemaTable) MarshalJSON() ([]byte, error) {
	return marshalOrderedJSON(func(set func(k string, v any) error) error {
		for _, k := range t.keys {
			if err := set(k, t.items[k]); err != nil {
				return err
			}
		}
		return nil
	})
}

// SecuritySchemeTable 有序 securitySchemes 表。
type SecuritySchemeTable struct {
	items map[string]*SecurityScheme
	keys  []string
}

// NewSecuritySchemeTable 构造有序 securitySchemes 表。
func NewSecuritySchemeTable() *SecuritySchemeTable {
	return &SecuritySchemeTable{items: map[string]*SecurityScheme{}}
}

// Get 取安全方案（不存在返回 nil）。
func (t *SecuritySchemeTable) Get(name string) *SecurityScheme { return t.items[name] }

// Set 写入安全方案。
func (t *SecuritySchemeTable) Set(name string, s *SecurityScheme) {
	if _, ok := t.items[name]; !ok {
		t.keys = append(t.keys, name)
	}
	t.items[name] = s
}

// MarshalYAML 按插入序输出 map。
func (t *SecuritySchemeTable) MarshalYAML() (interface{}, error) {
	out := &yamlMapSlice{}
	for _, k := range t.keys {
		out.set(k, t.items[k])
	}
	return out, nil
}

// MarshalJSON 按插入序输出 object。
func (t *SecuritySchemeTable) MarshalJSON() ([]byte, error) {
	return marshalOrderedJSON(func(set func(k string, v any) error) error {
		for _, k := range t.keys {
			if err := set(k, t.items[k]); err != nil {
				return err
			}
		}
		return nil
	})
}

// SecurityScheme bearer 鉴权方案。
type SecurityScheme struct {
	Type         string `yaml:"type" json:"type"`
	Description  string `yaml:"description,omitempty" json:"description,omitempty"`
	Scheme       string `yaml:"scheme,omitempty" json:"scheme,omitempty"`
	BearerFormat string `yaml:"bearerFormat,omitempty" json:"bearerFormat,omitempty"`
}

// Schema 是 OpenAPI 3.0 Schema Object 的生成侧子集。
type Schema struct {
	Ref                  string      `yaml:"$ref,omitempty" json:"$ref,omitempty"`
	Type                 string      `yaml:"type,omitempty" json:"type,omitempty"`
	Format               string      `yaml:"format,omitempty" json:"format,omitempty"`
	Description          string      `yaml:"description,omitempty" json:"description,omitempty"`
	Nullable             bool        `yaml:"nullable,omitempty" json:"nullable,omitempty"`
	UniqueItems          bool        `yaml:"uniqueItems,omitempty" json:"uniqueItems,omitempty"`
	Enum                 []any       `yaml:"enum,omitempty" json:"enum,omitempty"`
	Items                *Schema     `yaml:"items,omitempty" json:"items,omitempty"`
	Properties           *Properties `yaml:"properties,omitempty" json:"properties,omitempty"`
	AdditionalProperties *Schema     `yaml:"additionalProperties,omitempty" json:"additionalProperties,omitempty"`
	Required             []string    `yaml:"required,omitempty" json:"required,omitempty"`
	Default              any         `yaml:"default,omitempty" json:"default,omitempty"`
}

// Properties 有序属性表（与 struct 字段序一致）。
type Properties struct {
	items map[string]*Schema
	keys  []string
}

func NewProperties() *Properties { return &Properties{items: map[string]*Schema{}} }

// Set 写入属性。
func (p *Properties) Set(name string, s *Schema) {
	if _, ok := p.items[name]; !ok {
		p.keys = append(p.keys, name)
	}
	p.items[name] = s
}

// Get 取属性。
func (p *Properties) Get(name string) *Schema { return p.items[name] }

// Len 属性数量。
func (p *Properties) Len() int { return len(p.keys) }

// MarshalYAML 按插入序输出 map。
func (p *Properties) MarshalYAML() (interface{}, error) {
	out := &yamlMapSlice{}
	for _, k := range p.keys {
		out.set(k, p.items[k])
	}
	return out, nil
}

// MarshalJSON 按插入序输出 object。
func (p *Properties) MarshalJSON() ([]byte, error) {
	return marshalOrderedJSON(func(set func(k string, v any) error) error {
		for _, k := range p.keys {
			if err := set(k, p.items[k]); err != nil {
				return err
			}
		}
		return nil
	})
}

// refSchema 构造 $ref schema。
func refSchema(name string) *Schema {
	return &Schema{Ref: fmt.Sprintf("#/components/schemas/%s", name)}
}
