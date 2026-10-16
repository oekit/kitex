package kitex

import (
	"testing"

	"github.com/cloudwego/kitex/pkg/klog"
	"github.com/oekit/logx"
)

// TestNewLogger_AliasDelegates 冒烟：Deprecated 别名委托 logx/kitexlog 实现，
// 返回值实现 klog.FullLogger 与 klog.Control（SetLevel/SetOutput）契约。
func TestNewLogger_AliasDelegates(t *testing.T) {
	logger := NewLogger(logx.NewSlogLogger("t"))
	if logger == nil {
		t.Fatal("expected non-nil logger")
	}
	//nolint:staticcheck // 显式 interface compliance check，省略 type 后断言失去意义 (QF1011)
	var _ klog.FullLogger = logger
	var _ klog.Control = logger // Control 契约（SetLevel/SetOutput）
}
