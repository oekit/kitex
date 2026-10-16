# Changelog — oekit/kitex

由 ekit v0.34.0（git.enjoye.top/enjoydream/ekit，冻结维护）拆分而来。所有 notable 变更记录如下。




## [v0.4.1] — 2026-10-16

- **回退 v0.4.0 的更名**：恢复模块名 `github.com/oekit/kitex` 与原目录结构
  （`kitex/` 核心子包、package kitex；kitexmeta 回到 `kitex/` 下层）。
  v0.4.0（kitexx 路径）发布数小时内回退，无存量消费方

## [v0.3.11] — 2026-10-16

- ⚠️ 旧路径最后版本：模块已更名为 github.com/oekit/kitexx（v0.4.0 起使用新路径）；
  本版本代码与 v0.3.10 完全一致，仅 README 挂弃用通告

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

## [v0.3.8] — 2026-10-15

- v0.3.8

## [v0.3.7] — 2026-10-15

- v0.3.7

## [v0.3.6] — 2026-10-15

- v0.3.6

## [v0.3.5] — 2026-10-15

- v0.3.5

## [v0.3.4] — 2026-10-15

- v0.3.4

## [v0.3.3] — 2026-10-15

- v0.3.3

## [v0.3.2] — 2026-10-15

- v0.3.2

## [v0.3.1] — 2026-10-15

- v0.3.1

## [v0.3.0] — 2026-10-15

- v0.3.0

## [v0.2.1] — 2026-10-15

- v0.2.1

## [v0.2.0] — 2026-10-15

- v0.2.0

## [v0.1.0] — 2026-10-15

- v0.1.0
