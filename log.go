package kitex

import (
	"github.com/cloudwego/kitex/pkg/klog"
	"github.com/oekit/logx"
	logxkitexlog "github.com/oekit/logx/kitexlog"
)

// NewLogger 将 ekit [logx.Logger] 适配为 Kitex [klog.FullLogger]。
//
// 映射关系（与迁移前完全一致）：
//   - Trace → Debug（logx 无 Trace 级别）
//   - Notice → Info（logx 无 Notice 级别）
//   - Fatal → Error（ekit 不做 os.Exit）
//
// 必须传 slog 实例，不可传 hlog 后端的 Logger（如 logx.WithService(...)），
// 否则会形成 adapter → logx → hlog → adapter 的无限递归；实现会在检测到
// hlog 后端时 panic 以防止生产环境不可控故障。
//
// Deprecated: 已迁至 github.com/oekit/logx/kitexlog（kitexlog.NewLogger），
// 本别名委托实现，行为完全一致；将在未来 major 版本移除。
func NewLogger(logger logx.Logger) klog.FullLogger {
	return logxkitexlog.NewLogger(logger)
}
