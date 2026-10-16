# ⚠️ 已改名：oekit/kitex → oekit/kitexx

本模块已更名为 [oekit/kitexx](https://github.com/oekit/kitexx)（v0.4.0 起）——
避免与 cloudwego/kitex 命名混淆，与 redisx/gormx/hertzx/logx 的 `-x` 系对齐。

- 旧路径 `github.com/oekit/kitex` 经 GitHub 重定向仍可拉取 ≤ v0.3.11
- 结构：原 `kitex/` 核心子包上收为根包（package kitexx），nns/registry/observability/kitexmeta
  子树随新路径

```bash
go get github.com/oekit/kitex@v0.4.1
```
