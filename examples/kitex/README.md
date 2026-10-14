# Kitex 集成示例

演示如何使用 ekit 的 `registry/kitex` 和 `kitex` 包将 CloudWeGo Kitex RPC 框架
与 ekit 的服务注册发现、可观测性、治理配置能力集成。

## 目录结构

```
examples/kitex/
├── idl/echo.thrift              # Thrift IDL（示例用 Thrift，Protobuf 同理）
├── kitex_gen/                   # kitex 工具真实生成的代码（v0.27.2 起，可直接真跑 RPC）
├── server/main.go               # 服务端：注册到 Nacos + 可观测性 + 治理配置
├── client/main.go               # 客户端：服务发现 + 可观测性 + 调用
├── configs/                     # 示例 Nacos 治理配置（JSON）
│   ├── retry.json               # 重试策略
│   ├── rpc_timeout.json         # RPC 超时
│   └── limit.json               # 限流（服务端）
└── README.md
```

## 前置条件

1. Nacos 服务运行中（默认 `127.0.0.1:8848`）
2. （可选）安装 Kitex 代码生成工具：
   ```bash
   go install github.com/cloudwego/kitex/tool/cmd/kitex@latest
   ```

## 快速开始

### 1. 代码生成（可选，示例已含真实生成代码）

```bash
cd examples/kitex
kitex -module git.enjoye.top/enjoydream/ekit -service kitex.echo.EchoService idl/echo.thrift
```

### 2. 上传治理配置到 Nacos

将 `configs/` 下的 JSON 文件上传到 Nacos 配置中心：

| 文件 | dataId | Group |
|------|--------|-------|
| `retry.json` | `kitex-client.kitex-echo.retry` | `DEFAULT_GROUP` |
| `rpc_timeout.json` | `kitex-client.kitex-echo.rpc_timeout` | `DEFAULT_GROUP` |
| `limit.json` | `kitex-echo.limit` | `DEFAULT_GROUP` |

### 3. 启动服务端

```bash
go run ./server/main.go
```

服务端会：
- 通过 `registry/kitex.NewRegistry` 注册到 Nacos
- 通过 `kitex.NewServerTracingSuite` 接入 OpenTelemetry 链路追踪
- 通过 `kitex.RunServer` 接入 bootstrap 生命周期管理

### 4. 启动客户端

```bash
go run ./client/main.go
```

客户端会：
- 通过 `registry/kitex.NewResolver` 从 Nacos 发现服务实例
- 通过 `kitex.NewClientTracingSuite` 注入 trace context 透传
- 调用 `Echo` 方法并打印响应

## 核心集成点

### 服务注册（registry/kitex）

```go
// ekit Registrar → Kitex Registry
reg, _ := nacosimpl.NewRegistrar(namingCli)
svr := echo.NewServer(handler,
    server.WithRegistry(registrykitex.NewRegistry(reg)),
)
```

### 服务发现（registry/kitex）

```go
// ekit Discoverer → Kitex Resolver
disc, _ := nacosimpl.NewDiscoverer(namingCli)
cli, _ := echo.NewClient("target",
    client.WithResolver(registrykitex.NewResolver(disc)),
)
```

### 可观测性（kitex）

```go
// 日志桥接
klog.SetLogger(kitex.NewLogger(logx.WithService("my-svc")))

// OpenTelemetry tracing
svr := echo.NewServer(handler, server.WithSuite(kitex.NewServerTracingSuite()))
cli, _ := echo.NewClient("target", client.WithSuite(kitex.NewClientTracingSuite()))
```

### 治理配置热更新（kitex）

```go
gov, _ := kitex.NewGovernanceConfig(configCli)

// 客户端：retry + timeout + circuit breaker
suite := kitex.NewClientSuite("client-svc", "server-svc", gov,
    kitex.WithRetry(),
    kitex.WithRPCTimeout(),
    kitex.WithCircuitBreaker(),
)
cli, _ := echo.NewClient("server-svc", client.WithSuite(suite))

// 服务端：limiter
srvSuite := kitex.NewServerSuite("server-svc", gov, kitex.WithLimiter())
svr := echo.NewServer(handler, server.WithSuite(srvSuite))
```

### 生命周期管理（kitex）

```go
bootstrap.Run(cfg, func(app *bootstrap.App) error {
    svr := echo.NewServer(handler, ...)
    kitex.RunServer(app, "kitex-echo", svr)  // 自动 Stop + 后台 Run
    select {}  // 阻塞
})
```

## 治理配置格式

### retry（dataId: `{client}.{server}.retry`）

```json
{
  "*": {
    "enable": true,
    "type": 0,
    "failure_policy": {
      "stop_policy": {"max_retry_times": 3, "max_duration_ms": 2000},
      "backoff_policy": {"backoff_type": "fixed", "cfg_items": {"fix_ms": 50}}
    }
  }
}
```

### rpc_timeout（dataId: `{client}.{server}.rpc_timeout`）

```json
{
  "*":    {"conn_timeout_ms": 100, "rpc_timeout_ms": 3000},
  "Echo": {"conn_timeout_ms": 50,  "rpc_timeout_ms": 1000}
}
```

### circuit_break（dataId: `{client}.{server}.circuit_break`）

```json
{
  "Echo": {"enable": true, "err_rate": 0.3, "min_sample": 100}
}
```

### limit（dataId: `{server}.limit`）

```json
{"connection_limit": 100, "qps_limit": 2000}
```
