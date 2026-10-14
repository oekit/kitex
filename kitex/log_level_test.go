package kitex

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/yi-nology/ekit-observability/observability/logx"
	"github.com/cloudwego/kitex/pkg/klog"
)

// recordingLogger 记录 SetLevel 调用，用于断言 klog → logx 级别映射。
// 仅实现 SetLevel，其余方法内嵌 nil 接口（测试只调 SetLevel，不会触发）。
type recordingLogger struct {
	logx.Logger

	mu    sync.Mutex
	level logx.Level
}

func (r *recordingLogger) SetLevel(lv logx.Level) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.level = lv
}

func (r *recordingLogger) current() logx.Level {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.level
}

// TestKclogAdapter_SetLevel_Mapping 验证 SetLevel 与方法级映射一致：
// klog.LevelNotice(3) 应落 logx.LevelInfo（CtxNoticef → CtxInfof），而非 Warn。
func TestKclogAdapter_SetLevel_Mapping(t *testing.T) {
	cases := []struct {
		in   klog.Level
		want logx.Level
	}{
		{klog.LevelDebug, logx.LevelDebug},
		{klog.LevelInfo, logx.LevelInfo},
		{klog.LevelNotice, logx.LevelInfo},
		{klog.LevelWarn, logx.LevelWarn},
		{klog.LevelError, logx.LevelError},
		{klog.LevelFatal, logx.LevelError},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("klog%d", tc.in), func(t *testing.T) {
			rec := &recordingLogger{}
			a := &kclogAdapter{logger: rec}
			a.SetLevel(tc.in)
			if got := rec.current(); got != tc.want {
				t.Errorf("SetLevel(%d) = logx level %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestKclogAdapter_SetOutput_NoOp 文档化行为：logx 后端统一管理输出，
// SetOutput 有意 no-op（klog.Control 契约要求方法存在）。
func TestKclogAdapter_SetOutput_NoOp(t *testing.T) {
	rec := &recordingLogger{}
	a := &kclogAdapter{logger: rec}
	a.SetOutput(io.Discard) // 不得 panic
	_ = context.Background()
}
