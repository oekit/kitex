package kitex

import (
	"testing"
	"time"

	klimiter "github.com/cloudwego/kitex/pkg/limiter"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

// TestThrottledLimitReporter_AllowGate 验证节流门控：窗口内第一条放行、
// 后续全部丢弃、过窗后重新放行；conn 与 qps 两个通道独立节流。
func TestThrottledLimitReporter_AllowGate(t *testing.T) {
	r := &throttledLimitReporter{minInterval: 50 * time.Millisecond}

	if !r.allow(&r.lastConnAllow) {
		t.Fatal("first report should be allowed")
	}
	if r.allow(&r.lastConnAllow) {
		t.Error("second report inside window should be throttled")
	}
	// 窗口内连续上报必须全部被节流（CAS 循环不得误放行）
	for i := 0; i < 10; i++ {
		if r.allow(&r.lastConnAllow) {
			t.Fatal("reports inside window must all be throttled")
		}
	}
	// QPS 通道独立节流，不受 conn 通道影响
	if !r.allow(&r.lastQPSAllow) {
		t.Error("qps channel has its own throttle window")
	}
	time.Sleep(60 * time.Millisecond)
	if !r.allow(&r.lastConnAllow) {
		t.Error("report after window should be allowed")
	}
}

// recordingLimitReporter 记录上报次数，验证自定义上报器优先于默认实现。
type recordingLimitReporter struct {
	conn, qps int
}

func (m *recordingLimitReporter) ConnOverloadReport() { m.conn++ }
func (m *recordingLimitReporter) QPSOverloadReport()  { m.qps++ }

// TestNewLimitReporter_Custom 验证：注入自定义上报器时优先使用；
// 未注入时回退到内置节流日志上报器。
func TestNewLimitReporter_Custom(t *testing.T) {
	rec := &recordingLimitReporter{}
	got := newLimitReporter(rec)
	got.ConnOverloadReport()
	got.QPSOverloadReport()
	if rec.conn != 1 || rec.qps != 1 {
		t.Errorf("custom reporter not used: conn=%d qps=%d", rec.conn, rec.qps)
	}

	def := newLimitReporter(nil)
	//nolint:staticcheck // 编译期接口合规断言,保留
	var _ klimiter.LimitReporter = def
	if _, ok := def.(*throttledLimitReporter); !ok {
		t.Errorf("nil custom should yield default throttled reporter, got %T", def)
	}
}

// TestServerSuite_WithLimitReporter 验证 ServerSuite 可注入自定义上报器且构造正常。
func TestServerSuite_WithLimitReporter(t *testing.T) {
	stub := &stubConfigClient{
		getConfigFunc:    func(_ vo.ConfigParam) (string, error) { return "", nil },
		listenConfigFunc: func(_ vo.ConfigParam) error { return nil },
	}
	g, _ := NewGovernanceConfig(stub)
	s := NewServerSuite("svc", g, WithLimiter(), WithLimitReporter(&recordingLimitReporter{}))
	if s == nil || len(s.Options()) == 0 {
		t.Fatal("expected non-empty server options")
	}
}
