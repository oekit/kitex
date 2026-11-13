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

// orderedTable 是「插入序 map」的泛型基座（#187）：OpenAPI 文档的 map 型字段
// （paths/responses/content/schemas/securitySchemes/properties）要求生成结果
// 逐字节稳定（map 遍历序随机，golden 测试无法钉），故一律收编为本类型——
// Set/Delete/Len/MarshalYAML/MarshalJSON 五件套只实现这一份，各具名表类型
// 以薄包装嵌入复用。
type orderedTable[T any] struct {
	items map[string]T
	keys  []string
}

func newOrderedTable[T any]() orderedTable[T] {
	return orderedTable[T]{items: map[string]T{}}
}

// Get 取表项；不存在返回零值与 false。
func (t *orderedTable[T]) Get(key string) (T, bool) {
	v, ok := t.items[key]
	return v, ok
}

// Set 写入表项（保持首次插入序）。
func (t *orderedTable[T]) Set(key string, val T) {
	if t.items == nil {
		t.items = map[string]T{}
	}
	if _, ok := t.items[key]; !ok {
		t.keys = append(t.keys, key)
	}
	t.items[key] = val
}

// Delete 移除表项（保持其余项插入序）。
func (t *orderedTable[T]) Delete(key string) {
	if _, ok := t.items[key]; !ok {
		return
	}
	delete(t.items, key)
	for i, k := range t.keys {
		if k == key {
			t.keys = append(t.keys[:i], t.keys[i+1:]...)
			break
		}
	}
}

// Len 表项数量。
func (t *orderedTable[T]) Len() int { return len(t.keys) }

// MarshalYAML 按插入序输出 map。
func (t *orderedTable[T]) MarshalYAML() (any, error) {
	out := &yamlMapSlice{}
	for _, k := range t.keys {
		out.set(k, t.items[k])
	}
	return out, nil
}

// MarshalJSON 按插入序输出 object。
func (t *orderedTable[T]) MarshalJSON() ([]byte, error) {
	return marshalOrderedJSON(func(set func(k string, v any) error) error {
		for _, k := range t.keys {
			if err := set(k, t.items[k]); err != nil {
				return err
			}
		}
		return nil
	})
}

// PathItems 是有序路径表。
type PathItems struct {
	orderedTable[*PathItem]
}

func NewPathItems() *PathItems { return &PathItems{orderedTable: newOrderedTable[*PathItem]()} }

// Get 取路径项，不存在时创建（保持首次插入序）——「Get 即挂载」是路径表
// 独有语义，故保留为本类型方法（#187）。
func (p *PathItems) Get(path string) *PathItem {
	if pi, ok := p.items[path]; ok {
		return pi
	}
	pi := &PathItem{}
	p.Set(path, pi)
	return pi
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
	orderedTable[*Response]
}

func NewResponses() *Responses { return &Responses{orderedTable: newOrderedTable[*Response]()} }

// Response 单个响应。
type Response struct {
	Description string      `yaml:"description" json:"description"`
	Content     *MediaTypes `yaml:"content,omitempty" json:"content,omitempty"`
}

// MediaTypes 有序 content 表（application/json → schema）。
type MediaTypes struct {
	orderedTable[*MediaType]
}

func NewMediaTypes() *MediaTypes { return &MediaTypes{orderedTable: newOrderedTable[*MediaType]()} }

// MediaType 媒体类型载荷。
type MediaType struct {
	Schema *Schema `yaml:"schema" json:"schema"`
}

// SecurityReqs 安全要求（全局鉴权标记 / 免鉴权置空）。
type SecurityReqs struct {
	Schemes []string `yaml:"-" json:"-"`
}

// MarshalYAML 非空时输出 [{scheme: []}]，空（免鉴权）时输出 []。
func (s *SecurityReqs) MarshalYAML() (any, error) {
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
	orderedTable[*Schema]
}

func NewSchemaTable() *SchemaTable { return &SchemaTable{orderedTable: newOrderedTable[*Schema]()} }

// Get 取 schema（不存在返回 nil）。
func (t *SchemaTable) Get(name string) *Schema { return t.items[name] }

// SecuritySchemeTable 有序 securitySchemes 表。
type SecuritySchemeTable struct {
	orderedTable[*SecurityScheme]
}

// NewSecuritySchemeTable 构造有序 securitySchemes 表。
func NewSecuritySchemeTable() *SecuritySchemeTable {
	return &SecuritySchemeTable{orderedTable: newOrderedTable[*SecurityScheme]()}
}

// Get 取安全方案（不存在返回 nil）。
func (t *SecuritySchemeTable) Get(name string) *SecurityScheme { return t.items[name] }

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
	orderedTable[*Schema]
}

func NewProperties() *Properties { return &Properties{orderedTable: newOrderedTable[*Schema]()} }

// Get 取属性（不存在返回 nil）。
func (p *Properties) Get(name string) *Schema { return p.items[name] }

// refSchema 构造 $ref schema。
func refSchema(name string) *Schema {
	return &Schema{Ref: fmt.Sprintf("#/components/schemas/%s", name)}
}
