# thrift2openapi

thrift IDL（`api.*` 注解体系）→ OpenAPI 3.0 文档（YAML/JSON）官方生成器，替换各服务手搓的 regex 解析脚本（`gen_swagger.py` 一类，oekit/kitex #124）。

独立嵌套模块（依赖隔离同 `istio/`、`kitexotel/`：thrift 解析器不进根模块依赖面）。

## 安装与基本用法

```bash
go install github.com/oekit/kitex/cmd/thrift2openapi@latest

thrift2openapi -idl idl/api.thrift -o api.swagger.yaml
thrift2openapi -idl idl/api.thrift -o api.swagger.json -json
```

完整参数：

| 参数 | 说明 |
|---|---|
| `-idl` | thrift IDL 路径（必填；include 相对该文件解析） |
| `-o` | 输出路径（缺省写 stdout） |
| `-json` | 输出 JSON（缺省 YAML） |
| `-title` / `-version` / `-description` | info 元信息（title 缺省取第一个 service 名） |
| `-base-path` | `servers[0].url`（如 `https://svc.example.com/api`） |
| `-exempt` | **路由豁免表**（逗号分隔）：这些路径不出现在文档 |
| `-auth` | 全局鉴权方案名（如 `bearerAuth`）：输出 securitySchemes 并给全部路由挂 security |
| `-auth-exempt` | **免 JWT 标记**（逗号分隔）：文档中显式 `security: []` |
| `-envelope` | 统一响应包络 `{code, msg, data}` |
| `-envelope-fields` | **包络字段定制**（逗号分隔、有序）：如 `code,data,message,trace_id`——名为 `data` 的字段承载业务载荷，其余按名取类型（code→integer、msg/message→string、trace_id→string）；空=缺省 `code,msg,data` |
| `-error-responses` | **统一错误响应模板**（`code=描述` 逗号分隔）：如 `400=参数错误,401=未登录或 token 失效`；免鉴权路由（`api.noauth` / `-auth-exempt`）跳过 401 |
| `-extra-routes` | **IDL 外路由补录表**（YAML/JSON 文件）：把手写路由（探活/共享模块/网关身份回显等）补进文档 |

## 补录表（`-extra-routes`）

每个服务都有有意留在 IDL 外的路由（组织级共享模块注册的探活、读网关注入头回显
身份等）——`-exempt`/`api.skip` 只有「豁免」没有「补录」，本参数把消费方后处理
脚本里的手工注入表收编进工具。文件形态（YAML，JSON 亦可）：

```yaml
- method: GET
  path: /ping
  tag: Infra
  summary: Ping
  plain: true          # text/plain string 响应（非统一包络）
  security: []         # 空 = security: []（免鉴权）
- method: GET
  path: /api/v2/user/info
  tag: Identity
  summary: UserInfo
  data:                # 包络 data 的内联 schema
    userId: {type: string}
    username: {type: string}
  security: [{bearerAuth: []}]
```

- `plain` / `data` 覆盖两类消费方实测形态（非包络探活 vs 包络回显）；两者皆无时只输出 200 描述。
- `security` 缺省跟随全局 `-auth`；显式空列表输出 `security: []`。
- `responses` 可附加额外响应（`"400": 参数错误`）。
- 补录在豁免删除之后应用：允许与 `-exempt` 并存、允许覆盖同名 method+path 的内联生成操作。
- 非法条目（坏方法/坏 path/重复）构造期报错，不静默丢。

## 注解体系

方法级：

```thrift
service DemoService {
    // 发送通知
    // 第二行注释会合并进接口描述
    SendResp sendNotice(1: SendReq req) (api.post = "/api/v1/send")

    // 免 JWT 标记（配合 -auth 使用）
    void login(1: LoginReq req) (api.post = "/api/v1/login", api.noauth = "true")

    // 路由存在但明确不进文档（IDL 内单源豁免，替代硬编码豁免表）
    void internalOp(1: Req req) (api.post = "/api/v1/internal", api.skip = "true")
}
```

- `api.get/post/put/delete/patch`：路由（手搓脚本只支持 get/post，这里全支持）；hertz 风格 `:id` 自动转 `{id}`。
- `api.noauth`：免 JWT 标记（文档中显式 `security: []`，区分「无要求」与「未标记」）。
- `api.skip`：该路由不进文档。

字段级（struct 定义内）：

| 注解 | OpenAPI 映射 |
|---|---|
| `api.query` / `api.path` / `api.header` / `api.cookie` | 对应位置的 parameter（注解值可覆盖参数名） |
| `api.body` / `api.json` | application/json 请求体属性（`api.body="*"`：整个 struct 作为请求体） |
| `api.form` / `api.file` | multipart/form-data 属性 |
| `api.none` | 不进文档 |
| （未标注） | GET → query 参数；其余 verb → JSON body 属性 |

`//` 注释 → 参数/接口/schema 描述；字段默认值 → `default`；thrift 类型映射：`i32/i64→integer`、`double→number`、`list/set→array`（set 加 `uniqueItems`）、`map→object+additionalProperties`、enum → `integer+enum`（线上按 i32 传输）；struct/enum 一律 `$ref` 进 `components/schemas`（include 引用保留 `module.Type` 前缀防跨 IDL 同名冲突；自引用安全）。

## 与手搓脚本的差异（实证清单）

手搓 `gen_swagger.py` 的破损面 → 本生成器的对应处理：

| 手搓脚本破损面 | thrift2openapi |
|---|---|
| regex 解析（`FIELD_RE`/`METHOD_RE`）：多行字段/默认值/嵌套注解即破 | [thriftgo](https://github.com/cloudwego/thriftgo) 真 parser（kitex 官方工具链同源），全量吃下 |
| 自研 YAML emitter | yaml.v3 + 全结构有序序列化（输出逐字节稳定，golden 测试钉住） |
| `MANUAL_ROUTES` 硬编码豁免表，与 router.go 注册知识双写 | `-exempt` 显式配置 + `api.skip` IDL 注解（单源） |
| `$ref` 条件分支被下一行覆写（死代码 bug） | $ref 统一经 `components/schemas` 收敛，golden 钉住 |
| verb 只支持 get/post | get/post/put/delete/patch |

取舍说明：issue 建议的「代码扫描（反射/AST 注册点）消双写」需要感知各服务的路由注册代码形态，本轮未做——`-exempt` 配置 + `api.skip` 注解已消除「ID 外知识」的双写（IDL 内路由的豁免归 IDL，ID 外路由的豁免归生成配置，两者都可进 CI）；代码扫描留作演进（PR 说明见 oekit/kitex #124）。

## 防漂移

- 生成输出**确定性**（有序结构体序列化），CI 中 `thrift2openapi -idl api.thrift | diff - api.swagger.yaml` 即可做文档漂移门禁；
- 本模块自带 golden 端到端测试（`testdata/demo.thrift` → 期望 YAML/JSON 逐字节对比），任何生成语义变化都会以 diff 暴露，防止生成器自身腐烂。

## 测试

```bash
cd cmd/thrift2openapi
go test -race -count=1 ./...
```
