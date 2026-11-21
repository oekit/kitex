package kitex

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/pkg/rpcinfo"
	"github.com/cloudwego/kitex/pkg/rpctimeout"
	"github.com/oekit/core/governance"

	ekiterr "github.com/oekit/pkg/errors"

	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
	"github.com/oekit/logx"
)

var govLog = logx.Delegating("kitex-gov")

// 订阅失败后台重试缺省退避（指数增长，封顶 max）。
const (
	defaultRetryBase = 500 * time.Millisecond
	defaultRetryMax  = 30 * time.Second
)

// GovernanceConfig 从 Nacos 配置中心加载 Kitex 治理策略（retry/rpc_timeout/circuit_breaker/limiter）
// 并在配置变更时热更新到 Kitex 运行时。
//
// 每类策略对应一个独立的 Nacos 配置项（dataId），格式为：
//
//	客户端策略: {clientService}.{serverService}.{category}
//	服务端策略: {serverService}.{category}
//
// category 可选值: retry, rpc_timeout, circuit_break, limit, degradation
//
// # 订阅健康可查（#315）
//
// ListenConfig / 首拉 GetConfig 失败不再只打一条 Warn：失败被记录进
// [GovernanceConfig.SubscriptionStates]（Active/LastError/Attempts/NextRetry），
// 并启动后台指数退避重试（[WithGovernanceRetryBackoff] 调参）。治理层
// 降级因此可观测、可告警、可自愈，而不是「Nacos 抖动一次 → 永不恢复热更」。
type GovernanceConfig struct {
	templateDataID string
	templateTP     *governanceTimeoutProvider
	cli            config_client.IConfigClient
	group          string
	clientTmpl     string // 客户端 dataId 模板，支持 {client}、{server}、{category}
	serverTmpl     string // 服务端 dataId 模板，支持 {server}、{category}
	mu             sync.RWMutex
	callbacks      map[string]map[int64]func(string)
	nextID         int64
	listening      map[string]bool // 记录已注册监听器的 dataID，避免重复注册
	states         map[string]*subscriptionState
	// registerMu 串行化 subscribe 全流程；Close 也持有它，消除
	// "Cancel 之后才注册监听器"的泄漏窗口
	registerMu sync.Mutex

	retryBase time.Duration // 后台重试初始退避
	retryMax  time.Duration // 后台重试退避上限
	closeCh   chan struct{} // Close 关闭，终止全部重试 goroutine
	closed    bool
}

// subscriptionState 是单个 dataID 的订阅运行时状态（g.mu 保护）。
type subscriptionState struct {
	active      bool // 监听器已注册（首拉失败时仍为 true）
	lastErr     error
	attempts    int // 连续失败次数（成功清零）
	lastAttempt time.Time
	nextRetry   time.Time
	retrying    bool // 已有后台重试 goroutine 在跑
}

// SubscriptionState 是单个治理 dataID 的订阅健康快照（对外只读）。
type SubscriptionState struct {
	// DataID 是 Nacos 配置项 ID。
	DataID string
	// Group 是 Nacos 配置分组。
	Group string
	// Active 表示监听器已注册、热更通道可用。
	Active bool
	// LastError 是最近一次失败（nil = 健康）。
	LastError error
	// Attempts 是连续失败次数（成功后清零）。
	Attempts int
	// LastAttempt 是最近一次订阅/重试尝试时间。
	LastAttempt time.Time
	// NextRetry 是下一次后台重试时间（零值 = 无待执行重试）。
	NextRetry time.Time
}

// GovernanceOption 是 [NewGovernanceConfig] 的配置选项函数类型。
type GovernanceOption func(*GovernanceConfig)

// WithGovernanceGroup 设置 Nacos 配置分组，默认 "DEFAULT_GROUP"。
func WithGovernanceGroup(group string) GovernanceOption {
	return func(g *GovernanceConfig) { g.group = group }
}

// WithGovernanceRetryBackoff 设置订阅失败后的后台重试退避：初始 base、
// 上限 max。base <= 0 或 max < base 时使用缺省值（500ms / 30s）。
func WithGovernanceRetryBackoff(base, max time.Duration) GovernanceOption {
	return func(g *GovernanceConfig) {
		if base > 0 && max >= base {
			g.retryBase, g.retryMax = base, max
		}
	}
}

