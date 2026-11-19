package kitex

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/kitex/pkg/serviceinfo"
	"github.com/cloudwego/kitex/server"
	"github.com/oekit/concurrency/async"
	"github.com/oekit/core/lifecycle"
	echoservice "github.com/oekit/kitex/internal/e2e/kitex_gen/kitex/echo/echoservice"
	"github.com/oekit/logx"
)

// fakeServer 是 server.Server 的最小测试实现：允许测试编程 Run/Stop 的返回行为。
// 覆盖 Run/Stop/RegisterService/GetServiceInfos（RunServer 测试中不会触发后两个）。
type fakeServer struct {
	runFunc  func() error
	stopFunc func() error
	runCount int
	stopOnce sync.Once
}

func (f *fakeServer) Run() error {
	f.runCount++
	if f.runFunc != nil {
		return f.runFunc()
	}
	return nil
}

func (f *fakeServer) Stop() error {
	var err error
	f.stopOnce.Do(func() {
		if f.stopFunc != nil {
			err = f.stopFunc()
		}
	})
	return err
}

func (f *fakeServer) RegisterService(*serviceinfo.ServiceInfo, interface{}, ...server.RegisterOption) error {
	return nil
}

func (f *fakeServer) GetServiceInfos() map[string]*serviceinfo.ServiceInfo { return nil }

// fakeApp 是 lifecycle.App 的最小测试实现，记录 RequestShutdown 调用与资源。
// 模式与 hertz/bootstrap_test.go 保持一致。
type fakeApp struct {
	lifecycle.App
	ctx           context.Context
	cancel        context.CancelFunc
	requestedCh   chan struct{}
	requestedOnce sync.Once
	closers       map[string]func(ctx context.Context) error
}

func newFakeApp() *fakeApp {
	ctx, cancel := context.WithCancel(context.Background())
	return &fakeApp{
		ctx:         ctx,
		cancel:      cancel,
		requestedCh: make(chan struct{}),
		closers:     make(map[string]func(ctx context.Context) error),
	}
}

func (f *fakeApp) Context() context.Context { return f.ctx }

func (f *fakeApp) AddResource(name string, closer func(context.Context) error) {
	f.closers[name] = closer
}

func (f *fakeApp) GoBackground(_ string, fn func(ctx context.Context)) {
	async.GoSafe(func() { fn(f.ctx) })
}

func (f *fakeApp) RequestShutdown() {
	f.requestedOnce.Do(func() { close(f.requestedCh) })
}

// TestRunServer_PanicOnRun 钉住 Task 1 行为：svr.Run() 抛 panic 时，
// recover 后调用 app.RequestShutdown() 触发优雅关机，避免进程假活。
// 场景来源：netpoll 多路复用层在端口占用时直接 panic 而非返回 error。
func TestRunServer_PanicOnRun(t *testing.T) {
	fa := newFakeApp()
	svr := &fakeServer{
		runFunc: func() error {
			panic("simulated kitex Run panic (e.g. port already in use)")
		},
	}

	RunServer(fa, "panicking-kitex", svr)

	select {
	case <-fa.requestedCh:
		// RequestShutdown 被调用，符合预期
	case <-time.After(2 * time.Second):
		t.Fatal("RunServer did not call RequestShutdown after Run panic")
	}

	fa.cancel()
}

// TestRunServer_RunErrorTriggersShutdown 验证 svr.Run() 返回 error（非 panic）
// 时同样触发 RequestShutdown（与现有 h 路径对称）。
func TestRunServer_RunErrorTriggersShutdown(t *testing.T) {
	fa := newFakeApp()
	svr := &fakeServer{
		runFunc: func() error {
			return errors.New("simulated Run error")
		},
	}

	RunServer(fa, "errored-kitex", svr)

	select {
	case <-fa.requestedCh:
		// RequestShutdown 被调用，符合预期
	case <-time.After(2 * time.Second):
		t.Fatal("RunServer did not call RequestShutdown after Run error")
	}

	fa.cancel()
}

