package kitex

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"github.com/cloudwego/kitex/server"
	"github.com/oekit/concurrency/async"
	"github.com/oekit/core/lifecycle"
	"github.com/oekit/logx"
)

// srvLog 是 server 生命周期专用 logger；治理配置日志见 govLog（component=kitex-gov）。
var srvLog = logx.Delegating("kitex")

// defaultReadyProbeTimeout 是 WithOnReady 启用后的默认探活超时。
const defaultReadyProbeTimeout = 5 * time.Second

// errProbeAborted 表示探活被**正常关停**打断（不是探活失败）。
//
// 语义拆分：**探活失败**（启动失败 ⇒ 按失败处理、请求关机）与**探活被关停打断**
// （正常终止 ⇒ 预期路径）是两件事。关停不是失败——调用方用 errors.Is 判定后
// 只记一条 Info，且不再调 app.RequestShutdown()（进程本就在关停，再请求一次
// 是多余动作；旧实现把关停记成 ERROR「ready probe failed」，值班的人会误以为
// 服务启动失败）。
//
// 该错误只由 probeAndReady 在 ctx 被取消时返回；探活超时等真正的启动失败
// 仍返回普通 error。与 hertz 侧同名哨兵对称（#48）。
var errProbeAborted = errors.New("ready probe aborted: app shutting down")

// RunServerOption 配置 RunServer 的就绪回调行为。
type RunServerOption func(*runServerConfig)

