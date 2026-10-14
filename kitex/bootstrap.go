package kitex

import (
	"context"
	"runtime/debug"

	"github.com/yi-nology/ekit-core/lifecycle"
	"github.com/yi-nology/ekit-observability/observability/logx"
	"github.com/cloudwego/kitex/server"
)

// srvLog 是 server 生命周期专用 logger；治理配置日志见 govLog（component=kitex-gov）。
var srvLog = logx.Delegating("kitex")

// RunServer 将 Kitex Server 注册到 ekit 生命周期管理（bootstrap.App 满足
// lifecycle.App 接口；transport 适配层只依赖 lifecycle 契约，不依赖 bootstrap）。
//
// 该函数会：
//  1. 通过 app.AddResource 注册 svr.Stop 作为关闭回调（优雅关闭时自动调用）。
//  2. 通过 app.GoBackground 在后台启动 svr.Run()（阻塞直到 server 退出）。
//  3. svr.Run() 返回 error 或 panic 时（netpoll 多路复用层在监听失败如端口占用时
//     直接 panic 而非返回 error）调用 app.RequestShutdown() 触发优雅关机，
//     避免进程无服务地"假活"（对称 hertz.RunServer）。
//
// # 关闭语义（对照官方 best-practice/graceful_shutdown 文档）
//
// 信号路径：SIGTERM/SIGINT（SIGHUP 未被忽略时含之）由 svr.Run() 内置 waitSignal
// 处理，直接触发 svr.Stop(context.Background())，排空上限为
// server.WithExitWaitTime（**默认 5s** — kitex 官方默认;如未显式设置,框架
// 内部用 5s,非多路复用每秒 ticker 检 active conn / 多路复用先发控制帧）。
// 同一信号也会让 bootstrap 走本函数注册的关闭回调,第二次 Stop 因 server
// 已关可能返回 ErrServerDone（kitex 0.16+ 行为）— 跟 hertz 双路关闭的良性
// 竞争同理,本函数按成功处理。
//
// ⚠️ **必须满足 K8s 时间关系**(官方文档强制约束,违反则优雅停机不保证):
//
//	client.DiscoverLatency < k8s.PreStopTime < server.MaxLatency
//	    < kitex.ExitWaitTime < k8s.TerminationGracePeriodSeconds
//
// **推荐显式设置 server.WithExitWaitTime**(尤其长耗时 RPC):保持其小于
// bootstrap 单资源关闭预算(shutdown.New 的 defaultTimeout),避免排空被
// bootstrap 兜底截断。kitex server.Stop() 忽略传入 ctx,使用自身 ExitWaitTime。
//
// 使用方式（在 bootstrap.Run 的 userFn 内）：
//
//	bootstrap.Run(cfg, func(app *bootstrap.App) error {
//	    svr := echo.NewServer(new(Impl),
//	        server.WithRegistry(kitex.NewRegistry(reg)),
//	        server.WithServiceAddr(addr.NewNetAddr("tcp", ":9090")),
//	    )
//	    kitex.RunServer(app, "kitex-echo", svr)
//
//	    h := server.New(server.WithHostPorts(":8080"))
//	    app.AddResource("hertz", func(ctx context.Context) error { return h.Shutdown(ctx) })
//	    return h.Run()
//	})
func RunServer(app lifecycle.App, name string, svr server.Server) {
	app.AddResource(name, func(_ context.Context) error {
		return svr.Stop()
	})
	app.GoBackground(name+"-run", func(_ context.Context) {
		defer func() {
			if r := recover(); r != nil {
				srvLog.Error("kitex server start panicked, requesting shutdown",
					"name", name, "panic", r, "stack", string(debug.Stack()))
				app.RequestShutdown()
			}
		}()
		if err := svr.Run(); err != nil {
			srvLog.Error("kitex server stopped with error, requesting shutdown",
				"name", name, "err", err)
			app.RequestShutdown()
		}
	})
}
