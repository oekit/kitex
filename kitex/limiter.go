package kitex

import (
	"encoding/json"
	"sync"

	"github.com/cloudwego/kitex/pkg/limit"
	klimiter "github.com/cloudwego/kitex/pkg/limiter"
	"github.com/cloudwego/kitex/server"
)

// limitConfig 用于解析 Nacos limit 配置。
type limitConfig struct {
	MaxConnections int `json:"connection_limit"`
	MaxQPS         int `json:"qps_limit"`
}

// limiterController 管理 limit 配置的解析、暂存与应用。
// 抽出独立类型便于测试；withLimiter 只负责订阅与注入。
type limiterController struct {
	dataID string

	mu      sync.Mutex // 保护 updater 与 pending；Nacos 回调与 server 启动 goroutine 并发
	updater limit.Updater
	pending *limit.Option
}

// apply 解析并应用一轮 limit 配置，返回是否解析成功。
//
// kitex 语义：limit <= 0 表示不限流，因此 {0,0} 即「解除限流」，
// 必须原样下发 UpdateLimit——不能按 Option.Valid() 过滤，
// 否则配置过限流后就永远无法清零。
func (c *limiterController) apply(data string) bool {
	var cfg limitConfig
	if err := json.Unmarshal([]byte(data), &cfg); err != nil {
		govLog.Error("parse limit config failed", "dataId", c.dataID, "err", err)
		return false
	}
	limitOpt := &limit.Option{
		MaxConnections: cfg.MaxConnections,
		MaxQPS:         cfg.MaxQPS,
	}
	c.mu.Lock()
	u := c.updater
	if u == nil {
		// server 尚未启动，暂存待 UpdateControl 回放
		c.pending = limitOpt
		c.mu.Unlock()
		govLog.Info("limit config cached until server starts", "dataId", c.dataID,
			"maxConn", cfg.MaxConnections, "maxQPS", cfg.MaxQPS)
		return true
	}
	c.mu.Unlock()
	c.applyLimit(u, limitOpt)
	return true
}

// applyLimit 下发配置到 updater；UpdateLimit 返回 false 说明限流器不可更新
// （kitex 正常路径不会发生），必须留痕而非静默。
func (c *limiterController) applyLimit(u limit.Updater, limitOpt *limit.Option) {
	if u.UpdateLimit(limitOpt) {
		govLog.Info("limit config updated", "dataId", c.dataID,
			"maxConn", limitOpt.MaxConnections, "maxQPS", limitOpt.MaxQPS)
		return
	}
	govLog.Warn("limit config not applied", "dataId", c.dataID,
		"maxConn", limitOpt.MaxConnections, "maxQPS", limitOpt.MaxQPS)
}

// updateControl 是 kitex server 就绪时的回调：记录 updater 并回放暂存配置。
func (c *limiterController) updateControl(u limit.Updater) {
	c.mu.Lock()
	c.updater = u
	p := c.pending
	c.pending = nil
	c.mu.Unlock()
	// server 就绪，回放订阅期间收到的配置
	if p != nil {
		c.applyLimit(u, p)
	}
}

// withLimiter 订阅 Nacos limit 配置并注入 limit.Option。
//
// dataId 格式: {serverService}.limit
// 配置格式（JSON）：
//
//	{"connection_limit":100, "qps_limit":2000}
//
// 时序说明：Nacos 订阅在 suite 构造时建立，而 kitex 在 server 启动时才回调
// UpdateControl 注入 updater。订阅期间到达的配置暂存 pending，
// server 就绪后回放——首推配置不再被静默丢弃。
//
// reporter 为限流过载上报器（nil 时使用内置节流日志上报）。
//
// ⚠️ kitex 限流仅对 Thrift / KitexPB 协议生效（gRPC 需用传输层流控选项）；
// 若同时使用 server.WithQPSLimiter / server.WithConnectionLimiter，
// 它们会覆盖本 Suite 注入的限流器。
func withLimiter(serverService string, gov *GovernanceConfig, reporter klimiter.LimitReporter) []server.Option {
	dataID := gov.serverDataID(serverService, "limit")
	ctrl := &limiterController{dataID: dataID}

	opts := make([]server.Option, 0, 2)
	opts = append(opts,
		server.WithLimit(&limit.Option{
			UpdateControl: ctrl.updateControl,
		}),
		server.WithLimitReporter(newLimitReporter(reporter)),
	)

	if err := gov.subscribe(dataID, func(data string) {
		ctrl.apply(data)
	}); err != nil {
		govLog.Warn("subscribe limit config failed", "dataId", dataID, "err", err)
	}

	return opts
}