// TestRunServer_StopCloserDelegates 验证 RunServer 注册的 closer 透传到
// svr.Stop()，关闭时正确释放。
func TestRunServer_StopCloserDelegates(t *testing.T) {
	fa := newFakeApp()
	stopCalled := false
	svr := &fakeServer{
		stopFunc: func() error {
			stopCalled = true
			return nil
		},
	}

	RunServer(fa, "stop-kitex", svr)

	closer, ok := fa.closers["stop-kitex"]
	if !ok {
		t.Fatal("RunServer did not register close resource")
	}
	if err := closer(context.Background()); err != nil {
		t.Fatalf("closer returned error: %v", err)
	}
	if !stopCalled {
		t.Fatal("closer did not call svr.Stop()")
	}
}

// captureHandler 是记录 Message 的最小 slog.Handler，供 captureKitexLog
// 组装内存捕获 logger。
type captureHandler struct {
	mu   sync.Mutex
	msgs []string
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.msgs = append(h.msgs, r.Message)
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

// captureKitexLog 把 logx 默认 logger 换成内存捕获（NewSlogLogger +
// captureHandler），返回消息快照函数。srvLog 是 Delegating logger，
// SetDefault 换代后自动跟随新实现——RunServer closer 的看门狗 Warn 据此断言。
func captureKitexLog(t *testing.T) func() []string {
	t.Helper()
	h := &captureHandler{}
	old := logx.Default()
	logx.SetDefault(logx.NewSlogLogger("kitex-test", logx.WithHandler(h)))
	t.Cleanup(func() { logx.SetDefault(old) })
	return func() []string {
		h.mu.Lock()
		defer h.mu.Unlock()
		return append([]string(nil), h.msgs...)
	}
}

// waitForLog 轮询快照直到出现 want 消息（预算耗尽 → AfterFunc 异步触发，
// 需要等窗口）或超时失败。
func waitForLog(t *testing.T, snapshot func() []string, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, m := range snapshot() {
			if strings.Contains(m, want) {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected log %q within 2s, got %v", want, snapshot())
}

// TestRunServer_StopCloser_WatchdogWarnsOverBudget #299 主路径：显式配置
// 小预算，Stop 阻塞超过预算 → closer 内置看门狗打 Warn「kitex Stop 超出
// 关停预算仍未返回」，Stop 返回后 closer 正常结束（看门狗不动 Stop 本身）。
func TestRunServer_StopCloser_WatchdogWarnsOverBudget(t *testing.T) {
	snapshot := captureKitexLog(t)
	fa := newFakeApp()
	stopRelease := make(chan struct{})
	svr := &fakeServer{
		stopFunc: func() error {
			<-stopRelease
			return nil
		},
	}

	RunServer(fa, "watchdog-kitex", svr, WithStopWatchdogBudget(50*time.Millisecond))
	closer, ok := fa.closers["watchdog-kitex"]
	if !ok {
		t.Fatal("RunServer did not register close resource")
	}

	closerDone := make(chan error, 1)
	async.GoSafe(func() { closerDone <- closer(context.Background()) })
	waitForLog(t, snapshot, "kitex Stop 超出关停预算仍未返回")

	close(stopRelease)
	select {
	case err := <-closerDone:
		if err != nil {
			t.Fatalf("closer returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closer did not return after Stop released")
	}
}

// TestRunServer_StopCloser_WatchdogBudgetFromCtxDeadline #299 零配置路径：
// 未配 WithStopWatchdogBudget，closer ctx 携带短 deadline（core/lifecycle
// Manager per-job 预算 / bootstrap.AddResourceTimeout 派生 jobCtx 的形态）
// → 预算取 time.Until(deadline)。若实现误用缺省 10s，本用例 1s 内等不到
// Warn 即失败。
func TestRunServer_StopCloser_WatchdogBudgetFromCtxDeadline(t *testing.T) {
	snapshot := captureKitexLog(t)
	fa := newFakeApp()
	stopRelease := make(chan struct{})
	svr := &fakeServer{
		stopFunc: func() error {
			<-stopRelease
			return nil
		},
	}

	RunServer(fa, "watchdog-deadline-kitex", svr)
	closer, ok := fa.closers["watchdog-deadline-kitex"]
	if !ok {
		t.Fatal("RunServer did not register close resource")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	closerDone := make(chan error, 1)
	async.GoSafe(func() { closerDone <- closer(ctx) })
	waitForLog(t, snapshot, "kitex Stop 超出关停预算仍未返回")

	close(stopRelease)
	select {
	case err := <-closerDone:
		if err != nil {
			t.Fatalf("closer returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closer did not return after Stop released")
	}
}

// TestRunServer_StopCloser_NoWarnWithinBudget 预算内返回不误报：Stop 正常
// 返回时不得出现看门狗 Warn（Timer 已停表）。
func TestRunServer_StopCloser_NoWarnWithinBudget(t *testing.T) {
	snapshot := captureKitexLog(t)
	fa := newFakeApp()
	svr := &fakeServer{
		stopFunc: func() error { return nil },
	}

	RunServer(fa, "watchdog-quiet-kitex", svr, WithStopWatchdogBudget(50*time.Millisecond))
	closer, ok := fa.closers["watchdog-quiet-kitex"]
	if !ok {
		t.Fatal("RunServer did not register close resource")
	}
	if err := closer(context.Background()); err != nil {
		t.Fatalf("closer returned error: %v", err)
	}

	time.Sleep(100 * time.Millisecond) // 越过预算点，确认停表生效
	for _, m := range snapshot() {
		if strings.Contains(m, "kitex Stop 超出关停预算仍未返回") {
			t.Fatalf("watchdog warned although Stop returned within budget: %v", snapshot())
		}
	}
}

// TestRunServer_StopCloser_WatchdogBudgetInvalidFallsBack 非法值口径：预算
// 传 0/负数回退缺省（对齐 bootstrap WithShutdownTimeout #206 口径），不
// fail-fast 也不吞看门狗——负数预算下 ctx 无 deadline 时仍按缺省 10s 兜底。
func TestRunServer_StopCloser_WatchdogBudgetInvalidFallsBack(t *testing.T) {
	fa := newFakeApp()
	svr := &fakeServer{stopFunc: func() error { return nil }}
	RunServer(fa, "watchdog-invalid-kitex", svr, WithStopWatchdogBudget(-1))
	closer, ok := fa.closers["watchdog-invalid-kitex"]
	if !ok {
		t.Fatal("RunServer did not register close resource")
	}
	if err := closer(context.Background()); err != nil {
		t.Fatalf("closer returned error: %v", err)
	}
}

// TestRunServer_OnReadyNoTarget_WaitsForListener 钉住 #290：未指定探活目标
// （无 WithReadyAddr）时，onReady 必须等 listener 绑定成功（Started 门，
// RegisterStartHook）后才回调——回调发生时端口必须已在监听。
//
// 旧实现在 svr.Run() **之前**同步回调：回调时端口必然未监听，注册窗口内
// 消费方会拨到未就绪实例（对称 hertz #131 已收敛的同型窗口）。要旧语义
// （Run 前同步回调）需显式 WithReadyProbeTimeout(负数) 禁用探活。
func TestRunServer_OnReadyNoTarget_WaitsForListener(t *testing.T) {
	addr := reserveLoopbackAddr(t)
	tcpAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		t.Fatalf("resolve addr: %v", err)
	}

	fa := newFakeApp()
	svr := echoservice.NewServer(&echoHandler{},
		server.WithServiceAddr(tcpAddr),
		server.WithExitWaitTime(200*time.Millisecond), // 测试快速排空
	)
	t.Cleanup(func() { _ = svr.Stop() })

	var readyCalled atomic.Int64
	listeningAtReady := atomic.Bool{}
	RunServer(fa, "noaddr-svc", svr,
		WithOnReady(func(ctx context.Context) {
			readyCalled.Add(1)
			// 回调时刻的端口可达性是本用例的核心断言面。
			if c, derr := net.DialTimeout("tcp", addr, 2*time.Second); derr == nil {
				_ = c.Close()
				listeningAtReady.Store(true)
			}
		}),
		// 不设 WithReadyAddr：走 Started 门路径（#290）。
		WithReadyProbeTimeout(5*time.Second))

	deadline := time.After(10 * time.Second)
	for readyCalled.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("onReady not called within deadline (Started gate never opened)")
		case <-fa.requestedCh:
			t.Fatal("server requested shutdown before becoming ready")
		case <-time.After(20 * time.Millisecond):
		}
	}
	if !listeningAtReady.Load() {
		t.Fatal("onReady 回调时端口未监听——回到了 Run 前同步回调的旧语义（#290 窗口）")
	}
}

// TestRunServer_DocAnchorsExitWaitTime 钉住 RunServer doc 包含关键
// 关闭语义关键字:ExitWaitTime (5s 默认 + K8s 时间关系公式)。
//
// 官方依据:https://www.cloudwego.io/zh/docs/kitex/best-practice/graceful_shutdown/
//
//	"kitex.ExitWaitTime 默认 5s;K8s 时间关系:DiscoverLatency < PreStopTime
//	 < MaxLatency < ExitWaitTime < TerminationGracePeriodSeconds"
//
// 该测试用 go/parser 读 kitex/bootstrap.go AST,找 RunServer func decl
// 关联的 doc 字符串,kitex 升级或注释误改时立即 fail。
func TestRunServer_DocAnchorsExitWaitTime(t *testing.T) {
	doc := runServerGodoc(t)
	required := []string{
		"ExitWaitTime",                  // 排空上限
		"默认 5s",                         // kitex 官方默认
		"DiscoverLatency",               // K8s 时间关系公式
		"TerminationGracePeriodSeconds", // K8s 强杀
	}
	for _, s := range required {
		if !strings.Contains(doc, s) {
			t.Errorf("RunServer doc missing required anchor %q (doc=\n%s)", s, doc)
		}
	}
}

// runServerGodoc 读 kitex/bootstrap.go 源码,parse AST,找 RunServer 函数的
// godoc 字符串(// 紧贴 func decl 之前的 comment group)。
func runServerGodoc(t *testing.T) string {
	t.Helper()
	// 找当前测试文件同目录的 bootstrap.go
	fset := token.NewFileSet()
	bootstrapPath := "bootstrap.go" // go test 的 cwd 是包目录(kitex/),相对路径据此解析
	node, err := parser.ParseFile(fset, bootstrapPath, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse bootstrap.go: %v", err)
	}
	for _, decl := range node.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Name.Name != "RunServer" {
			continue
		}
		if fn.Doc == nil {
			t.Fatal("RunServer has no godoc; add a comment block immediately above the func decl")
		}
		return fn.Doc.Text()
	}
	t.Fatal("RunServer func decl not found in bootstrap.go")
	return ""
}

// TestRunServer_ReadyProbe_RealServerBindsPort 是 #38 的回归用例：探活目标
// 由 **server 自己绑定**（端口在 svr.Run() 内部才绑定）时，就绪回调必须触发，
// 且端口真的在监听。
//
// 旧实现把探活放在 svr.Run() **之前** ⇒ 必然 connection refused ⇒ 探活超时
// ⇒ RequestShutdown ⇒ svr.Run() 从未被调用 ⇒ server 根本不起（与 hertz #43
// 同型）。本用例用真实 kitex server（echoservice）+ 真实端口，钉住
// 「就绪回调触发 + 端口在监听 + 未请求关机」三条。
func TestRunServer_ReadyProbe_RealServerBindsPort(t *testing.T) {
	addr := reserveLoopbackAddr(t)
	tcpAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		t.Fatalf("resolve addr: %v", err)
	}

	fa := newFakeApp()
	svr := echoservice.NewServer(&echoHandler{},
		server.WithServiceAddr(tcpAddr),
		server.WithExitWaitTime(200*time.Millisecond), // 测试快速排空
	)
	t.Cleanup(func() { _ = svr.Stop() })

	var readyCalled atomic.Int64
	RunServer(fa, "probe-svc", svr,
		WithOnReady(func(ctx context.Context) { readyCalled.Add(1) }),
		WithReadyAddr(addr),
		WithReadyProbeTimeout(5*time.Second))

	deadline := time.After(10 * time.Second)
	for readyCalled.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("onReady not called within deadline (server never became ready)")
		case <-fa.requestedCh:
			t.Fatal("server requested shutdown before becoming ready")
		case <-time.After(20 * time.Millisecond):
		}
	}

	// 核心断言：端口真的在监听（旧实现下端口从不监听）。
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("server port %s is not listening: %v", addr, err)
	}
	_ = c.Close()

	if len(fa.closers) != 1 {
		t.Fatalf("closers = %v", fa.closers)
	}
	fa.cancel()
}

