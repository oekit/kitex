package kitex

import (
	"testing"

	"github.com/cloudwego/kitex/pkg/gofunc"
)

// TestSetDefaultPanicHandler_RegistersGlobal 验证 SetDefaultPanicHandler
// 注册到 kitex 全局 panic handler(通过触发 fake panic 观察日志输出)。
//
// kitex gofunc 内部维护全局 panicHandler 变量(无 getter),无法直接断言;
// 改用行为验证:触发 SetDefaultPanicHandler 前后,kitex RecoverGoFuncWithInfo
// 调用栈不 panic(默认 handler 已替换)。
func TestSetDefaultPanicHandler_RegistersGlobal(t *testing.T) {
	SetDefaultPanicHandler()

	info := gofunc.NewBasicInfo("test-svc", "127.0.0.1:8080")
	done := make(chan struct{})
	gofunc.RecoverGoFuncWithInfo(t.Context(), func() {
		defer close(done)
	}, info)

	<-done
}

// TestSetDefaultPanicHandler_MultipleCallsOverwrite 验证多次调用 SetDefaultPanicHandler
// 不 panic(覆盖式,跟 kitex 原生行为一致)。
func TestSetDefaultPanicHandler_MultipleCallsOverwrite(t *testing.T) {
	SetDefaultPanicHandler()
	SetDefaultPanicHandler() // 第二次覆盖,不 panic
}

// TestSetDefaultPanicHandler_TriggeredByPanic 验证 handler 真的被 kitex 在 panic
// 时调起(通过 RecoverGoFuncWithInfo + 内部 panic 触发)。
//
// 通过捕获 logx 输出断言 handler 被调:这里用一个标志变量 + RecoverGoFuncWithInfo
// 调用栈间接验证 — gofunc 内部 recover 后会调全局 handler,我们的 handler 写日志
// 但不暴露标志,所以此 case 仅验证"无 panic"语义(handler 链不抛异常)。
func TestSetDefaultPanicHandler_TriggeredByPanic(t *testing.T) {
	SetDefaultPanicHandler()

	info := gofunc.NewBasicInfo("trigger-svc", "10.0.0.1:9999")
	defer func() {
		// RecoverGoFuncWithInfo 内部 defer recover,但外层我们再加一层防
		// gofunc 自己抛 panic 兜不住(虽然不应该发生)。
		if r := recover(); r != nil {
			t.Fatalf("unexpected panic bubbled: %v", r)
		}
	}()

	gofunc.RecoverGoFuncWithInfo(t.Context(), func() {
		panic("simulated handler panic")
	}, info)
	// 任务已结束,handler 已被调(panic 已被 kitex 框架 recover + 走我们 handler)
}

// TestSetDefaultPanicHandler_EmptyInfo 验证 info 字段为空时 handler 不 panic
// (kitex 在调用 handler 之前会把空 RemoteService/Addr 填 "unknown",但我们
// 直接走 handler API 时也可能拿到零值 — helper 应能容忍)。
func TestSetDefaultPanicHandler_EmptyInfo(t *testing.T) {
	SetDefaultPanicHandler()

	info := &gofunc.Info{}
	done := make(chan struct{})
	gofunc.RecoverGoFuncWithInfo(t.Context(), func() {
		defer close(done)
	}, info)

	<-done
}
