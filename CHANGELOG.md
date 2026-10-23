# Changelog — oekit/kitex

由 ekit v0.34.0（git.enjoye.top/enjoydream/ekit，冻结维护）拆分而来。所有 notable 变更记录如下。

## [v0.6.6] — 2026-10-23

- docs: 补 v0.3.9 / v0.3.10 发布序回退的事实声明，并补齐 v0.3.9 缺失的版本节——当前请用主线版本 v0.6.5+（#33）

## [v0.6.5] — 2026-10-22

- build(deps): logx/kitexlog 地板 v0.1.1 → v0.1.4（父模块地板与 logx v0.7.1 对齐；零代码改动）

## [v0.6.4] — 2026-10-22

- build: 矩阵依赖地板对齐——logx v0.7.0 → v0.7.1、registry v0.4.1 → v0.5.0
  并改用嵌套模块 registry/nacos v0.1.0（registry v0.5.0 起 nacos 子包迁独立模块，
  根仓不再提供该子包）；零代码改动，测试随依赖切到嵌套模块

## [v0.6.3] — 2026-10-19

- build: 矩阵内依赖地板对齐（align_floors 不动点）：bootstrap v0.6.1 → v0.6.2、concurrency v0.5.4 → v0.17.0、core v0.7.0 → v0.9.2、logx v0.6.0 → v0.7.0、nns v0.4.1 → v0.4.2、pkg v0.11.0 → v0.15.1

## [v0.6.2] — 2026-10-18

- fix(deps): metrics v0.1.7 地板对齐（零代码改动）

## [v0.6.1] — 2026-10-18

- fix(deps): 全部 oekit 依赖地板对齐最新（bootstrap v0.6.1、nns v0.4.1、registry v0.4.1、logx v0.6.0、core v0.7.0，零代码改动）

## [v0.6.0] — 2026-10-17

### Breaking
- **主包提升至模块根**：`github.com/oekit/kitex/kitex` → `github.com/oekit/kitex`、
  `.../kitex/kitex/kitexmeta` → `.../kitex/kitexmeta`（仅移动目录与导入路径，零逻辑变化）

### Added（go-kratos 对齐）
- `transportx` 新子包：Kitex ctx → core/transport 适配（metainfo 后端；
  ctx 盒子指针承载原地写语义，写仅影响链内读取、线上透传需调用前构造 ctx）
- `chainmw` 新子包：core/chain 栈接入 kitex——`ToKitex`（resp/next 经 ctx 桥接，
  出参回填与错误语义与直连一致）、`ServerContext`（metainfo 元数据注入）
- `RunServer` 变参扩展：`WithOnReady`/`WithReadyAddr`/`WithReadyProbeTimeout`
  （kitex 无监听就绪 introspection，探活目标必须显式给出；未指定地址时 onReady
  在 Run 前同步执行，注册窗口毫秒级）

### Changed
- 依赖升级：registry v0.4.0 / nns v0.4.0 / core v0.7.0（前两者包提升至模块根）

## [v0.5.6] — 2026-10-17

- bootstrap 地板 v0.5.2 → v0.5.3；nns 地板 v0.3.4 → v0.3.5

## [v0.5.5] — 2026-10-17

- pkg 地板 v0.11.0；concurrency 地板 v0.5.4

## [v0.5.4] — 2026-10-17

- fix(deps): pkg 地板 v0.9.3 → v0.10.0（拆分版，零代码改动）；concurrency 地板 v0.5.2 → v0.5.3

## [v0.5.3] — 2026-10-17

- fix(lifecycle): suite_test 迁移 logx/shutdown → core/lifecycle（SA1019）；fix(deps): bootstrap 地板 v0.5.1 → v0.5.2

## [v0.5.2] — 2026-10-17

- fix(deps): 内部依赖地板全量对齐最新——bootstrap v0.5.1、concurrency v0.5.2、core v0.6.0、logx v0.5.0（kitexlog v0.1.1）、metrics v0.1.6、nns v0.3.4、registry v0.3.1、pkg v0.9.3；go 维持 1.26.0（kitex/OTel 生态要求）

## [v0.5.1] — 2026-10-17

