package kitex

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime/debug"
	"time"

	"github.com/cloudwego/kitex/server"
	"github.com/oekit/core/lifecycle"
	"github.com/oekit/logx"
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
//	b, _ := bootstrap.New(bootstrap.WithServiceName("svc")) // + nacos/obs WithXxx 按需注入
//	b.Run(func(app *bootstrap.App) error {
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
//
// defaultReadyProbeTimeout 是 WithOnReady 启用后的默认探活超时。
const defaultReadyProbeTimeout = 5 * time.Second

// RunServerOption 配置 RunServer 的就绪回调行为。
type RunServerOption func(*runServerConfig)

type runServerConfig struct {
	onReady      func(ctx context.Context)
	probeAddr    string
	probeTimeout time.Duration
	probeEnabled bool
}

// WithOnReady 注册就绪回调：TCP 探活通过（WithReadyAddr 指定地址，默认
// 未指定时立即回调）后执行，典型用途是触发延迟注册（app.Ready()，配合
// bootstrap/nacos v0.3.0）。kitex server 无监听就绪 introspection，
// 探活目标必须显式给出。
func WithOnReady(fn func(ctx context.Context)) RunServerOption {
	return func(cfg *runServerConfig) { cfg.onReady = fn }
}

// WithReadyAddr 指定探活目标地址（host:port，即 server.WithServiceAddr 所配）。
// 设置 WithOnReady 且未指定本项时：无探活目标，onReady 在 svr.Run 启动前
// 同步执行（注册窗口毫秒级，见 doc 说明）。
func WithReadyAddr(addr string) RunServerOption {
	return func(cfg *runServerConfig) { cfg.probeAddr = addr; cfg.probeEnabled = true }
}

// WithReadyProbeTimeout 配置探活超时（默认 5s；仅 WithReadyAddr 生效）。
func WithReadyProbeTimeout(d time.Duration) RunServerOption {
	return func(cfg *runServerConfig) { cfg.probeTimeout = d }
}

// RunServer 将 Kitex Server 注册到 ekit 生命周期管理（bootstrap.App 满足
// lifecycle.App 接口；transport 适配层只依赖 lifecycle 契约，不依赖 bootstrap）。
//
// 该函数会：
//  1. 通过 app.AddResource 注册 svr.Stop 作为关闭回调（优雅关闭时自动调用）。
//  2. 通过 app.GoBackground 在后台启动 svr.Run()（阻塞直到 server 退出）。
//     设置 WithOnReady 时回调以触发延迟注册（app.Ready()）；WithReadyAddr
//     指定时先 TCP 探活（默认 5s），全部通过后才回调；探活超时按启动失败
//     处理（RequestShutdown）。
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
//	b, _ := bootstrap.New(bootstrap.WithServiceName("svc")) // + nacos/obs WithXxx 按需注入
//	b.Run(func(app *bootstrap.App) error {
//	    svr := echo.NewServer(new(Impl),
//	        server.WithRegistry(kitex.NewRegistry(reg)),
//	        server.WithServiceAddr(addr.NewNetAddr("tcp", ":9090")),
//	    )
//	    kitex.RunServer(app, "kitex-echo", svr,
//	        kitex.WithOnReady(func(ctx context.Context) { app.Ready() }),
//	        kitex.WithReadyAddr(":9090"))
//
//	    h := server.New(server.WithHostPorts(":8080"))
//	    app.AddResource("hertz", func(ctx context.Context) error { return h.Shutdown(ctx) })
//	    return h.Run()
//	})
func RunServer(app lifecycle.App, name string, svr server.Server, opts ...RunServerOption) {
	cfg := &runServerConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

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
		if cfg.onReady != nil {
			if err := probeAndReady(app, name, cfg); err != nil {
				srvLog.Error("kitex ready probe failed, requesting shutdown",
					"name", name, "err", err)
				app.RequestShutdown()
				return
			}
		}
		if err := svr.Run(); err != nil {
			srvLog.Error("kitex server stopped with error, requesting shutdown",
				"name", name, "err", err)
			app.RequestShutdown()
		}
	})
}

// probeAndReady 执行探活并回调 onReady（无探活目标时在 Run 前同步回调）。
func probeAndReady(app lifecycle.App, name string, cfg *runServerConfig) error {
	if cfg.probeAddr == "" {
		// 无 introspection 目标：Run 前同步回调（注册窗口毫秒级）
		srvLog.Info("kitex ready (no probe target, firing before Run)", "name", name)
		cfg.onReady(app.Context())
		return nil
	}

	timeout := cfg.probeTimeout
	if timeout <= 0 {
		timeout = defaultReadyProbeTimeout
	}
	deadline := time.Now().Add(timeout)
	for {
		d := net.Dialer{Timeout: 500 * time.Millisecond}
		conn, err := d.DialContext(app.Context(), "tcp", cfg.probeAddr)
		if err == nil {
			_ = conn.Close()
			srvLog.Info("kitex ready probe passed", "name", name, "addr", cfg.probeAddr)
			cfg.onReady(app.Context())
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("ready probe timeout after %s, addr=%s: %w", timeout, cfg.probeAddr, err)
		}
		select {
		case <-app.Context().Done():
			return errors.New("ready probe aborted: app shutting down")
		case <-time.After(100 * time.Millisecond):
		}
	}
}