// WithGovernanceTemplate 订阅**统一治理模板**(governance 包 schema,
// audit 5.2 阶段 2):单 dataID 承载全部类别,template 中 timeout_ms 经
// service 感知的四档查找应用到 RPC 超时(经 TemplateTimeoutOptions 取
// client.Option,#213);retry/circuit_breaker/limiter 为 kitex 原生富结构,
// 仍走 per-category dataID。
func WithGovernanceTemplate(dataID string) GovernanceOption {
	return func(g *GovernanceConfig) { g.templateDataID = dataID }
}

// WithDataIDTemplate 设置 dataId 模板，支持占位符 {client}、{server}、{category}。
// 默认 "{client}.{server}.{category}"（客户端）和 "{server}.{category}"（服务端）。
//
// 服务端策略没有 client 维度，服务端模板由传入模板**剥离 {client} 占位符**
// 派生（占位符连同紧随其后的一个分隔符整体删除）——保证任意分隔符形态的
// 模板派生出的服务端模板都不残留未替换占位符（#199：此前只删字面量
// "{client}."，"{client}-{server}.{category}" 这类非点号模板渲染出的服务端
// dataId 残留 "{client}"，Nacos 上永远查不到配置，服务端治理静默失效）。
func WithDataIDTemplate(tmpl string) GovernanceOption {
	return func(g *GovernanceConfig) {
		g.clientTmpl = tmpl
		g.serverTmpl = stripClientSegment(tmpl)
	}
}

// stripClientSegment 从模板中剥离 {client} 占位符：连同紧随其后的一个
// dataId 分隔符（./-/_/:）一起删；{client} 收尾且带前置分隔符时连分隔符
// 一起删，避免悬挂分隔符。无分隔符相邻（直连形态）只删占位符本身。
func stripClientSegment(tmpl string) string {
	const ph = "{client}"
	out := tmpl
	for {
		i := strings.Index(out, ph)
		if i < 0 {
			return out
		}
		end := i + len(ph)
		if end < len(out) && isDataIDSeparator(out[end]) {
			end++
		} else if end == len(out) && i > 0 && isDataIDSeparator(out[i-1]) {
			i--
		}
		out = out[:i] + out[end:]
	}
}

func isDataIDSeparator(c byte) bool {
	switch c {
	case '.', '-', '_', '/', ':':
		return true
	}
	return false
}

// NewGovernanceConfig 创建治理配置客户端。
//
// cli 为 ekit [nacos.NewConfigClient] 返回的配置客户端，与业务配置共享同一连接。
func NewGovernanceConfig(cli config_client.IConfigClient, opts ...GovernanceOption) (*GovernanceConfig, error) {
	if cli == nil {
		return nil, ekiterr.New("kitex governance: config client must not be nil")
	}
	g := &GovernanceConfig{
		cli:        cli,
		group:      "DEFAULT_GROUP",
		clientTmpl: "{client}.{server}.{category}",
		serverTmpl: "{server}.{category}",
		callbacks:  make(map[string]map[int64]func(string)),
		listening:  make(map[string]bool),
		states:     make(map[string]*subscriptionState),
		retryBase:  defaultRetryBase,
		retryMax:   defaultRetryMax,
		closeCh:    make(chan struct{}),
	}
	for _, opt := range opts {
		opt(g)
	}
	if g.templateDataID != "" {
		g.templateTP = &governanceTimeoutProvider{}
		dataID := g.templateDataID
		if err := g.subscribe(dataID, func(data string) {
			cfg, err := governance.Parse([]byte(data))
			if err != nil {
				govLog.Error("parse governance template failed", "dataId", dataID, logx.ErrKey, err)
				return
			}
			// 全量同步语义：模板删掉 timeout_ms 段 = 清空上一轮超时
			// （provider 对无命中的 lookup 返回 nil，kitex 回退默认超时），
			// 此前 len==0 直接 return 导致旧超时永久残留
			g.templateTP.store(cfg)
			govLog.Info("governance template timeout updated", "dataId", dataID, "keys", len(cfg.TimeoutMs))
		}); err != nil {
			govLog.Warn("subscribe governance template failed", "dataId", dataID, logx.ErrKey, err)
		}
	}
	return g, nil
}