// TestRunServer_ReadyProbe_PortOwnedByOther_NoReady 钉住 #38 的第三条守则：
// 探活目标端口被**他人**占用时，本进程 server 必然绑定失败 ⇒ 不得回调
// onReady（否则把不属于自己的实例注册到注册中心），且按启动失败请求关机。
func TestRunServer_ReadyProbe_PortOwnedByOther_NoReady(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }() // 持续占用该端口（模拟他人监听）
	addr := ln.Addr().String()

	fa := newFakeApp()
	svr := &fakeServer{}

	var readyCalled atomic.Int64
	RunServer(fa, "busy-svc", svr,
		WithOnReady(func(ctx context.Context) { readyCalled.Add(1) }),
		WithReadyAddr(addr),
		WithReadyProbeTimeout(2*time.Second))

	select {
	case <-fa.requestedCh:
		// 按启动失败请求关机，符合预期
	case <-time.After(5 * time.Second):
		t.Fatal("probe target owned by another process did not request shutdown")
	}
	if readyCalled.Load() != 0 {
		t.Fatal("onReady must not fire when the probe target port is owned by another process")
	}
	fa.cancel()
}

// TestRunServer_ReadyProbe_PortStolenAfterPrecheck_NoReady 是 #137 的 TOCTOU
// **全链路**回归：端口归属预检（checkProbeAddrFree）通过后、kitex 真正 bind
// 之前，端口被**他人**抢占 ⇒ 本进程 server 绑定失败（svr.Run() 返回 error）。
// 此时探活若「连上即回调」会连上对方的监听器并误判就绪 ⇒ onReady 把不属于
// 自己的实例注册到注册中心。两阶段复核（started 门 + runDone 复核）必须闭合
// 该误判：server 先退出 ⇒ 按启动失败处理，绝不回调 onReady。
//
// 时序确定性：预检、探活协程 spawn、svr.Run() 在同一 goroutine 内顺序执行，
// fakeServer 的 Run 在预检**之后**才抢占端口；fakeServer 不触发
// RegisterStartHook（started 永不就绪），探活阶段一必然等到 runDone 关闭
// （Run 返回）才动——「抢占 → Run 失败返回 → runDone 关闭」全序无竞速。
//
// 与既有用例互补：TestRunServer_ReadyProbe_PortOwnedByOther_NoReady 钉
// 「预检即拦截」（端口在预检时已被占）；本用例钉「预检通过后被抢占」；
// 「dial 建连成功但 server 已退出」的复核分支由
// TestProbeAndReady_ServerExitedBeforeReady_NoReady 场景 B 钉住。
func TestRunServer_ReadyProbe_PortStolenAfterPrecheck_NoReady(t *testing.T) {
	// 此刻空闲：预检（Listen + Close）必然通过。
	addr := reserveLoopbackAddr(t)

	fa := newFakeApp()
	// stolenCh 回传「他人」抢占用的 listener，避免与 Cleanup 跨 goroutine
	// 共享变量（-race 干净）。
	stolenCh := make(chan net.Listener, 1)
	svr := &fakeServer{
		runFunc: func() error {
			// 模拟「他人」在预检 Close 与本进程 bind 之间抢占端口：先占住
			// 监听，再模拟本进程 bind 失败（Run 返回 error）。若 Listen 因
			// 环境噪声失败（端口被无关进程抢先），Run 仍返回 error，
			// 「server 退出 ⇒ 不得回调」的断言同样成立。
			if ln, lerr := net.Listen("tcp", addr); lerr == nil {
				stolenCh <- ln
			}
			return errors.New("simulated kitex bind failure (port stolen after pre-check)")
		},
	}
	t.Cleanup(func() {
		select {
		case ln := <-stolenCh:
			_ = ln.Close()
		default:
		}
	})

	var readyCalled atomic.Int64
	RunServer(fa, "stolen-after-precheck-svc", svr,
		WithOnReady(func(ctx context.Context) { readyCalled.Add(1) }),
		WithReadyAddr(addr),
		WithReadyProbeTimeout(2*time.Second))

	select {
	case <-fa.requestedCh:
		// 按启动失败请求关机，符合预期
	case <-time.After(5 * time.Second):
		t.Fatal("port stolen after pre-check did not request shutdown")
	}
	if readyCalled.Load() != 0 {
		t.Fatal("onReady must not fire when the port was stolen after pre-check (dial would hit the other process's listener)")
	}
	fa.cancel()
}

