package istio

import (
	"strings"
	"testing"
)

// TestWithXDSServerAddress_OptionType 验证 Option 构造(编译期检查)。
func TestWithXDSServerAddress_OptionType(t *testing.T) {
	_ = WithXDSServerAddress("istiod.istio-system:15010")
	// Option 是 struct(F func),不能用 == nil,改用 _ = 触发构造调用
}

// TestWithXDSServerConfig_OptionType 验证带 config Option 构造(编译期检查)。
func TestWithXDSServerConfig_OptionType(t *testing.T) {
	cfg := &XDSServerConfig{
		SvrAddr: "istiod.istio-system:15010",
	}
	_ = WithXDSServerConfig(cfg)
}

// TestNewXDSClientOption_PanicsWithoutInit 行为级(2026-09-06 复审修复:
// 原测试 defer recover 吞 panic 恒过,形同虚设):未 InitXDS 时上游 xds
// 资源管理器为 nil,xdssuite.NewCircuitBreaker 必然 panic(panic 内容含
// "resource manager has not been initialized")— 把"未初始化会炸"钉成
// 明确契约,而不是靠吞掉让任何行为都算通过。
func TestNewXDSClientOption_PanicsWithoutInit(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("NewXDSClientOption without InitXDS must panic (manager not initialized)")
		}
		t.Logf("expected panic: %v", r)
	}()
	_ = NewXDSClientOption()
}

// TestXDSOption_TypeAlias 验证 type alias 编译期兼容。
func TestXDSOption_TypeAlias(t *testing.T) {
	var _ = WithXDSServerAddress("x")
	var _ = WithXDSServerConfig(&XDSServerConfig{})
}

// TestXDSServerConfig_TypeAlias 验证 type alias 编译期兼容。
func TestXDSServerConfig_TypeAlias(t *testing.T) {
	var _ = &XDSServerConfig{}
}

// TestInitXDS_MissingEnvErrors 行为级(2026-09-06 复审):InitXDS 构造期
// 要求 K8s bootstrap 环境变量(POD_NAMESPACE/POD_NAME/INSTANCE_IP),缺失时
// 返回明确错误 — 这是单测环境唯一可稳定断言的错误路径。
//
// 注:不测"istiod 不可达"的路径 — 实测上游 xds.Init 在 bootstrap 通过后
// 会对不可达 istiod 阻塞重试(非立即返回),单测不可承载;连接语义见
// xds.go InitXDS 文档。
func TestInitXDS_MissingEnvErrors(t *testing.T) {
	err := InitXDS(WithXDSServerAddress("127.0.0.1:1"))
	if err == nil {
		t.Fatal("InitXDS without K8s bootstrap env should return error")
	}
	if !strings.Contains(err.Error(), "env") {
		t.Errorf("error should mention missing env, got: %v", err)
	}
}
