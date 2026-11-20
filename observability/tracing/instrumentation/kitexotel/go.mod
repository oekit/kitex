module github.com/oekit/kitex/observability/tracing/instrumentation/kitexotel

go 1.26.0

// 嵌套模块：Kitex 的 OpenTelemetry tracing 接入（client/server suite +
// metainfo 透传）。
//
// 为什么要独立成模块（实测，#37）：
// kitex-contrib/obs-opentelemetry v0.3.0 的 go.mod 把 apache/thrift 抬到
// v0.16.0，而 apache/thrift v0.16 的协议方法多了 context.Context 首参——
// 留在根模块里时，**任何 import github.com/oekit/kitex 的下游**都会被 MVS
// 抬到 thrift v0.16.0，按 thrift 0.13 生成的 Thrift 代码大面积编译不过
// （not enough arguments in call to iprot.ReadStructBegin）。
//
// 隔离方向：父←子。本模块依赖 obs-opentelemetry，父仓根模块不依赖它，
// 故只想要 Kitex 基础能力的消费方不会被拖进 thrift 大版本。
//
// require 的 kitex 版本必须 ≥ v0.7.0：v0.6.x 的 kitex 仓里**还带着**
// kitexotel/ 这个子包目录，若同时存在两个模块提供
// `github.com/oekit/kitex/observability/tracing/instrumentation/kitexotel`，
// 消费方会撞上 ambiguous import。v0.7.0 起该目录已随嵌套模块移出根仓。
require (
	github.com/bytedance/gopkg v0.1.4
	github.com/cloudwego/kitex v0.16.4
	github.com/kitex-contrib/obs-opentelemetry v0.3.0
	github.com/oekit/kitex v0.8.7
	go.opentelemetry.io/otel v1.47.0
	go.opentelemetry.io/otel/sdk v1.47.0
	go.opentelemetry.io/otel/trace v1.47.0
)

require (
	github.com/bytedance/sonic v1.15.0 // indirect
	github.com/bytedance/sonic/loader v0.5.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/cloudwego/base64x v0.1.6 // indirect
	github.com/cloudwego/configmanager v0.2.3 // indirect
	github.com/cloudwego/dynamicgo v0.9.2 // indirect
	github.com/cloudwego/fastpb v0.0.6 // indirect
	github.com/cloudwego/frugal v0.3.1 // indirect
	github.com/cloudwego/gopkg v0.2.1 // indirect
	github.com/cloudwego/localsession v0.2.2 // indirect
	github.com/cloudwego/netpoll v0.7.5 // indirect
	github.com/cloudwego/runtimex v0.1.2 // indirect
	github.com/cloudwego/thriftgo v0.4.5 // indirect
	github.com/fatih/structtag v1.2.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/google/pprof v0.0.0-20240727154555-813a5fbdbec8 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/iancoleman/strcase v0.3.0 // indirect
	github.com/jhump/protoreflect v1.8.2 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/klauspost/cpuid/v2 v2.2.10 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/oekit/logx v0.12.0 // indirect
	github.com/oekit/tracing v0.4.8 // indirect
	github.com/stretchr/testify v1.12.1 // indirect
	github.com/tidwall/gjson v1.18.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	github.com/twitchyliquid64/golang-asm v0.15.1 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/log v1.47.0 // indirect
	go.opentelemetry.io/otel/metric v1.47.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/arch v0.14.0 // indirect
	golang.org/x/net v0.59.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	google.golang.org/genproto v0.0.0-20240227224415-6ceb2ff114de // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260928230214-8a89bd6388cc // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// 本地开发用：父仓源码就在上溯四层。发布后 replace 对消费方无效（只有主模块
// 的 replace 生效），消费方走上面 require 的已发布版本。
replace github.com/oekit/kitex => ../../../..
