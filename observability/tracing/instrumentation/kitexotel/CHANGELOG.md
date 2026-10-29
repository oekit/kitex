# Changelog — oekit/kitex/observability/tracing/instrumentation/kitexotel

由 oekit/kitex v0.7.0 拆分而来（Kitex OpenTelemetry tracing 接入独立成模块——
obs-opentelemetry 重依赖隔离在此，kitex 根仓回归零 apache/thrift）。

## [v0.7.2] — 2026-10-29

- 跟随 kitex v0.7.2 发版（release_train 嵌套 train）

## [v0.7.0] — 2026-10-24

- 版本与根模块**锁步**（根 v0.7.0 ⇒ 嵌套 v0.7.0）：v0.1.0 撞上根模块改名前
  （旧模块路径，原文见 #44）的历史版本号，代理缓存里那几版的 `.mod` 仍声明旧
  模块路径 ⇒ Go 解析父模块前缀候选时命中脏条目即硬失败，消费方首次 `go get` 间歇性
  失败（#44）。**请用 v0.7.0 或更新的同号版本**，不要用 v0.1.0
- 依赖面澄清：本模块依赖 `kitex-contrib/obs-opentelemetry v0.3.0`，后者仍把
  `apache/thrift` 抬到 v0.16.0 ⇒ 用了本模块（tracing suite）的下游 MVS 仍会选到
  thrift v0.16.0，仍需自己的 `replace github.com/apache/thrift => github.com/apache/thrift v0.13.0`
  压住（#44）

## [v0.1.0] — 2026-10-24

- 跟随 kitex v0.7.0 发版（release_train 嵌套 train）