- bootstrap 结构重置对齐（v0.5.0 起包提升至模块根）：`kitex/kitex/suite_test.go` 的 import 由旧路径 `github.com/oekit/bootstrap/bootstrap` 改为模块根；依赖地板 bootstrap v0.4.1 → v0.5.0（旧版本 tag 已随结构重置清空）

## [v0.5.0] — 2026-10-16

### Added
- 收编 `github.com/oekit/istio` 微库为本仓子包 `istio/`：Kitex xDS 动态熔断
  （Proxyless 对接 Istio，`InitXDS`/`NewXDSClientOption`），新增依赖
  `github.com/kitex-contrib/xds v0.4.2`；原独立微库同步退役，迁移方式：
  `github.com/oekit/istio` → `github.com/oekit/kitex/istio`（包名不变）

## [v0.4.1] — 2026-10-16

- 结构与文档收口：go.sum 发布哈希对齐、README 定稿
  （模块路径维持 `github.com/oekit/kitex`，`kitex/` 核心子包、package kitex）

## [v0.3.10] — 2026-10-16

- **refactor**：klog→logx 日志桥接（`kitex.NewLogger`）迁至
  [oekit/logx](https://github.com/oekit/logx) 的 `kitexlog` 子包；本包保留
  `kitex.NewLogger` **Deprecated 别名**（委托 `kitexlog.NewLogger`，行为完全一致，
  含 hlog 后端递归 panic 守卫与 caller-skip），未来 major 版本移除。零行为变更，
  只需要日志桥接的用户可改依赖 `github.com/oekit/logx/kitexlog` 以摆脱重型 kitex 模块
- **build**：`github.com/oekit/logx` 依赖升至 v0.2.0，并新增
  `github.com/oekit/logx/kitexlog v0.1.0`——logx 已拆分为嵌套多模块仓，
  `kitexlog` 成为目标模块（import 路径不变）；两 tag（`v0.2.0`/`kitexlog/v0.1.0`）
  推送后 CI 方可拉取，本地开发经 go.work replace 解析

> **ℹ️ 本版本的 tag 发布时刻晚于同线的 `v0.4.1`**（`v0.3.10` 的 tag 于
> **2026-10-16 08:39:16 +0800** 推送，而 `v0.4.1` 的 tag 早在 **10-15 16:48:56**）
> ——**版本号在发布时间线上回退**。本版本内容属主线序列（见上），但远端
> 「按发布时刻排序」与「按版本号排序」不一致。事实声明见
> [oekit/kitex#33](https://github.com/oekit/kitex/issues/33)；跨仓门禁口径见
> [oekit/matrix-tools#88](https://github.com/oekit/matrix-tools/issues/88)。

## [v0.3.9] — 2026-10-15

- build: 最低 Go 版本 1.26（依赖相应调整；CI/lint 同步）——对应提交 `97617cb`
  「build: 最低 Go 版本降至 1.26（依赖相应调整；CI/lint 同步）」

> **⚠️ 本版本的 tag 发布于 2026-10-15 19:32:06 +0800，晚于同线的 `v0.4.1`
> （10-15 16:48:56）2h43m** ——**版本号在发布时间线上回退**。
> 本版本此前**没有 CHANGELOG 版本节**（本节为 2026-10-23 补齐）：tag 早已发布，
> GitHub Release 在，Go module proxy 上可解析 ⇒ **保留不删**（删 tag / 重推 =
> 已缓存消费方集体中毒）。
> **当前请使用主线版本**（`v0.6.5+`）。注意 `v0.6.0` 起「主包提升至模块根」
> （`github.com/oekit/kitex/kitex` → `github.com/oekit/kitex`），本版本仍是旧的
> 双层布局。事实声明见 [oekit/kitex#33](https://github.com/oekit/kitex/issues/33)；
> 跨仓门禁口径见 [oekit/matrix-tools#88](https://github.com/oekit/matrix-tools/issues/88)。

## [v0.1.0] — 2026-10-15

自 ekit v0.34.0（git.enjoye.top/enjoydream/ekit，冻结维护）拆分而来的初始公开版本，完成拆分稳定化：模块路径统一为 `github.com/oekit/kitex`，多轮拆分/合并收敛至当前模块边界，组织基建（共享 CI workflow、golangci v2、Dependabot）就位。