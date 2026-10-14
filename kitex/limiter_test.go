package kitex

import (
	"testing"

	"github.com/cloudwego/kitex/pkg/limit"
)

// fakeUpdater 捕获 UpdateLimit 收到的配置。
type fakeUpdater struct {
	got   *limit.Option
	calls int
}

func (f *fakeUpdater) UpdateLimit(opt *limit.Option) bool {
	f.got = opt
	f.calls++
	return true
}

// TestLimiterController_ClearLimits 验证 {0,0} 会原样下发 UpdateLimit
// （kitex 语义 limit<=0 = 不限流），而不是被 Valid() 门禁静默丢弃——
// 否则一旦配置过限流就永远无法解除。
func TestLimiterController_ClearLimits(t *testing.T) {
	ctrl := &limiterController{dataID: "s.limit"}
	fake := &fakeUpdater{}
	ctrl.updateControl(fake)

	if !ctrl.apply(`{"connection_limit":0,"qps_limit":0}`) {
		t.Fatal("apply failed")
	}
	if fake.calls != 1 {
		t.Fatalf("expected UpdateLimit called once, got %d", fake.calls)
	}
	if fake.got == nil || fake.got.MaxConnections != 0 || fake.got.MaxQPS != 0 {
		t.Errorf("UpdateLimit got %+v, want {0,0}", fake.got)
	}
}

// TestLimiterController_PendingReplay 验证 server 就绪前收到的配置
// 会被暂存 pending 并在 UpdateControl 回放。
func TestLimiterController_PendingReplay(t *testing.T) {
	ctrl := &limiterController{dataID: "s.limit"}
	fake := &fakeUpdater{}

	if !ctrl.apply(`{"connection_limit":100,"qps_limit":2000}`) {
		t.Fatal("apply failed")
	}
	if fake.calls != 0 {
		t.Fatalf("expected no UpdateLimit before server starts, got %d calls", fake.calls)
	}

	ctrl.updateControl(fake)
	if fake.calls != 1 {
		t.Fatalf("expected pending config replayed once, got %d calls", fake.calls)
	}
	if fake.got == nil || fake.got.MaxConnections != 100 || fake.got.MaxQPS != 2000 {
		t.Errorf("UpdateLimit got %+v, want {100,2000}", fake.got)
	}
}

// TestLimiterController_BadJSON 验证解析失败不 panic 且不下发。
func TestLimiterController_BadJSON(t *testing.T) {
	ctrl := &limiterController{dataID: "s.limit"}
	fake := &fakeUpdater{}
	ctrl.updateControl(fake)

	if ctrl.apply("not-json{") {
		t.Error("apply(bad json) should report failure")
	}
	if fake.calls != 0 {
		t.Errorf("expected no UpdateLimit on bad JSON, got %d calls", fake.calls)
	}
}