// reserveLoopbackAddr 取一个空闲的回环端口号（先 listen 再释放），供真实
// server 绑定 —— 与 e2e startE2EServer 同口径。
func reserveLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// TestRunServer_ReadyProbeTimeout 钉住探活超时路径：按启动失败处理
// （RequestShutdown）。fake server 的 Run 必须**阻塞**（真实 server 的
// Run 会一直阻塞到 server 退出）——否则 GoBackground 提前返回会把探活
// 一并中止，走的是「server 退出」分支而非超时分支。
func TestRunServer_ReadyProbeTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // 确定不可建连

	fa := newFakeApp()
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	svr := &fakeServer{
		runFunc: func() error {
			<-stop
			return nil
		},
	}

	var readyCalled atomic.Int64
	RunServer(fa, "test-svc", svr,
		WithOnReady(func(ctx context.Context) { readyCalled.Add(1) }),
		WithReadyAddr(addr),
		WithReadyProbeTimeout(300*time.Millisecond))

	select {
	case <-fa.requestedCh:
		// 已按启动失败请求关机
	case <-time.After(3 * time.Second):
		t.Fatal("probe timeout did not request shutdown")
	}
	if readyCalled.Load() != 0 {
		t.Fatal("onReady should not be called on probe timeout")
	}
	fa.cancel()
}