type runServerConfig struct {
	onReady func(ctx context.Context)
	// probeAddr 是 WithReadyAddr 指定的探活目标（host:port）。
	probeAddr string
	// probeTimeout 是探活超时；零值/缺省取 defaultReadyProbeTimeout。
	probeTimeout time.Duration
	// probeDisabled 表示探活被显式禁用（WithReadyProbeTimeout 传负数，
	// 与 hertz 侧同名 option 语义对齐，oekit#142）：onReady 改为在
	// svr.Run() 前同步回调，不做端口探活。
	probeDisabled bool
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
//
// ⚠️ 探活目标必须是**本 server 自己绑定**的监听地址（契约）：端口被他人
// 占用时本进程 server 必然绑定失败（kitex 监听失败 → svr.Run() 返回 error），
// 此时探活会连上对方的监听器并误判就绪 ⇒ onReady 把不属于自己的实例注册到
// 注册中心。故启动前先做端口归属预检，被占用即按启动失败处理（见 RunServer）。
func WithReadyAddr(addr string) RunServerOption {
	return func(cfg *runServerConfig) { cfg.probeAddr = addr }
}

// WithReadyProbeTimeout 配置探活超时（零值/缺省 = 默认 5s；仅 WithReadyAddr
// 生效）。设为**负数禁用探活**——与 hertz 侧同名 option 语义对齐（oekit#142，
// 双协议服务复制配置时行为不漂移）：不做端口探活，onReady 在 svr.Run() 启动前
// 同步回调。注意回调时端口可能尚未监听（kitex listener 在 svr.Run() 内部才
// 绑定），消费方在注册窗口内可能拨到未就绪实例——仅在明确接受该窗口时使用。
func WithReadyProbeTimeout(d time.Duration) RunServerOption {
	return func(cfg *runServerConfig) {
		cfg.probeTimeout = d
		cfg.probeDisabled = d < 0
	}
}

// RunServer 将 Kitex Server 注册到 ekit 生命周期管理（bootstrap.App 满足
// lifecycle.App 接口；transport 适配层只依赖 lifecycle 契约，不依赖 bootstrap）。
//
// 该函数会：
//  1. 通过 app.AddResource 注册 svr.Stop 作为关闭回调（优雅关闭时自动调用）。
//  2. 通过 app.GoBackground 在后台启动 svr.Run()（阻塞直到 server 退出）。
//     设置 WithOnReady 时回调以触发延迟注册（app.Ready()）；WithReadyAddr
//     指定时**探活与 svr.Run() 并发**（#38），端口就绪后回调；探活超时按
//     启动失败处理（RequestShutdown）。
//  3. svr.Run() 返回 error（kitex 监听失败如端口被占用即走此路径）或 panic
//     时调用 app.RequestShutdown() 触发优雅关机，避免进程无服务地"假活"
//     （对称 hertz.RunServer）。
//
// # 就绪门与启动顺序（#38）
//
// 监听端口是在 svr.Run() **内部**才绑定的（kitex server 侧 listener 创建），
// 故探活必须与 svr.Run() **并发**：串行先探活后启动必然 connection refused
// ⇒ 探活必超时 ⇒ svr.Run() 根本不会被调用 ⇒ server 从不监听，
// 「kitex.RunServer + WithOnReady 配合 bootstrap 的 Ready 实现 NNS 延迟
// 注册」这条推荐路径在生产中不可用（与 hertz #43 完全同型，hertz 侧已修）。
//
// 两条守则：
//   - 端口归属预检：启动前先自行绑定一次探活地址；已被他人占用（EADDRINUSE）
//     说明本进程 server 必然绑定失败，此时若继续探活会连上**对方**的监听器
//     并误判就绪 ⇒ 直接按启动失败处理，绝不回调 onReady。
//   - 探活期间 app 关闭或 svr.Run() 返回即中止探活（不触发 onReady）。
//
// ⚠️ TCP 可建连只近似「端口已监听」，不等于业务健康；业务级健康门请配合
// 业务自身的健康检查后再 Ready。
//
// # 关闭语义（对照官方 best-practice/graceful_shutdown 文档）
//
// 信号路径：SIGTERM/SIGINT（SIGHUP 未被忽略时含之）由 svr.Run() 内置 waitSignal
// 处理，直接触发 svr.Stop(context.Background())，排空上限为
// server.WithExitWaitTime（**默认 5s** — kitex 官方默认;如未显式设置,框架
// 内部用 5s,非多路复用每秒 ticker 检 active conn / 多路复用先发控制帧）。
// 同一信号也会让 bootstrap 走本函数注册的关闭回调,与信号路径形成双路关闭
// 的良性竞争。与 hertz 侧不同（hertz 二次 Shutdown 返回 "engine is not
// running" 需按文本吞并）,kitex v0.16.4 的 server.Stop() 以 sync.Once 包裹
// （server/server.go 的 stopped 字段）,二次调用直接返回 nil,**全仓不存在
// ErrServerDone 符号**——本 closer 无需任何已关机错误过滤,依赖该幂等行为
// （TestRunServer_StopIdempotent 钉住;kitex 若改为返回错误,该测试会先红）。
// （#121：旧注释声称「第二次 Stop 可能返回 ErrServerDone（kitex 0.16+ 行为）
// ,本函数按成功处理」,该行为契约并不存在,已修正为如实描述。）
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
		// runDone 在 svr.Run() 返回（启动失败 / 正常退出）后关闭，供探活
		// 协程做就绪复核（#120，见 probeAndReady）。
		runDone := make(chan struct{})
		defer close(runDone)
		defer func() {
			if r := recover(); r != nil {
				srvLog.Error("kitex server start panicked, requesting shutdown",
					"name", name, "panic", r, "stack", string(debug.Stack()))
				app.RequestShutdown()
			}
		}()
		if cfg.onReady != nil {
			if cfg.probeAddr == "" || cfg.probeDisabled {
				// 无探活目标，或显式禁用探活（WithReadyProbeTimeout 负数，
				// 与 hertz 对齐，oekit#142）：Run 前同步回调（注册窗口毫秒级；
				// 禁用路径下回调时端口可能尚未监听，见 WithReadyProbeTimeout）。
				srvLog.Info("kitex ready (no probe target, firing before Run)", "name", name)
				cfg.onReady(app.Context())
			} else {
				// 端口归属预检（#38）：探活目标端口若已被**他人**占用，本进程
				// server 必然绑定失败（kitex 监听失败 → svr.Run() 返回 error →
				// 按启动失败关机）。此时若继续探活，探活会连上对方的监听器并
				// 误判就绪 ⇒ onReady 把不属于自己的实例注册到注册中心。故先
				// 自己绑定一次确认端口空闲（占用则直接按启动失败处理）。
				if err := checkProbeAddrFree(app.Context(), cfg.probeAddr); err != nil {
					srvLog.Error("kitex ready probe target busy, requesting shutdown",
						"name", name, "addr", cfg.probeAddr, "err", err)
					app.RequestShutdown()
					return
				}
				// 就绪复核信号（#120；#137 系该 TOCTOU 的重报单，复审确认此修法
				// 即其建议的第一种——「探活成功后复核 server 状态」：kitex 的
				// server.Server 接口只有 Run/Stop/RegisterService/
				// GetServiceInfos，**无 IsRunning 一类的自省 API**，故以双信号
				// 复核替代）：预检 Close 与 kitex 真正 bind 之间端口仍可能被抢占
				// （TOCTOU），探活建连成功的可能是**对方**的监听器。
				//   - probeStarted：server.RegisterStartHook 注册的启动钩子——
				//     kitex 在 listener 绑定成功、进入 serving **之后**才触发
				//     （Run 内 svr.Start() 同步建 listener，失败立即走 errCh、
				//     不触发钩子），作为「已开始服务」的正向门（对称 hertz 的
				//     「只在 engine running 之后才探活」）；
				//   - runDone：svr.Run() 返回即关闭（启动失败/退出）。
				// ⚠️ RegisterStartHook 是 kitex 进程级钩子：单 kitex server 进程
				// 内语义严格（钩子触发 ⇒ 本 server 已绑定成功，端口不可能同时
				// 被他人持有）；一进程多 kitex server 时钩子可能由**其他** server
				// 的启动触发，探活后置的 runDone 复核仍会把误判窗口压缩到 bind
				// 失败返回的同量级，但无法完全消除——多 server 进程建议由调用方
				// 自建 listener 传 server.WithListener（端口归属天然无竞态）。
				probeStarted := make(chan struct{})
				var startOnce sync.Once
				server.RegisterStartHook(func() {
					startOnce.Do(func() { close(probeStarted) })
				})
				// 探活必须与 svr.Run() **并发**（#38）：监听端口是在 svr.Run()
				// 内部才绑定的，先探活后启动必然 connection refused ⇒ 探活必超时
				// ⇒ svr.Run() 根本不会被调用 ⇒ server 从不监听（对称 hertz #43）。
				// 探活协程只负责「探活 + 回调 onReady + 探活失败则按启动失败关机」，
				// svr.Run() 仍在本 goroutine 内跑（其 panic 由上面的 recover 兜住）。
				probeCtx, cancelProbe := context.WithCancel(app.Context())
				defer cancelProbe()
				async.GoSafe(func() {
					err := probeAndReady(probeCtx, app, name, cfg, probeStarted, runDone)
					if err == nil {
						return
					}
					if errors.Is(err, errProbeAborted) {
						// 正常关停打断探活（#48，与 hertz 同型）：**预期路径，
						// 不是探活失败**。只记一条 Info，不请求关机——进程正在
						// 关停，RequestShutdown 此刻是多余动作（旧实现此处打
						// ERROR「ready probe failed, requesting shutdown」，
						// 把关停误报成启动失败 ⇒ 值班误判 + 假告警）。
						srvLog.Info("kitex ready probe aborted by shutdown", "name", name)
						return
					}
					srvLog.Error("kitex ready probe failed, requesting shutdown",
						"name", name, "err", err)
					app.RequestShutdown()
				})
			}
		}
		if err := svr.Run(); err != nil {
			srvLog.Error("kitex server stopped with error, requesting shutdown",
				"name", name, "err", err)
			app.RequestShutdown()
		}
	})
}