// TemplateTimeoutOptions 返回统一模板 timeout_ms 的 client.Option;
// 未启用 WithGovernanceTemplate 时返回 nil。
//
// 返回的 TimeoutProvider 每次 RPC 按 governance schema 的四档有序查找
// （精确 "svc:Method" → 服务级 "svc" → service 点段前缀最长 → 全局 ""）
// 取超时——service 维度在查找面内被正确消费（#213：旧的 key 翻译把
// service 维度整个丢掉，多个 service 级 key 塌进同一个 "*" 槽位，生效
// 超时随 map 遍历序随机漂移且跨服务串味）；无命中返回 nil，kitex 回退
// 默认超时。
func (g *GovernanceConfig) TemplateTimeoutOptions() []client.Option {
	if g.templateTP == nil {
		return nil
	}
	return []client.Option{client.WithTimeoutProvider(g.templateTP)}
}

// governanceTimeoutProvider 实现 rpcinfo.TimeoutProvider：持有最新一轮
// 统一治理模板（热更整表原子换新），每次 RPC 按 schema 四档查找取超时。
// kitex 客户端对 Timeouts 返回 nil 的处理是回退默认超时（client.go:
// `if c := p.Timeouts(ri); c != nil`），无命中即默认，无需本地兜底。
type governanceTimeoutProvider struct {
	cfg atomic.Pointer[governance.Config]
}

func (p *governanceTimeoutProvider) store(cfg *governance.Config) { p.cfg.Store(cfg) }

// Timeouts 按 (目的服务, 方法) 做四档查找。模板只有总超时:连接超时取
// 固定 100ms,其余给 RPC（与旧翻译路径同口径）。
func (p *governanceTimeoutProvider) Timeouts(ri rpcinfo.RPCInfo) rpcinfo.Timeouts {
	cfg := p.cfg.Load()
	if cfg == nil {
		return nil
	}
	svc, method := "", ""
	if ri != nil {
		if to := ri.To(); to != nil {
			svc = to.ServiceName()
		}
		if inv := ri.Invocation(); inv != nil {
			method = inv.MethodName()
		}
	}
	ms, ok := cfg.LookupTimeout(svc, method)
	if !ok {
		return nil
	}
	conn := int64(100)
	if ms < int(conn) {
		conn = int64(ms) / 2
	}
	return &rpctimeout.RPCTimeout{ConnTimeoutMS: int(conn), RPCTimeoutMS: ms}
}

// clientDataID 渲染客户端策略的 dataId。
func (g *GovernanceConfig) clientDataID(client, server, category string) string {
	id := g.clientTmpl
	id = replacePlaceholder(id, "{client}", client)
	id = replacePlaceholder(id, "{server}", server)
	id = replacePlaceholder(id, "{category}", category)
	return id
}

// serverDataID 渲染服务端策略的 dataId。
func (g *GovernanceConfig) serverDataID(server, category string) string {
	id := g.serverTmpl
	id = replacePlaceholder(id, "{server}", server)
	id = replacePlaceholder(id, "{category}", category)
	return id
}

// replacePlaceholder 替换模板中的占位符。
func replacePlaceholder(s, placeholder, value string) string {
	return strings.Replace(s, placeholder, value, 1)
}

