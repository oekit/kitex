package kitex

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
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

// TestRunServer_NoProbeAddr_FiresBeforeRun 钉住无探活目标路径：
// onReady 在 Run 前同步执行。
func TestRunServer_NoProbeAddr_FiresBeforeRun(t *testing.T) {
	fa := newFakeApp()
	svr := &fakeServer{}

	var readyCalled atomic.Int64
	RunServer(fa, "test-svc", svr,
		WithOnReady(func(ctx context.Context) { readyCalled.Add(1) }))

	deadline := time.After(3 * time.Second)
	for readyCalled.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("onReady not called within deadline")
		case <-fa.requestedCh:
			t.Fatal("no-probe path should not request shutdown")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if got := readyCalled.Load(); got != 1 {
		t.Fatalf("onReady called %d times, want 1", got)
	}
}