// checkProbeAddrFree 确认探活目标端口当前可绑定（#38 端口归属预检；预检通过
// 不代表 bind 时仍空闲——Close 与 kitex bind 之间的抢占窗口由 probeAndReady
// 的两阶段复核兜底，见 #120/#137）。
//
// 契约：WithReadyAddr 指向的必须是**本 server 自己绑定**的监听地址。端口被
// 他人占用时，本进程 server 必然绑定失败（kitex 监听失败 → svr.Run() 返回
// error），而探活会连上**对方**的监听器 ⇒ 误判就绪 ⇒ onReady 把不属于自己的
// 实例注册到注册中心。故启动前先自行绑定一次：
//   - EADDRINUSE：端口已被占用 ⇒ 直接按启动失败处理（不探活、不回调 onReady）；
//   - 其他错误（如地址不可在本机绑定）：不阻断启动，交由探活自身超时判定。
func checkProbeAddrFree(ctx context.Context, addr string) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err == nil {
		_ = ln.Close()
		return nil
	}
	if errors.Is(err, syscall.EADDRINUSE) {
		return fmt.Errorf("ready probe addr %s is already in use: %w", addr, err)
	}
	return nil
}

// probeAndReady 探活目标地址，通过后执行 onReady。两阶段（#120；#137 为该
// TOCTOU 的重报单，复审确认两阶段复核已覆盖其场景，对称 hertz
// probeAndReady 的 IsRunning 门与复核）：
//
//  1. 等待「已开始服务」的正向信号：started（RegisterStartHook 触发——kitex
//     listener 绑定成功、进入 serving 之后）或 runDone（svr.Run() 已返回）。
//     端口归属预检（checkProbeAddrFree）的 Close 与 kitex 真正 bind 之间存在
//     TOCTOU 窗口：另一进程抢占端口后，裸 TCP 探活连上的是**对方**的监听器，
//     连上即回调会把不属于自己的实例注册到注册中心。单向门 + 复核后，单
//     kitex server 进程内该误判路径闭合：started 只可能由本 server 绑定成功
//     触发；端口被抢占时本 server bind 失败 ⇒ runDone 先就绪 ⇒ 按启动失败返回。
//  2. 循环 TCP dial 探活目标；建连成功后**复核 runDone 未关闭**（对称 hertz
//     探活成功后的 !IsRunning() 复核），再回调 onReady。
//
// 探活必须与 svr.Run() 并发（#38）：监听端口是在 svr.Run() 内部才绑定的，
// 串行先探活必然 connection refused、探活必超时、server 根本不会启动。
// 探活期间 ctx 取消（app 关闭或 svr.Run() 返回）即中止，不触发 onReady。
//
// **关停与失败是两件事**（#48，与 hertz 同型）：ctx 被取消 ⇒ 探活是被正常
// 关停打断（**预期路径**）⇒ 返回 errProbeAborted；调用方据此只记 Info，
// 不请求关机。旧实现把关停记成 ERROR「ready probe failed」并再次
// app.RequestShutdown() ⇒ 值班的人会误以为服务启动失败。
func probeAndReady(ctx context.Context, app lifecycle.App, name string, cfg *runServerConfig, started, runDone <-chan struct{}) error {
	timeout := cfg.probeTimeout
	if timeout <= 0 {
		timeout = defaultReadyProbeTimeout
	}
	deadline := time.Now().Add(timeout)

	// 阶段一：等正向就绪信号（或失败/关停/超时）。每个分支先复核 ctx，
	// 保证「关停优先」（#48）在信号同时就绪时仍然确定。
	select {
	case <-started:
	case <-runDone:
		if ctx.Err() != nil {
			return errProbeAborted
		}
		// server 在探活通过前退出（典型：预检后端口被抢占 ⇒ kitex bind
		// 失败 ⇒ svr.Run() 返回 error）：按启动失败处理，绝不回调 onReady。
		return fmt.Errorf("kitex server exited before ready probe passed, addr=%s", cfg.probeAddr)
	case <-ctx.Done():
		return errProbeAborted
	case <-time.After(timeout):
		if ctx.Err() != nil {
			return errProbeAborted
		}
		return fmt.Errorf("ready probe timeout after %s, addr=%s: server never started", timeout, cfg.probeAddr)
	}

	for {
		d := net.Dialer{Timeout: 500 * time.Millisecond}
		conn, err := d.DialContext(ctx, "tcp", cfg.probeAddr)
		if err == nil {
			_ = conn.Close()
			// 阶段二复核（#120）：建连成功后复核 svr.Run() 未返回——
			// 连上的是**他人**监听器而本进程 server 已启动失败时，不得回调。
			select {
			case <-runDone:
				return fmt.Errorf("ready probe connected but kitex server already exited, addr=%s", cfg.probeAddr)
			default:
			}
			if ctx.Err() != nil {
				return errProbeAborted
			}
			srvLog.Info("kitex ready probe passed", "name", name, "addr", cfg.probeAddr)
			cfg.onReady(app.Context())
			return nil
		}
		// 关停优先于超时：ctx 已取消 ⇒ 探活是被正常关停打断，不是启动失败。
		if ctx.Err() != nil {
			return errProbeAborted
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("ready probe timeout after %s, addr=%s: %w", timeout, cfg.probeAddr, err)
		}
		select {
		case <-ctx.Done():
			return errProbeAborted
		case <-time.After(100 * time.Millisecond):
		}
	}
}