// Close 取消本 GovernanceConfig 注册的全部 Nacos 监听并清空订阅状态。
//
// 动态创建依赖 client 的场景（如网关按需建 client）会随时间累积监听器，
// 在 client 全部关闭后调用 Close 释放治理订阅。Close 后对象仍可复用：
// 下一次 subscribe 会重新注册监听，但未重新订阅前不再收到任何回调。
//
// 注意：nacos 的 CancelListenConfig 以 dataId 为粒度，会一并移除该 dataId
// 上其他监听者——请勿对同一 dataId 混用不同 GovernanceConfig 实例。
func (g *GovernanceConfig) Close() error {
	// 先排空在途 subscribe：否则 Cancel 之后 subscribe 才注册监听器 = 泄漏
	g.registerMu.Lock()
	defer g.registerMu.Unlock()

	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return nil
	}
	g.closed = true
	if g.closeCh != nil {
		// 关闭后后台重试 goroutine 全部退出；下次 subscribe 会重建通道。
		close(g.closeCh)
	}
	dataIDs := make([]string, 0, len(g.listening))
	for id := range g.listening {
		dataIDs = append(dataIDs, id)
	}
	g.callbacks = make(map[string]map[int64]func(string))
	g.listening = make(map[string]bool)
	for _, st := range g.states {
		st.active = false
		st.retrying = false
		st.nextRetry = time.Time{}
	}
	g.mu.Unlock()

	var firstErr error
	for _, id := range dataIDs {
		if err := g.cli.CancelListenConfig(vo.ConfigParam{
			DataId: id,
			Group:  g.group,
		}); err != nil {
			govLog.Warn("cancel listen failed", "dataId", id, logx.ErrKey, err)
			if firstErr == nil {
				firstErr = ekiterr.Wrapf(err, "cancel listen %s", id)
			}
		}
	}
	return firstErr
}

// SubscriptionStates 返回全部已订阅 dataID 的健康快照（按 DataID 排序）。
// 用于启动自检、健康检查与告警：LastError != nil 或 Active=false 表示该
// 策略通道降级（#315）。
func (g *GovernanceConfig) SubscriptionStates() []SubscriptionState {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]SubscriptionState, 0, len(g.states))
	for id, st := range g.states {
		out = append(out, SubscriptionState{
			DataID:      id,
			Group:       g.group,
			Active:      st.active,
			LastError:   st.lastErr,
			Attempts:    st.attempts,
			LastAttempt: st.lastAttempt,
			NextRetry:   st.nextRetry,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DataID < out[j].DataID })
	return out
}

// SubscriptionState 返回指定 dataID 的健康快照。
func (g *GovernanceConfig) SubscriptionState(dataID string) (SubscriptionState, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	st, ok := g.states[dataID]
	if !ok {
		return SubscriptionState{}, false
	}
	return SubscriptionState{
		DataID:      dataID,
		Group:       g.group,
		Active:      st.active,
		LastError:   st.lastErr,
		Attempts:    st.attempts,
		LastAttempt: st.lastAttempt,
		NextRetry:   st.nextRetry,
	}, true
}

// Degraded 报告是否有任意订阅处于降级状态（监听器缺失或最近一次拉取
// 失败）。Close 之后全部订阅标记为 inactive，故也返回 true。
func (g *GovernanceConfig) Degraded() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, st := range g.states {
		if !st.active || st.lastErr != nil {
			return true
		}
	}
	return false
}

