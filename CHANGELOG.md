# Changelog — oekit/kitex

由 ekit v0.34.0（git.enjoye.top/enjoydream/ekit，冻结维护）拆分而来。所有 notable 变更记录如下。

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

## [v0.1.0] — 2026-10-15

自 ekit v0.34.0（git.enjoye.top/enjoydream/ekit，冻结维护）拆分而来的初始公开版本，完成拆分稳定化：模块路径统一为 `github.com/oekit/kitex`，多轮拆分/合并收敛至当前模块边界，组织基建（共享 CI workflow、golangci v2、Dependabot）就位。