// TestRunServer_NoProbeAddr_WaitsForStartedGate 钉住无探活目标路径的新语义
// （#290，对称 hertz #131）：onReady 不再在 Run 前同步回调，而是等 Started
// 门（RegisterStartHook）通过后回调——fakeServer 永不启动（钩子不触发）⇒
// 按探活超时 fail-closed 请求关机、onReady 不得回调（不能把「未监听」的
// 实例注册出去）。正向路径（真 server 等 listener 后回调）见
// TestRunServer_OnReadyNoTarget_WaitsForListener；需要旧「Run 前同步回调」
// 语义用 WithReadyProbeTimeout(负数) 显式禁用探活。
func TestRunServer_NoProbeAddr_WaitsForStartedGate(t *testing.T) {
	fa := newFakeApp()
	// Run 保持阻塞（真 server 形态）：立即返回会让 GoBackground 收尾先
	// cancelProbe，探活被判「正常关停打断」（ErrProbeAborted）而不请求关机
	// ——那不是本用例要钉的 fail-closed 路径。
	block := make(chan struct{})
	svr := &fakeServer{runFunc: func() error { <-block; return nil }}
	t.Cleanup(func() { close(block) })

	var readyCalled atomic.Int64
	RunServer(fa, "test-svc", svr,
		WithOnReady(func(ctx context.Context) { readyCalled.Add(1) }),
		WithReadyProbeTimeout(300*time.Millisecond))

	select {
	case <-fa.requestedCh:
		// Started 门在探活预算内未打开：按启动失败请求关机（fail-closed）。
	case <-time.After(3 * time.Second):
		t.Fatal("started gate 未打开时必须按探活超时请求关机")
	}
	if got := readyCalled.Load(); got != 0 {
		t.Fatalf("onReady called %d times, want 0（server 未 started 不得回调）", got)
	}
	fa.cancel()
}

