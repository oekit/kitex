# Changelog — oekit/kitex

由 ekit v0.34.0（git.enjoye.top/enjoydream/ekit，冻结维护）拆分而来。所有 notable 变更记录如下。

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