package kitex

// #212 回归：nns/kitex 的 Registry 适配器 Register/Deregister 必须带单次
// 预算——kitex 的 kregistry.Registry 接口无 ctx，框架注入的 WithTimeout
// 必须真正传到底层 Registrar（与 registry/kitex 的 #191 同根因同口径）。

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/cloudwego/kitex/pkg/registry"

	regpkg "github.com/oekit/registry"
)

// blockingRegistrar 注册/注销阻塞到 ctx 到期或自身 delay——用于钉预算：
// 接口层注入的 WithTimeout 必须真正传到底层 Registrar。
type blockingRegistrar struct {
	delay      time.Duration
	regCtx     context.Context
	deregCtx   context.Context
	regCalls   int
	deregCalls int
}

func (b *blockingRegistrar) Register(ctx context.Context, _ regpkg.Instance) error {
	b.regCalls++
	b.regCtx = ctx
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(b.delay):
		return nil
	}
}

func (b *blockingRegistrar) Deregister(ctx context.Context, _ regpkg.Instance) error {
	b.deregCalls++
	b.deregCtx = ctx
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(b.delay):
		return nil
	}
}

func budgetInfo() *registry.Info {
	return &registry.Info{ServiceName: "budget-svc", Addr: &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 8080}}
}

// TestNNSRegistry_Register_TimeoutEnforced 预算到期即失败，不再是无截止的
// Background。
func TestNNSRegistry_Register_TimeoutEnforced(t *testing.T) {
	blk := &blockingRegistrar{delay: 5 * time.Second}
	c := newTestClientWithRegistrar(blk, "", nil)
	r := NewRegistry(c, WithRegisterTimeout(50*time.Millisecond), WithDeregisterTimeout(50*time.Millisecond))

	start := time.Now()
	err := r.Register(budgetInfo())
	elapsed := time.Since(start)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Register err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed > time.Second {
		t.Fatalf("Register took %v, timeout budget not enforced", elapsed)
	}
	if blk.regCtx == nil || blk.regCtx.Err() == nil {
		t.Error("Registrar did not observe a deadline-bound ctx")
	}

	start = time.Now()
	err = r.Deregister(budgetInfo())
	elapsed = time.Since(start)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Deregister err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed > time.Second {
		t.Fatalf("Deregister took %v, timeout budget not enforced", elapsed)
	}
}

// TestNNSRegistry_Register_DefaultBudget 缺省构造即带 3s 预算：未配选项时
// 底层拿到的 ctx 也应有截止时间。
func TestNNSRegistry_Register_DefaultBudget(t *testing.T) {
	blk := &blockingRegistrar{delay: 0}
	c := newTestClientWithRegistrar(blk, "", nil)
	r := NewRegistry(c)

	if err := r.Register(budgetInfo()); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if dl, ok := blk.regCtx.Deadline(); !ok || dl.Before(time.Now()) {
		t.Error("default Register ctx has no future deadline (budget missing)")
	}
	if err := r.Deregister(budgetInfo()); err != nil {
		t.Fatalf("Deregister() error = %v", err)
	}
	if dl, ok := blk.deregCtx.Deadline(); !ok || dl.Before(time.Now()) {
		t.Error("default Deregister ctx has no future deadline (budget missing)")
	}
}

// TestNNSRegistry_WithTimeout_Negative_Panics 非法预算构造期 fail-fast。
func TestNNSRegistry_WithTimeout_Negative_Panics(t *testing.T) {
	c := newTestClientWithRegistrar(&blockingRegistrar{}, "", nil)
	assertPanic(t, func() { NewRegistry(c, WithRegisterTimeout(-1)) })
	assertPanic(t, func() { NewRegistry(c, WithDeregisterTimeout(0)) })
}

func assertPanic(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Error("expected panic, got none")
		}
	}()
	fn()
}
