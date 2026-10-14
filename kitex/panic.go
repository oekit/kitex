package kitex

import (
	"context"
	"errors"

	"github.com/cloudwego/kitex/pkg/endpoint"
	"github.com/cloudwego/kitex/pkg/gofunc"
	"github.com/cloudwego/kitex/pkg/kerrors"
	"github.com/cloudwego/kitex/pkg/rpcinfo"

	"github.com/yi-nology/ekit-observability/observability/logx"
)

// panicLog 是 kitex panic 专用 logger,
// 输出 method / remote_addr + panic 详情 + stack,
// 走 logx 全局 sink(默认 std log,可由调用方 SetDefault 切换到 hlog/slog)。
var panicLog = logx.Delegating("kitex-panic")

// SetDefaultPanicHandler 注册 ekit 风格的 kitex **内部 goroutine** panic handler
// (gofunc.RecoverGoFuncWithInfo 恢复的 goroutine:ttstream/nphttp2 流式传输、
// 框架后台任务等),日志走 logx 结构化输出。
//
// ⚠️ 覆盖范围(2026-09-05 复审修正,以 kitex v0.16.3 源码为准):
//
//   - 业务 handler panic:kitex 在 server/server.go invokeHandleEndpoint 的
//     defer 里直接 recover 并转 kerrors.ErrPanic,**不经过** gofunc 的
//     panicHandler。要在统一日志里记录 handler panic,请使用
//     NewPanicLogMiddleware(官方推荐路径)。
//   - kitex 内部 goroutine panic(流式传输等):走 gofunc.SetPanicHandler,
//     本函数生效。注意这是对 kitex 默认行为的叠加——gofunc 内部仍会打
//     klog,本 handler 额外写 logx。
//
// 官方依据:https://www.cloudwego.io/zh/docs/kitex/tutorials/basic-feature/panic/
// ("业务代码使用 go 关键字创建的 goroutine 里发生的 panic,需要业务自行
// recover"——handler panic 由框架 recover,详情走 Stats().Panicked(),官方
// 文档未将 gofunc.SetPanicHandler 列为 handler panic 钩子)
//
// 用法(在 bootstrap.Run userFn 内,kitex.RunServer 之前):
//
//	bootstrap.Run(cfg, func(app *bootstrap.App) error {
//	    ekitkitex.SetDefaultPanicHandler()
//	    svr := echo.NewServer(new(Impl), ...)
//	    ekitkitex.RunServer(app, "kitex-echo", svr)
//	    ...
//	})
func SetDefaultPanicHandler() {
	gofunc.SetPanicHandler(func(info *gofunc.Info, panicErr interface{}, panicStack string) {
		panicLog.Error("kitex goroutine panic recovered",
			"remote_service", info.RemoteService,
			"remote_addr", info.RemoteAddr,
			"panic", panicErr,
			"stack", panicStack)
	})
}

// NewPanicLogMiddleware 返回记录业务 handler panic 的 server middleware。
//
// kitex 框架在 handler panic 时自动 recover 并把详情写入 rpcinfo stats
// (返回 kerrors.ErrPanic/*kerrors.DetailedError 给调用方),业务无法在
// recover 之前插桩;官方推荐的获取详情方式就是在 middleware 里检查
// rpcinfo.GetRPCInfo(ctx).Stats().Panicked()。本 middleware 做的事:
//
//   - next 返回 error 且 stats 记录了 panic → 写 logx 结构化错误日志
//     (method / remote_addr / panic 值 / err / stack),err 原样透传,
//     不改变 kitex 默认错误返回语义;
//   - 无 panic 的普通错误、无 rpcinfo 的 ctx:直接透传,零开销。
//
// 用法(与 SetDefaultPanicHandler 互补,建议一起挂):
//
//	svr := echo.NewServer(handler,
//	    server.WithMiddleware(ekitkitex.NewPanicLogMiddleware()),
//	)
//
// 官方依据:https://www.cloudwego.io/zh/docs/kitex/tutorials/basic-feature/panic/
// ("你可以在自己的中间件里检查是否发生过 panic" →
// ri := rpcinfo.GetRPCInfo(ctx); ri.Stats(); stats.Panicked())
func NewPanicLogMiddleware() endpoint.Middleware {
	return func(next endpoint.Endpoint) endpoint.Endpoint {
		return func(ctx context.Context, req, resp interface{}) (err error) {
			err = next(ctx, req, resp)
			if err == nil {
				return nil
			}
			logKitexHandlerPanic(ctx, err)
			return err
		}
	}
}

// logKitexHandlerPanic 检查 rpcinfo stats 的 panic 记录并写结构化日志。
// 仅在框架记录了 panic 时输出(普通业务错误不打 panic 日志)。
func logKitexHandlerPanic(ctx context.Context, err error) {
	ri := rpcinfo.GetRPCInfo(ctx)
	if ri == nil || ri.Stats() == nil {
		return
	}
	panicked, panicVal := ri.Stats().Panicked()
	if !panicked {
		return
	}
	stack := ""
	var de *kerrors.DetailedError
	if errors.As(err, &de) {
		stack = de.Stack()
	}
	method := ""
	if inv := ri.Invocation(); inv != nil {
		method = inv.MethodName()
	}
	remote := ""
	if to := ri.To(); to != nil {
		if addr := to.Address(); addr != nil {
			remote = addr.String()
		}
	}
	panicLog.CtxError(ctx, "kitex handler panic recovered",
		"method", method,
		"remote_addr", remote,
		"panic", panicVal,
		"err", err,
		"stack", stack)
}
