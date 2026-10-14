# ekit-kitex

> Kitex RPC 生态——日志桥接、治理配置热更新、生命周期、BizError 桥接、可观测性与 Istio。

本仓库是 [ekit](https://git.enjoye.top/enjoydream/ekit)（`git.enjoye.top/enjoydream/ekit` v0.34.0）
按框架耦合分层拆分出的独立 Go module，属于 **yi-nology** ekit 子项目矩阵。

## 安装

```bash
go get github.com/yi-nology/ekit-kitex@v0.1.0
```

## 包清单

- `kitex/` — klog→logx 桥接、NewServerTracingSuite、GovernanceConfig（Nacos 热更 retry/timeout/CB/limiter）、RunServer、WithBizErrorBridge、PanicLogMiddleware
- `kitex/kitexmeta/` — trace_id + metainfo 跨 RPC 透传（client middleware）
- `registry/kitex/`、`nns/kitex/` — Kitex 注册发现适配器
- `observability/metrics/instrumentation/kitexprom/` — 方法级 RPC 指标（monitor-prometheus 桥接）
- `observability/tracing/instrumentation/kitexotel/` — OTel server/client suite
- `observability/istio/` — Istio Proxyless XDS
- `examples/` — kitex echo 示例（client/server + IDL + 治理配置）

## 依赖关系

子项目矩阵与依赖方向（自底向上）：

```
ekit-core                      纯工具层（零内部依赖）
  └─ ekit-observability        日志/追踪/指标门面
       └─ ekit-registry        注册发现 + 配置
            ├─ ekit-data       存储层
            ├─ ekit-network    网络客户端
            │    └─ ekit-nns   服务治理客户端
            │         └─ ekit-bootstrap  应用引导
            ├─ ekit-hertz      Hertz 生态
            ├─ ekit-kitex      Kitex 生态
            └─ ekit-workflow   流程编排
```

依赖：[ekit-core](https://github.com/yi-nology/ekit-core)、[ekit-observability](https://github.com/yi-nology/ekit-observability)、[ekit-registry](https://github.com/yi-nology/ekit-registry)、[ekit-nns](https://github.com/yi-nology/ekit-nns)（examples 另引 ekit-bootstrap）。

## 与 ekit 单体仓库的关系

- 代码与 ekit v0.34.0 等价，仅 module 路径从 `git.enjoye.top/enjoydream/ekit/...` 改为 `github.com/yi-nology/ekit-kitex/...`
- 后续演进以各子仓库独立发版（SemVer）；ekit 单体仓库保留完整历史与架构文档（docs/PLATFORM_CONTRACT.md）
- 开发规范（命名/错误处理/并发安全/日志门面）沿用 ekit AGENTS.md 约定

## License

见 [LICENSE](LICENSE)
