module github.com/oekit/kitex/istio

go 1.26.0

// 嵌套模块：Kitex 的 xDS / Proxyless / Istio 集成桥接（xDS 资源管理器 +
// 动态熔断）。
//
// 为什么要独立成模块（实测，#37）：
// kitex-contrib/xds v0.4.2 的 go.mod 把 apache/thrift 抬到 v0.16.0（且
// v0.3.0～v0.4.2 全部版本都抬），而 apache/thrift v0.16 的协议方法多了
// context.Context 首参——留在根模块里时，**任何 import github.com/oekit/kitex
// 的下游**都会被 MVS 抬到 thrift v0.16.0，按 thrift 0.13 生成的 Thrift 代码
// 大面积编译不过。降版本解不开（xds 早期版本对应的 kitex 生态停在 v0.7.x），
// 只有隔离一条路。
//
// 隔离方向：父←子。本模块依赖 kitex-contrib/xds，父仓根模块不依赖它。
require (
	github.com/cloudwego/kitex v0.16.4
	github.com/kitex-contrib/xds v0.4.2
)

require (
	github.com/bytedance/gopkg v0.1.4 // indirect
	github.com/bytedance/sonic v1.15.0 // indirect
	github.com/bytedance/sonic/loader v0.5.0 // indirect
	github.com/cenkalti/backoff/v4 v4.1.0 // indirect
	github.com/census-instrumentation/opencensus-proto v0.4.1 // indirect
	github.com/cloudwego/base64x v0.1.6 // indirect
	github.com/cloudwego/configmanager v0.2.3 // indirect
	github.com/cloudwego/dynamicgo v0.9.2 // indirect
	github.com/cloudwego/fastpb v0.0.6 // indirect
	github.com/cloudwego/frugal v0.3.1 // indirect
	github.com/cloudwego/gopkg v0.2.1 // indirect
	github.com/cloudwego/localsession v0.2.2 // indirect
	github.com/cloudwego/netpoll v0.7.2 // indirect
	github.com/cloudwego/runtimex v0.1.2 // indirect
	github.com/cloudwego/thriftgo v0.4.5 // indirect
	github.com/cncf/xds/go v0.0.0-20230607035331-e9ce68804cb4 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/envoyproxy/go-control-plane v0.11.1 // indirect
	github.com/envoyproxy/protoc-gen-validate v1.0.1 // indirect
	github.com/fatih/structtag v1.2.0 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/google/pprof v0.0.0-20240727154555-813a5fbdbec8 // indirect
	github.com/iancoleman/strcase v0.2.0 // indirect
	github.com/jhump/protoreflect v1.8.2 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/klauspost/cpuid/v2 v2.2.9 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/stretchr/testify v1.10.0 // indirect
	github.com/tidwall/gjson v1.17.3 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	github.com/twitchyliquid64/golang-asm v0.15.1 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	golang.org/x/arch v0.14.0 // indirect
	golang.org/x/net v0.24.0 // indirect
	golang.org/x/sync v0.11.0 // indirect
	golang.org/x/sys v0.30.0 // indirect
	golang.org/x/text v0.14.0 // indirect
	google.golang.org/genproto v0.0.0-20230526203410-71b5a4ffd15e // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20230526203410-71b5a4ffd15e // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20230526203410-71b5a4ffd15e // indirect
	google.golang.org/grpc v1.56.3 // indirect
	google.golang.org/protobuf v1.33.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
