// Command thrift2openapi 把 thrift IDL（api.* 注解体系）编译成 OpenAPI 3.0
// 文档（YAML 或 JSON），替换各服务手搓的 regex 解析脚本（oekit/kitex #124）。
//
// 安装：
//
//	go install github.com/oekit/kitex/cmd/thrift2openapi@latest
//
// 基本用法：
//
//	thrift2openapi -idl idl/api.thrift -o api.swagger.yaml
//	thrift2openapi -idl idl/api.thrift -o api.swagger.json -json
//
// 完整注解体系与矩阵约定（统一响应包络/免 JWT 标记/路由豁免）见 README.md。
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cloudwego/thriftgo/parser"
	"github.com/cloudwego/thriftgo/semantic"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "thrift2openapi:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("thrift2openapi", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "用法：thrift2openapi -idl <api.thrift> [-o out.yaml] [选项]\n\n")
		fs.PrintDefaults()
	}
	var (
		idlPath  = fs.String("idl", "", "thrift IDL 路径（必填，include 相对该文件解析）")
		outPath  = fs.String("o", "", "输出路径（缺省写 stdout）")
		asJSON   = fs.Bool("json", false, "输出 JSON（缺省 YAML）")
		title    = fs.String("title", "", "文档标题（缺省取第一个 service 名）")
		version  = fs.String("version", "", "文档版本（缺省 1.0.0）")
		desc     = fs.String("description", "", "文档描述")
		basePath = fs.String("base-path", "", "servers[0].url 前缀（如 https://svc.example.com/api）")
		exempt   = fs.String("exempt", "", "路由豁免表（逗号分隔）：这些路径不出现在文档（替代手搓脚本的硬编码豁免表）")
		auth     = fs.String("auth", "", "全局鉴权方案名（如 bearerAuth）：非空时全路由挂 security")
		authExmp = fs.String("auth-exempt", "", "免鉴权路由表（逗号分隔，免 JWT 标记）：文档中显式 security: []")
		envelope = fs.Bool("envelope", false, "统一响应包络 {code, msg, data}")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *idlPath == "" {
		fs.Usage()
		return fmt.Errorf("-idl 必填")
	}

	ast, err := parseIDL(*idlPath)
	if err != nil {
		return err
	}

	opts := Options{
		Title:           *title,
		Version:         *version,
		Description:     *desc,
		BasePath:        *basePath,
		ExemptPaths:     csv(*exempt),
		AuthScheme:      *auth,
		AuthExemptPaths: csv(*authExmp),
		Envelope:        *envelope,
	}

	doc, err := Generate(ast, opts)
	if err != nil {
		return err
	}

	var out []byte
	if *asJSON {
		out, err = marshalJSONDoc(doc)
	} else {
		out, err = marshalYAMLDoc(doc)
	}
	if err != nil {
		return fmt.Errorf("序列化文档: %w", err)
	}

	if *outPath == "" {
		_, err = os.Stdout.Write(out)
		return err
	}
	if dir := filepath.Dir(*outPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(*outPath, out, 0o644)
}

// csv 把逗号分隔的 flag 值拆成列表（trim 空白、丢空段）。
func csv(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseIDL 解析 IDL 并完成符号解析（填充 Type.Category/include 引用，
// 供类型映射使用）+ 语义检查。
func parseIDL(path string) (*parser.Thrift, error) {
	ast, err := parser.ParseFile(path, nil, true)
	if err != nil {
		return nil, fmt.Errorf("解析 %s: %w", path, err)
	}
	if err := semantic.ResolveSymbols(ast); err != nil {
		return nil, fmt.Errorf("符号解析 %s: %w", path, err)
	}
	if _, err := semantic.NewChecker(semantic.Options{FixWarnings: true}).CheckAll(ast); err != nil {
		return nil, fmt.Errorf("语义检查 %s: %w", path, err)
	}
	return ast, nil
}