// subscribe 订阅 Nacos 配置变更。首次调用时立即拉取一次并回调，后续变更自动回调。
// 同一 dataID 只注册一次 Nacos 监听器，多次 subscribe 只追加回调。
//
// 失败语义（#315）：ListenConfig 失败返回 error 并保留回调、启动后台退避
// 重试；首拉 GetConfig 失败只记录 / 重试（监听器已可用，稍后可能恢复），
// 返回 nil。两种失败的运行态都经 [GovernanceConfig.SubscriptionStates] 可查。
func (g *GovernanceConfig) subscribe(dataID string, callback func(string)) error {
	// registerMu 串行化整个订阅流程（订阅只发生在 Suite 构造期）：
	// 并发首注册 + 一方 ListenConfig 失败时，失败方回滚 listening 标记
	// 但保留全部回调，排队中的订阅者随后重新评估 needListen 并自己完成
	// 注册；后台重试也会用保留的回调补注册，不会再有人留在
	// listening=true 但无监听器的死态。
	// 注意不能改持 g.mu 过 ListenConfig：OnChange 可能同步触发（nacos
	// SDK 异步，但任何同步桩都会经 RLock 自锁死）。
	g.registerMu.Lock()
	defer g.registerMu.Unlock()

	g.mu.Lock()
	if g.closed {
		// Close 后可复用：重建关闭通道，旧重试 goroutine 绑旧通道自然退出。
		g.closed = false
		g.closeCh = make(chan struct{})
	}
	g.nextID++
	id := g.nextID
	if g.callbacks[dataID] == nil {
		g.callbacks[dataID] = make(map[int64]func(string))
	}
	g.callbacks[dataID][id] = callback
	needListen := !g.listening[dataID]
	g.listening[dataID] = true
	st := g.ensureStateLocked(dataID)
	st.lastAttempt = time.Now()
	g.mu.Unlock()

	// 先注册监听，再做首拉（at-least-once 语义）：注册与首拉之间到达的变更
	// 由监听补发；四类策略回调均为覆盖运行时状态的幂等操作，重复投递无副作用。
	// 只注册一次监听器，避免同一 dataID 重复注册导致 N×N 回调
	if needListen {
		err := g.cli.ListenConfig(vo.ConfigParam{
			DataId:   dataID,
			Group:    g.group,
			OnChange: g.changeHandler(dataID),
		})
		if err != nil {
			// 只回滚 listening 标记，保留回调：后台重试要用它们补注册。
			g.mu.Lock()
			g.listening[dataID] = false
			g.mu.Unlock()
			g.recordFailure(dataID, err)
			return ekiterr.Wrapf(err, "listen config %s", dataID)
		}
		g.mu.Lock()
		if st := g.states[dataID]; st != nil {
			st.active = true
		}
		g.mu.Unlock()
	}

	// 首拉：listen 成功后进行，保证失败时不留下「已应用配置但无热更通道」的状态
	content, err := g.cli.GetConfig(vo.ConfigParam{
		DataId: dataID,
		Group:  g.group,
	})
	if err != nil {
		// 监听器已注册，热更通道可用；本次首拉失败按降级记录并后台重试。
		govLog.Warn("get initial config failed", "dataId", dataID, logx.ErrKey, err)
		g.recordFailure(dataID, err)
		return nil
	}
	g.recordSuccess(dataID)
	if content != "" {
		callback(content)
	}
	return nil
}

// changeHandler 返回 Nacos OnChange 回调：空 content 归一成 "{}"（#197 删除
// 语义），锁内拷贝后再遍历（避免与 subscribe 写入并发）。
func (g *GovernanceConfig) changeHandler(dataID string) func(_, _, _, data string) {
	return func(_, _, _, data string) {
		if strings.TrimSpace(data) == "" {
			data = "{}"
		}
		g.mu.RLock()
		handlers := make([]func(string), 0, len(g.callbacks[dataID]))
		for _, h := range g.callbacks[dataID] {
			handlers = append(handlers, h)
		}
		g.mu.RUnlock()
		for _, h := range handlers {
			h(data)
		}
	}
}

// ensureStateLocked 返回 dataID 的状态记录（调用方持 g.mu 写锁）。
func (g *GovernanceConfig) ensureStateLocked(dataID string) *subscriptionState {
	if g.states == nil {
		g.states = make(map[string]*subscriptionState)
	}
	st := g.states[dataID]
	if st == nil {
		st = &subscriptionState{}
		g.states[dataID] = st
	}
	return st
}

// recordFailure 记录一次订阅失败并调度后台退避重试。
func (g *GovernanceConfig) recordFailure(dataID string, err error) {
	g.mu.Lock()
	st := g.ensureStateLocked(dataID)
	st.active = g.listening[dataID]
	st.lastErr = err
	st.attempts++
	st.lastAttempt = time.Now()
	attempts := st.attempts
	g.mu.Unlock()
	g.scheduleRetry(dataID, attempts)
}

// recordSuccess 清空降级状态。
func (g *GovernanceConfig) recordSuccess(dataID string) {
	g.mu.Lock()
	st := g.ensureStateLocked(dataID)
	st.active = true
	st.lastErr = nil
	st.attempts = 0
	st.lastAttempt = time.Now()
	st.nextRetry = time.Time{}
	g.mu.Unlock()
}