// TestRunServer_ProbeDisabled_FiresBeforeRun 钉住显式禁用探活路径的保留
// 语义（WithReadyProbeTimeout 负数）：onReady 在 Run 前同步执行。
func TestRunServer_ProbeDisabled_FiresBeforeRun(t *testing.T) {
	fa := newFakeApp()
	svr := &fakeServer{}

	var readyCalled atomic.Int64
	RunServer(fa, "test-svc", svr,
		WithOnReady(func(ctx context.Context) { readyCalled.Add(1) }),
		WithReadyProbeTimeout(-time.Second))

	deadline := time.After(3 * time.Second)
	for readyCalled.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("onReady not called within deadline")
		case <-fa.requestedCh:
			t.Fatal("probe-disabled path should not request shutdown")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if got := readyCalled.Load(); got != 1 {
		t.Fatalf("onReady called %d times, want 1", got)
	}
}

// TestRunServer_NegativeProbeTimeout_DisablesProbeSyncFires 是 oekit#142 的
// kitex 侧回归：WithReadyProbeTimeout 传负数禁用探活（与 hertz 侧同名 option
// 语义对齐）——即使已设置 WithReadyAddr，onReady 也必须改在 svr.Run() 启动前
// **同步**回调，不做端口探活（回调时端口可能尚未监听）。
//
// 旧实现 timeout<=0 一律取默认 5s 继续探活：双协议服务复制 hertz 的配置时
// 行为漂移（hertz 负数=禁用）。
func TestRunServer_NegativeProbeTimeout_DisablesProbeSyncFires(t *testing.T) {
	fa := newFakeApp()

	var readyCalled atomic.Int64
	svr := &fakeServer{
		runFunc: func() error {
			// 同步语义断言：Run 启动时 onReady 必须已回调（同一 goroutine 内
			// 先回调后 Run），探活禁用路径绝不 spawn 探活协程。
			if readyCalled.Load() != 1 {
				t.Error("负超时禁用探活：onReady 必须在 svr.Run() 之前同步回调")
			}
			return nil
		},
	}

	RunServer(fa, "neg-timeout-svc", svr,
		WithOnReady(func(ctx context.Context) { readyCalled.Add(1) }),
		WithReadyAddr("127.0.0.1:1"),        // 不可建连的探活目标：若仍探活必超时失败
		WithReadyProbeTimeout(-time.Second)) // 负数 = 禁用探活

	deadline := time.After(3 * time.Second)
	for readyCalled.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("onReady not called within deadline（负超时应禁用探活并同步回调）")
		case <-fa.requestedCh:
			t.Fatal("禁用探活路径不应请求关机")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if got := readyCalled.Load(); got != 1 {
		t.Fatalf("onReady called %d times, want 1", got)
	}
	fa.cancel()
}

