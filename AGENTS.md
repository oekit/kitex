# AGENTS.md — oekit/kitex

> Kitex 生态——日志桥接、治理热更、生命周期、BizError 桥接、kitexotel、istio（xDS 动态熔断，原 istio 微库收编）；RunServer 变参就绪门（WithOnReady/WithReadyAddr/WithReadyProbeTimeout）+ chainmw.ToKitex（core/chain.Middleware → kitex 中间件，与 hertz 共享 middleware/chainmw 实现）+ transportx
> 当前版本：`v0.7.13` · module：`github.com/oekit/kitex` · org：[oekit = Open Easy Kit](https://oekit.github.io)

## 本仓包含
`./`、`chainmw/`、`internal/e2e/kitex_gen/kitex/echo/`、`internal/e2e/kitex_gen/kitex/echo/echoservice/`、`kitexmeta/`、`nns/kitex/`、`observability/metrics/instrumentation/kitexprom/`、`registry/kitex/`、`transportx/`

### 嵌套模块（独立发版 · 依赖隔离，v0.7.0 起）
- `observability/tracing/instrumentation/kitexotel/` → module `github.com/oekit/kitex/observability/tracing/instrumentation/kitexotel`
- `istio/` → module `github.com/oekit/kitex/istio`
- `cmd/thrift2openapi/` → module `github.com/oekit/kitex/cmd/thrift2openapi`（thrift→OpenAPI 生成器 CLI，thriftgo 解析器依赖隔离在子模块内，#124）

**为什么拆**：obs-opentelemetry / xds 的 go.mod 把 `apache/thrift` 抬到 v0.16.0，
而 thrift v0.16 的协议方法多了 `context.Context` 首参 ⇒ 留在根模块时，**任何
`import github.com/oekit/kitex` 的下游**都被 MVS 抬到 thrift v0.16.0，按 thrift 0.13
生成的 Thrift 代码大面积编译不过（#37）。降版本解不开（obs v0.2.4+ / xds v0.3.0+ 全部
抬 thrift v0.16.0，且旧版对应的 kitex 生态停在 v0.5–v0.7）⇒ 只有隔离一条路。

**红线**：根模块**不得**再引 obs-opentelemetry / xds / apache-thrift——包括**测试**
（根仓测试引 kitexotel 会把重依赖拖回根模块的依赖面）。需要这两个能力的下游
显式 `go get` 对应嵌套模块（import 路径不变）。


## 硬规则速览（全矩阵适用）

### 分层与依赖
- 依赖严格自底向上，禁止反向依赖；本仓的矩阵内依赖见 README「矩阵总览」表
- 微工具集统一在 `github.com/oekit/pkg`；本仓新增基础工具前先确认 pkg 里没有
- `pkg` 是**零第三方依赖仓**（go.mod 零 require，仅标准库，CI 强制）——需要三方能力的基础工具拆独立仓（先例：kitx）

### 错误处理
- 业务错误（需 BizCode/HTTPCode 双轨、可被 As/Is 判定）统一用
  `github.com/oekit/pkg/errors` 的 BizError（命名工厂 NewNotFoundError 等）
- 普通错误包装允许 stdlib：`fmt.Errorf("...: %w", err)` / `errors.New`
- BizError 载体同一进程只此一套：混入其他 BizError 实现会打断 As/Is 判定（Not Found 变 500）
- handler 层统一 `mw.Fail(ctx, c, err)` 出口；所有 error 禁止 `_` 忽略

### 日志
- 门面 `github.com/oekit/logx`：业务服务 `logx.WithService("svc")`；
  **SDK/基础包必须 `logx.Delegating("组件名")` 懒解析**（禁止 Default()/WithService 声明包级 logger）
- kv 结构化风格为默认；`Ctx*` 方法自动携带 trace_id；禁止 printf 风格 `logx.Infof` 作为主要手段
- 错误字段名统一用 `logx.ErrKey` 常量（值 "err"），禁止手写 "err"/"error" 字面量（两矩阵落同一 Loki/ES）
- 后端经 `logx.SetDefault` 替换；`LOG_LEVEL` 环境变量统一生效

### 并发安全
- 禁止裸 `go func`：用 `github.com/oekit/concurrency/async` 的 GoSafe 系列（panic 被 recover）
- 共享状态 mutex / atomic 保护；包级全局可变状态用 `atomic.Pointer[T]`

### 生命周期
- 组件 Close 统一 `Close(ctx) error` 签名 + sync.Once 幂等
- 应用骨架用 `github.com/oekit/bootstrap/bootstrap.Run`；**userFn 返回即服务结束，
  长驻服务必须 `<-app.Context().Done()` 阻塞**

### Option 与命名
- 函数选项模式 `XxxOption`（禁止裸 `Option`，多包 co-import 冲突）
- 导出符号 PascalCase + godoc 注释；包文档放 doc.go
- Option 非法值总则：① 构造期非法值 fail-fast（panic 或返回 error，同仓内保持一致）；
  ② nil 参数 = 显式回退默认值，不算非法；③ 运行期不可恢复错误显式上抛，不静默

### 超时默认值
- 探测类 ≤10s、治理类 ≤60s、后台任务 ≤10min；跨区间取值须在包文档声明理由

### 版本与发版
- SemVer；0.x 阶段 minor 允许 breaking
- **main 受分支保护，改动一律走 PR，管理员也不例外**（`enforce_admins=true`，
  直推会被 `GH006 Changes must be made through a pull request` 拒掉）
- 发版流程：建分支 → 提交 → 开 PR → CI（`Lint & Test / Lint` + `Lint & Test / Test`）
  绿 → 合入 main → **合入之后**再打 annotated tag 推远端
- tag 不走 PR（`protect-release-tags` ruleset 管的是不可删/不可改，不是审批）；
  tag 必须落在已合入 main 的 commit 上，否则 `go get` 拉到的是没进过 CI 的代码
- module 路径 = `github.com/oekit/<name>`；**改 module 路径必须同步**：
  全部 import、go.mod、README 安装行、logx caller 启发式（若涉及 logx）

### 文档
- 本仓 README 的「矩阵总览」表由脚本生成，结构变化后需重新生成
- 历史完整规范见 ekit 单体仓（git.enjoye.top/enjoydream/ekit，冻结维护）的 AGENTS.md 与 docs/

## 本仓范围

