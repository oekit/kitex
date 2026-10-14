package kitex

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"sync"

	"github.com/cloudwego/kitex/pkg/klog"
	"github.com/yi-nology/ekit-observability/observability/logx"
)

// kclogAdapter 将 ekit logx.Logger 适配为 Kitex klog.FullLogger。
//
// 映射关系：
//   - Trace → Debug（logx 无 Trace 级别）
//   - Notice → Info（logx 无 Notice 级别）
//   - Fatal → Error（ekit 不做 os.Exit）
type kclogAdapter struct {
	logger logx.Logger
}

var _ klog.FullLogger = (*kclogAdapter)(nil)

// registerBridgeFileOnce 把本文件注册为 logx 的内部帧（仅一次）：
// 适配器方法位于 logx 之外，若不跳过，caller 字段会归属到本文件而非
// Kitex 框架真实调用方（官方日志文档警告的封装定位失准问题）。
var registerBridgeFileOnce sync.Once

// NewLogger 将 ekit [logx.Logger] 适配为 Kitex [klog.FullLogger]。
//
// 本函数会把自身所在文件注册为 logx 内部帧（logx.SkipCallerFiles），
// 使经由适配器的框架日志 caller 归属真实调用方；副作用仅此一项。
//
// 使用方式（必须传 slog 实例，不可传 hlog 后端的 Logger）：
//
//	klog.SetLogger(kitex.NewLogger(logx.NewSlogLogger("my-rpc")))
//
// ⚠️ 禁止传入 hlog 后端的 Logger（如 logx.WithService(...)），否则会形成
// adapter → logx → hlog → adapter 的无限递归，导致栈溢出和进程挂死。
// 此函数会在检测到 hlog 后端时 panic 以防止生产环境不可控故障。
func NewLogger(logger logx.Logger) klog.FullLogger {
	if logger == nil {
		panic("kitex.NewLogger: logger must not be nil")
	}
	if logx.IsHlogBacked(logger) {
		panic("kitex.NewLogger: hlog-backed Logger causes infinite recursion; use logx.NewSlogLogger(\"svc\") or logx.UseSlog(\"svc\")")
	}
	registerBridgeFileOnce.Do(func() {
		if _, file, _, ok := runtime.Caller(0); ok {
			logx.SkipCallerFiles(file)
		}
	})
	return &kclogAdapter{logger: logger}
}

func (a *kclogAdapter) Trace(v ...interface{})                  { a.logger.Debug(fmt.Sprint(v...)) }
func (a *kclogAdapter) Debug(v ...interface{})                  { a.logger.Debug(fmt.Sprint(v...)) }
func (a *kclogAdapter) Info(v ...interface{})                   { a.logger.Info(fmt.Sprint(v...)) }
func (a *kclogAdapter) Notice(v ...interface{})                 { a.logger.Info(fmt.Sprint(v...)) }
func (a *kclogAdapter) Warn(v ...interface{})                   { a.logger.Warn(fmt.Sprint(v...)) }
func (a *kclogAdapter) Error(v ...interface{})                  { a.logger.Error(fmt.Sprint(v...)) }
func (a *kclogAdapter) Fatal(v ...interface{})                  { a.logger.Error(fmt.Sprint(v...)) }
func (a *kclogAdapter) Tracef(format string, v ...interface{})  { a.logger.Debugf(format, v...) }
func (a *kclogAdapter) Debugf(format string, v ...interface{})  { a.logger.Debugf(format, v...) }
func (a *kclogAdapter) Infof(format string, v ...interface{})   { a.logger.Infof(format, v...) }
func (a *kclogAdapter) Noticef(format string, v ...interface{}) { a.logger.Infof(format, v...) }
func (a *kclogAdapter) Warnf(format string, v ...interface{})   { a.logger.Warnf(format, v...) }
func (a *kclogAdapter) Errorf(format string, v ...interface{})  { a.logger.Errorf(format, v...) }
func (a *kclogAdapter) Fatalf(format string, v ...interface{})  { a.logger.Errorf(format, v...) }

func (a *kclogAdapter) CtxTracef(ctx context.Context, format string, v ...interface{}) {
	a.logger.CtxDebugf(ctx, format, v...)
}
func (a *kclogAdapter) CtxDebugf(ctx context.Context, format string, v ...interface{}) {
	a.logger.CtxDebugf(ctx, format, v...)
}
func (a *kclogAdapter) CtxInfof(ctx context.Context, format string, v ...interface{}) {
	a.logger.CtxInfof(ctx, format, v...)
}
func (a *kclogAdapter) CtxNoticef(ctx context.Context, format string, v ...interface{}) {
	a.logger.CtxInfof(ctx, format, v...)
}
func (a *kclogAdapter) CtxWarnf(ctx context.Context, format string, v ...interface{}) {
	a.logger.CtxWarnf(ctx, format, v...)
}
func (a *kclogAdapter) CtxErrorf(ctx context.Context, format string, v ...interface{}) {
	a.logger.CtxErrorf(ctx, format, v...)
}
func (a *kclogAdapter) CtxFatalf(ctx context.Context, format string, v ...interface{}) {
	a.logger.CtxErrorf(ctx, format, v...)
}

func (a *kclogAdapter) SetLevel(lv klog.Level) {
	switch {
	case lv <= klog.LevelDebug:
		a.logger.SetLevel(logx.LevelDebug)
	case lv <= klog.LevelInfo:
		a.logger.SetLevel(logx.LevelInfo)
	case lv <= klog.LevelNotice:
		// 与方法级映射一致：Notice → Info（CtxNoticef → CtxInfof）
		a.logger.SetLevel(logx.LevelInfo)
	case lv <= klog.LevelWarn:
		a.logger.SetLevel(logx.LevelWarn)
	default:
		a.logger.SetLevel(logx.LevelError)
	}
}

// SetOutput 有意 no-op：logx.Logger 的输出目标由后端（slog/hlog/std）统一管理，
// 不暴露 per-logger writer；本方法仅为满足 klog.Control 契约而存在。
func (a *kclogAdapter) SetOutput(_ io.Writer) {}