// TestWithReadyProbeTimeoutOptionSemantics 钉住 option 边界语义：负数=禁用
// 探活；零值/正数=不禁用（零值经 core/lifecycle.ReadyProbe 取默认 5s，#208
// 探活机制单源）。
func TestWithReadyProbeTimeoutOptionSemantics(t *testing.T) {
	cases := []struct {
		name     string
		d        time.Duration
		disabled bool
	}{
		{"negative disables", -1 * time.Second, true},
		{"zero keeps default path", 0, false},
		{"positive sets timeout", 3 * time.Second, false},
		{"missing option keeps default path", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &runServerConfig{}
			if tc.name != "missing option keeps default path" {
				WithReadyProbeTimeout(tc.d)(cfg)
			}
			if cfg.probeDisabled != tc.disabled {
				t.Fatalf("probeDisabled = %v, want %v", cfg.probeDisabled, tc.disabled)
			}
		})
	}
}

// TestRunServer_StopIdempotent_SecondStopReturnsNil 钉住 #121 修正后的注释
// 契约：kitex v0.16.4 的 server.Stop() 以 sync.Once 包裹，**二次 Stop 返回
// nil**（全仓不存在 ErrServerDone 符号）。RunServer 注册的 closer 依赖该
// 幂等行为处理「信号路径 + bootstrap 关闭回调」的双路关闭竞争——kitex 未来
// 若改为返回错误，本测试先红，提示需要补 hertz 式的已关机错误吞并。
func TestRunServer_StopIdempotent_SecondStopReturnsNil(t *testing.T) {
	addr := reserveLoopbackAddr(t)
	tcpAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		t.Fatalf("resolve addr: %v", err)
	}

	svr := echoservice.NewServer(&echoHandler{},
		server.WithServiceAddr(tcpAddr),
		server.WithExitWaitTime(200*time.Millisecond), // 测试快速排空
	)
	runErr := make(chan error, 1)
	go func() { runErr <- svr.Run() }()

	// 等端口真的在监听（与 e2e startBizErrServer 同口径）。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, derr := net.DialTimeout("tcp", addr, 100*time.Millisecond); derr == nil {
			_ = c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 第一次 Stop：优雅排空，期望 nil。
	if stopErr := svr.Stop(); stopErr != nil {
		t.Fatalf("first Stop() = %v, want nil", stopErr)
	}
	// 第二次 Stop（bootstrap 关闭回调与信号路径双路关闭的竞争路径）：sync.Once
	// 幂等 ⇒ 必须返回 nil，绝无 ErrServerDone 一类的已关机错误。
	if stopErr := svr.Stop(); stopErr != nil {
		t.Fatalf("second Stop() = %v, want nil（kitex v0.16.4 Stop 幂等，#121）", stopErr)
	}

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run() returned error after Stop: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run() did not return after Stop")
	}
}
