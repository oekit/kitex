package kitex

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/kitex/pkg/serviceinfo"
	"github.com/cloudwego/kitex/server"
	"github.com/yi-nology/ekit-concurrency/async"
	"github.com/yi-nology/ekit-core/lifecycle"
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
