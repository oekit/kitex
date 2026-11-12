package kitex

// #200 护栏：AGENTS.md 日志节硬规则——错误字段名统一用 logx.ErrKey 常量，
// 禁止手写 "err"/"error" 字面量（两矩阵落同一 Loki/ES）。本测试用 go/parser
// 扫描根模块全部非测试、非生成源码，任何把字面量 "err"/"error" 作为调用参数
// （即 logx kv 对的字段名位）的写法直接 fail，防止红线随新代码回流。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestLogxErrKey_NoHandwrittenLiteral 扫描根模块（不含嵌套模块——各自独立
// 发版、由各自仓规约束；不含 kitex_gen 生成物），钉住 logx.ErrKey 红线。
func TestLogxErrKey_NoHandwrittenLiteral(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file for repo root")
	}
	root := filepath.Dir(thisFile)

	var violations []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "kitex_gen" || name == "testdata" {
				return filepath.SkipDir
			}
			// 嵌套模块（自持 go.mod、独立发版）不在根模块仓规扫描面内；
			// 根目录的 go.mod 属于本模块，继续。
			if rel != "." {
				if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			for _, arg := range call.Args {
				lit, ok := arg.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				v := strings.Trim(lit.Value, `"`)
				if v == "err" || v == "error" {
					pos := fset.Position(lit.Pos())
					violations = append(violations,
						pos.String()+"：手写错误字段名 "+lit.Value+"，改用 logx.ErrKey（AGENTS.md 日志节红线，#200）")
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}
	for _, v := range violations {
		t.Error(v)
	}
}
