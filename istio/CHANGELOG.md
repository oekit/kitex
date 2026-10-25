# Changelog — oekit/kitex/istio

由 oekit/kitex v0.7.0 拆分而来（xDS / Proxyless / Istio 集成桥接独立成模块——
kitex-contrib/xds 重依赖隔离在此，kitex 根仓回归零 apache/thrift）。

## [v0.7.0] — 2026-10-24

- 版本与根模块**锁步**（根 v0.7.0 ⇒ 嵌套 v0.7.0）：v0.1.0 撞上根模块改名前
  （`github.com/yi-nology/ekit-kitex`）的历史版本号 ⇒ Go 解析父模块前缀候选时命中代理
  上仍缓存旧模块路径的 `.mod` 条目即硬失败，消费方首次 `go get` 间歇性失败（#44）。
  **请用 v0.7.0 或更新的同号版本**，不要用 v0.1.0

## [v0.1.0] — 2026-10-24

- 跟随 kitex v0.7.0 发版（release_train 嵌套 train）