// scheduleRetry 为该 dataID 启动（若尚未运行）后台重试 goroutine。
func (g *GovernanceConfig) scheduleRetry(dataID string, attempts int) {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	st := g.ensureStateLocked(dataID)
	if st.retrying {
		g.mu.Unlock()
		return
	}
	st.retrying = true
	delay := g.backoffLocked(attempts)
	st.nextRetry = time.Now().Add(delay)
	if g.closeCh == nil {
		g.closeCh = make(chan struct{})
	}
	ch := g.closeCh
	g.mu.Unlock()

	govLog.Warn("governance subscription degraded, retrying in background",
		"dataId", dataID, "attempts", attempts, "nextRetry", delay)
	go g.retryLoop(dataID, ch, delay)
}

// finishRetry 清理重试态；仅当仍绑定当前关闭通道时执行，避免 Close 后
// 重新订阅产生的新重试循环被旧 goroutine 清掉 retrying 标记。
func (g *GovernanceConfig) finishRetry(dataID string, closeCh <-chan struct{}) {
	g.mu.Lock()
	if g.closeCh == closeCh {
		if st := g.states[dataID]; st != nil {
			st.retrying = false
			st.nextRetry = time.Time{}
		}
	}
	g.mu.Unlock()
}

// backoffLocked 计算第 attempts 次失败后的退避（指数增长、封顶 retryMax）。
func (g *GovernanceConfig) backoffLocked(attempts int) time.Duration {
	base, max := g.retryBase, g.retryMax
	if base <= 0 {
		base = defaultRetryBase
	}
	if max < base {
		max = defaultRetryMax
	}
	d := base
	for i := 1; i < attempts; i++ {
		if d >= max/2 {
			return max
		}
		d *= 2
	}
	if d > max {
		d = max
	}
	return d
}

// retryLoop 按退避重试直到成功、Close 或对象被重新订阅（旧通道关闭）。
func (g *GovernanceConfig) retryLoop(dataID string, closeCh <-chan struct{}, delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-closeCh:
			g.finishRetry(dataID, closeCh)
			return
		case <-timer.C:
		}

		// Close 后对象被重新订阅会换新通道：旧循环必须退出，不能拿旧
		// dataID 再次注册监听器（会复活已 Cancel 的订阅）。
		g.mu.Lock()
		stale := g.closed || g.closeCh != closeCh
		g.mu.Unlock()
		if stale {
			g.finishRetry(dataID, closeCh)
			return
		}

		err := g.retryConnect(dataID)
		if err == nil {
			g.finishRetry(dataID, closeCh)
			govLog.Info("governance subscription recovered", "dataId", dataID)
			return
		}

		g.mu.Lock()
		st := g.ensureStateLocked(dataID)
		attempts := st.attempts
		delay = g.backoffLocked(attempts)
		st.nextRetry = time.Now().Add(delay)
		g.mu.Unlock()
		timer.Reset(delay)
	}
}

// retryConnect 执行一次重连：缺监听器先补 ListenConfig，再拉一次首配置。
// 成功时清空降级状态并把配置分发给保留的回调。
func (g *GovernanceConfig) retryConnect(dataID string) error {
	// 与 subscribe/Close 串行化，避免与 Cancel 交叉注册。
	g.registerMu.Lock()
	defer g.registerMu.Unlock()

	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return ekiterr.New("governance config closed")
	}
	needListen := !g.listening[dataID]
	g.mu.Unlock()

	if needListen {
		if err := g.cli.ListenConfig(vo.ConfigParam{
			DataId:   dataID,
			Group:    g.group,
			OnChange: g.changeHandler(dataID),
		}); err != nil {
			g.recordFailure(dataID, err)
			return err
		}
		g.mu.Lock()
		g.listening[dataID] = true
		g.mu.Unlock()
	}

	content, err := g.cli.GetConfig(vo.ConfigParam{
		DataId: dataID,
		Group:  g.group,
	})
	if err != nil {
		g.recordFailure(dataID, err)
		return err
	}
	g.recordSuccess(dataID)
	if content != "" {
		g.mu.RLock()
		handlers := make([]func(string), 0, len(g.callbacks[dataID]))
		for _, h := range g.callbacks[dataID] {
			handlers = append(handlers, h)
		}
		g.mu.RUnlock()
		for _, h := range handlers {
			h(content)
		}
	}
	return nil
}
