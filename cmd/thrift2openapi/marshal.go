package main

// 有序 map 的 YAML/JSON 序列化辅助：yaml.v3 移除了 v2 的 MapSlice，JSON 的
// map 也无序——统一用本文件的 yamlMapSlice / marshalOrderedJSON 保证生成
// 文档逐字节稳定（golden 测试钉住，#124）。

import (
	"bytes"
	"encoding/json"

	"gopkg.in/yaml.v3"
)

// marshalYAMLDoc 序列化文档为 YAML（2 空格缩进）。
func marshalYAMLDoc(doc *OpenAPIDoc) ([]byte, error) {
	return yaml.Marshal(doc)
}

// marshalJSONDoc 序列化文档为缩进 JSON（有序 marshaler 生效）。
func marshalJSONDoc(doc *OpenAPIDoc) ([]byte, error) {
	return json.MarshalIndent(doc, "", "  ")
}

// yamlMapSlice 是有序 string→any 映射。
type yamlMapSlice struct {
	keys []string
	vals []any
}

func (m *yamlMapSlice) set(k string, v any) {
	m.keys = append(m.keys, k)
	m.vals = append(m.vals, v)
}

// MarshalYAML 以 MappingNode 输出（保持插入序）。
// 值接收者：切片元素（yamlMapSlice 值）也能被 yaml.v3 调用。
func (m yamlMapSlice) MarshalYAML() (any, error) {
	node := &yaml.Node{Kind: yaml.MappingNode}
	for i := range m.keys {
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: m.keys[i]}
		var valNode yaml.Node
		if err := valNode.Encode(m.vals[i]); err != nil {
			return nil, err
		}
		node.Content = append(node.Content, keyNode, &valNode)
	}
	return node, nil
}

// marshalOrderedJSON 把 collect 收集的有序键值对序列化为 JSON object。
func marshalOrderedJSON(collect func(set func(k string, v any) error) error) ([]byte, error) {
	var keys []string
	vals := map[string]json.RawMessage{}
	if err := collect(func(k string, v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		keys = append(keys, k)
		vals[k] = b
		return nil
	}); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(vals[k])
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}
