package kitex

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/pkg/rpcinfo"
	"github.com/cloudwego/kitex/pkg/rpctimeout"
	"github.com/oekit/core/governance"

	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
	"github.com/oekit/logx"
)

var govLog = logx.Delegating("kitex-gov")

// GovernanceConfig 从 Nacos 配置中心加载 Kitex 治理策略（retry/rpc_timeout/circuit_breaker/limiter）
// 并在配置变更时热更新到 Kitex 运行时。
//
// 每类策略对应一个独立的 Nacos 配置项（dataId），格式为：
//
//	客户端策略: {clientService}.{serverService}.{category}
//	服务端策略: {serverService}.{category}
//
// category 可选值: retry, rpc_timeout, circuit_break, limit, degradation
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
	// registerMu 串行化 subscribe 全流程；Close 也持有它，消除
	// "Cancel 之后才注册监听器"的泄漏窗口
	registerMu sync.Mutex
}

// GovernanceOption 是 [NewGovernanceConfig] 的配置选项函数类型。
type GovernanceOption func(*GovernanceConfig)

// WithGovernanceGroup 设置 Nacos 配置分组，默认 "DEFAULT_GROUP"。
func WithGovernanceGroup(group string) GovernanceOption {
	return func(g *GovernanceConfig) { g.group = group }
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
		return nil, fmt.Errorf("kitex governance: config client must not be nil")
	}
	g := &GovernanceConfig{
		cli:        cli,
		group:      "DEFAULT_GROUP",
		clientTmpl: "{client}.{server}.{category}",
		serverTmpl: "{server}.{category}",
		callbacks:  make(map[string]map[int64]func(string)),
		listening:  make(map[string]bool),
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
	dataIDs := make([]string, 0, len(g.listening))
	for id := range g.listening {
		dataIDs = append(dataIDs, id)
	}
	g.callbacks = make(map[string]map[int64]func(string))
	g.listening = make(map[string]bool)
	g.mu.Unlock()

	var firstErr error
	for _, id := range dataIDs {
		if err := g.cli.CancelListenConfig(vo.ConfigParam{
			DataId: id,
			Group:  g.group,
		}); err != nil {
			govLog.Warn("cancel listen failed", "dataId", id, logx.ErrKey, err)
			if firstErr == nil {
				firstErr = fmt.Errorf("cancel listen %s: %w", id, err)
			}
		}
	}
	return firstErr
}

// subscribe 订阅 Nacos 配置变更。首次调用时立即拉取一次并回调，后续变更自动回调。
// 同一 dataID 只注册一次 Nacos 监听器，多次 subscribe 只追加回调。
func (g *GovernanceConfig) subscribe(dataID string, callback func(string)) error {
	// registerMu 串行化整个订阅流程（订阅只发生在 Suite 构造期）：
	// 并发首注册 + 一方 ListenConfig 失败时，失败的回滚会清空该 dataID
	// 的全部回调与监听标记——排队中的订阅者随后重新评估 needListen 并
	// 自己完成注册，不会再有人留在 listening=true 但无监听器的死态
	// （此前 B 跳过注册、A 失败只回滚自己，B 的回调留在 map 里却永远
	// 收不到热更、也拿不到错误）。
	// 注意不能改持 g.mu 过 ListenConfig：OnChange 可能同步触发（nacos
	// SDK 异步，但任何同步桩都会经 RLock 自锁死）。
	g.registerMu.Lock()
	defer g.registerMu.Unlock()

	g.mu.Lock()
	g.nextID++
	id := g.nextID
	if g.callbacks[dataID] == nil {
		g.callbacks[dataID] = make(map[int64]func(string))
	}
	g.callbacks[dataID][id] = callback
	needListen := !g.listening[dataID]
	g.listening[dataID] = true
	g.mu.Unlock()

	// 先注册监听，再做首拉（at-least-once 语义）：注册与首拉之间到达的变更
	// 由监听补发；四类策略回调均为覆盖运行时状态的幂等操作，重复投递无副作用。
	// 只注册一次监听器，避免同一 dataID 重复注册导致 N×N 回调
	if needListen {
		err := g.cli.ListenConfig(vo.ConfigParam{
			DataId: dataID,
			Group:  g.group,
			OnChange: func(_, _, _, data string) {
				// Nacos 删除语义（#197）：Nacos 上删除配置时 SDK 投递的是空串
				// 而非空表（config_proxy.go 对 errorCode 300 写空文件后原样
				// 回调）——空串不是合法 JSON，会被各 handler 当成坏 JSON 保留
				// 上一轮旧策略，运维删配置等于什么都没恢复。归一成 "{}" 空表
				// 走正常全量同步路径：retry/熔断被 remove 回默认、rpc_timeout
				// 整表替换回默认、limit {0,0} 解除限流，与首拉路径（下方
				// content != "" 跳过）的「空 = 无配置」口径一致。
				if strings.TrimSpace(data) == "" {
					data = "{}"
				}
				// 锁内拷贝后再遍历：直接持有 map 遍历会与 subscribe 的
				// 写入并发，触发 concurrent map iteration and write panic
				g.mu.RLock()
				handlers := make([]func(string), 0, len(g.callbacks[dataID]))
				for _, h := range g.callbacks[dataID] {
					handlers = append(handlers, h)
				}
				g.mu.RUnlock()
				for _, h := range handlers {
					h(data)
				}
			},
		})
		if err != nil {
			// 回滚该 dataID 的全部订阅状态（含并发期间注册的回调——没有
			// 监听器它们永远收不到热更），否则重试时会因 listening=true
			// 跳过监听器注册，该 dataID 永久失去热更新
			g.mu.Lock()
			g.listening[dataID] = false
			delete(g.callbacks, dataID)
			g.mu.Unlock()
			return fmt.Errorf("listen config %s: %w", dataID, err)
		}
	}

	// 首拉：listen 成功后进行，保证失败时不留下「已应用配置但无热更通道」的状态
	content, err := g.cli.GetConfig(vo.ConfigParam{
		DataId: dataID,
		Group:  g.group,
	})
	if err != nil {
		govLog.Warn("get initial config failed", "dataId", dataID, logx.ErrKey, err)
	} else if content != "" {
		callback(content)
	}
	return nil
}
