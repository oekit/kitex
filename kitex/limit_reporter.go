package kitex

import (
	"sync/atomic"
	"time"

	klimiter "github.com/cloudwego/kitex/pkg/limiter"
)

// defaultReportInterval 是默认过载上报的最小间隔：kitex 在每次拒绝请求时回调
// LimitReporter，不限频的日志会在限流生效期间形成风暴。
const defaultReportInterval = time.Second

// throttledLimitReporter 是默认的限流过载上报器：结构化 Warn + 按 event 类型节流
// （默认每秒至多一条）。需要接入 metrics 时经 [WithLimitReporter] 注入自定义实现。
type throttledLimitReporter struct {
	minInterval   time.Duration
	lastConnAllow atomic.Int64 // UnixNano
	lastQPSAllow  atomic.Int64
}

// newLimitReporter 返回用户注入的上报器；未注入时使用内置节流日志上报。
func newLimitReporter(custom klimiter.LimitReporter) klimiter.LimitReporter {
	if custom != nil {
		return custom
	}
	return &throttledLimitReporter{minInterval: defaultReportInterval}
}

// ConnOverloadReport 实现 [klimiter.LimitReporter]：连接数超限上报。
func (r *throttledLimitReporter) ConnOverloadReport() {
	if r.allow(&r.lastConnAllow) {
		govLog.Warn("server connection limit overloaded, requests are being rejected")
	}
}

// QPSOverloadReport 实现 [klimiter.LimitReporter]：QPS 超限上报。
func (r *throttledLimitReporter) QPSOverloadReport() {
	if r.allow(&r.lastQPSAllow) {
		govLog.Warn("server qps limit overloaded, requests are being rejected")
	}
}

// allow 节流判断：距上次放行不足 minInterval 则丢弃本次上报。
// CAS 保证并发回调下仍精确限频（不会被并发打穿窗口）。
func (r *throttledLimitReporter) allow(last *atomic.Int64) bool {
	now := time.Now().UnixNano()
	for {
		prev := last.Load()
		if now-prev < int64(r.minInterval) {
			return false
		}
		if last.CompareAndSwap(prev, now) {
			return true
		}
	}
}
