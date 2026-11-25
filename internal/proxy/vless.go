// Package proxy 为 GitHub / Google 等境外 OAuth 出站提供代理（移植自 think-tpad）：
//   - vless://   进程内嵌 xray 实例（无需外部客户端）
//   - http(s)://、socks5://  标准代理（如本机 Clash/V2RayN 的 7890/1080 端口）
//
// 未配置（空串）时调用方走直连。xray 实例按配置指纹懒重建：相同配置重复调用无开销；
// 更换实例时旧实例延迟关闭，避免打断在途请求。
package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	xnet "github.com/xtls/xray-core/common/net"
	core "github.com/xtls/xray-core/core"
	_ "github.com/xtls/xray-core/main/distro/all" // 注册 JSON 配置加载器与全部协议实现
)

// oldInstanceGrace 是旧实例被替换后的延迟关闭窗口，用于让在途请求自然结束。
const oldInstanceGrace = 2 * time.Minute

// VLESSNode 是从 vless:// 分享链接解析出的节点参数。
type VLESSNode struct {
	UUID     string
	Host     string
	Port     int
	Flow     string
	Security string // "" / "none" / "tls" / "reality"
	SNI      string
	// Fingerprint 是 uTLS 指纹，tls/reality 默认 chrome。
	Fingerprint string
	PublicKey   string
	ShortID     string
}

// ServerPreview 返回用于管理界面展示的 host:port，不含任何凭据。
func (n VLESSNode) ServerPreview() string {
	return net.JoinHostPort(n.Host, strconv.Itoa(n.Port))
}

// ParseVLESS 解析并校验 vless:// 分享链接。
// 仅支持 TCP 传输 + none/tls/reality 安全层；其余组合（ws/grpc 等）明确报错而非静默忽略。
func ParseVLESS(raw string) (VLESSNode, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return VLESSNode{}, errors.New("VLESS 链接为空")
	}
	u, err := url.Parse(raw)
	if err != nil {
		// 不包裹原始错误：其中带完整链接（UUID 即节点凭据）
		return VLESSNode{}, errors.New("VLESS 链接无法解析")
	}
	if !strings.EqualFold(u.Scheme, "vless") {
		return VLESSNode{}, fmt.Errorf("链接协议必须是 vless://，当前为 %q", u.Scheme)
	}
	node := VLESSNode{UUID: u.User.Username()}
	if node.UUID == "" {
		return VLESSNode{}, errors.New("VLESS 链接缺少用户 UUID")
	}
	if _, err := uuid.Parse(node.UUID); err != nil {
		return VLESSNode{}, fmt.Errorf("VLESS 用户 UUID 格式非法: %v", err)
	}
	node.Host = u.Hostname()
	if node.Host == "" {
		return VLESSNode{}, errors.New("VLESS 链接缺少服务器地址")
	}
	portStr := u.Port()
	if portStr == "" {
		return VLESSNode{}, errors.New("VLESS 链接缺少端口")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return VLESSNode{}, fmt.Errorf("VLESS 端口非法: %q", portStr)
	}
	node.Port = port

	q := u.Query()
	if network := strings.ToLower(strings.TrimSpace(q.Get("type"))); network != "" && network != "tcp" {
		return VLESSNode{}, fmt.Errorf("暂不支持 %q 传输方式，仅支持 tcp", network)
	}
	node.Security = strings.ToLower(strings.TrimSpace(q.Get("security")))
	if node.Security == "none" {
		node.Security = ""
	}
	switch node.Security {
	case "", "tls", "reality":
	default:
		return VLESSNode{}, fmt.Errorf("暂不支持 %q 安全层，仅支持 none/tls/reality", node.Security)
	}
	node.Flow = strings.TrimSpace(q.Get("flow"))
	if node.Flow != "" {
		if node.Flow != "xtls-rprx-vision" {
			return VLESSNode{}, fmt.Errorf("暂不支持 %q 流控，仅支持 xtls-rprx-vision", node.Flow)
		}
		if node.Security == "" {
			return VLESSNode{}, errors.New("xtls-rprx-vision 流控要求 security 为 tls 或 reality")
		}
	}
	node.Fingerprint = strings.TrimSpace(q.Get("fp"))
	if node.Fingerprint == "" {
		node.Fingerprint = "chrome"
	}
	node.SNI = strings.TrimSpace(q.Get("sni"))
	if node.SNI == "" && node.Security != "" {
		// reality/tls 必须有 serverName；缺省回落到服务器地址（域名节点常见写法）。
		node.SNI = node.Host
	}
	node.PublicKey = strings.TrimSpace(q.Get("pbk"))
	node.ShortID = strings.TrimSpace(q.Get("sid"))
	if node.Security == "reality" {
		if node.PublicKey == "" {
			return VLESSNode{}, errors.New("reality 安全层缺少 pbk（公钥）参数")
		}
		// reality 公钥是 base64url 无填充编码的 32 字节 X25519 公钥；
		// 解析期校验，让管理员保存时即时报错，而非运行时由 xray 拒绝。
		if raw, err := base64.RawURLEncoding.DecodeString(node.PublicKey); err != nil || len(raw) != 32 {
			return VLESSNode{}, errors.New("pbk 公钥格式非法（应为 base64url 编码的 32 字节）")
		}
		if node.ShortID != "" {
			if len(node.ShortID) > 16 {
				return VLESSNode{}, errors.New("sid（shortId）最长 16 个十六进制字符")
			}
			if _, err := hex.DecodeString(node.ShortID); err != nil {
				return VLESSNode{}, errors.New("sid（shortId）必须是十六进制字符")
			}
		}
	}
	return node, nil
}

// buildConfig 把节点参数组装成 xray JSON 配置；仅出站，无入站（走 core.Dial 直拨）。
func buildConfig(node VLESSNode) ([]byte, error) {
	user := map[string]any{"id": node.UUID, "encryption": "none"}
	if node.Flow != "" {
		user["flow"] = node.Flow
	}
	stream := map[string]any{"network": "tcp"}
	switch node.Security {
	case "reality":
		stream["security"] = "reality"
		stream["realitySettings"] = map[string]any{
			"serverName":  node.SNI,
			"fingerprint": node.Fingerprint,
			"publicKey":   node.PublicKey,
			"shortId":     node.ShortID,
		}
	case "tls":
		stream["security"] = "tls"
		stream["tlsSettings"] = map[string]any{
			"serverName":  node.SNI,
			"fingerprint": node.Fingerprint,
		}
	}
	cfg := map[string]any{
		"log": map[string]any{"loglevel": "error"},
		"outbounds": []any{map[string]any{
			"protocol": "vless",
			"settings": map[string]any{
				"vnext": []any{map[string]any{
					"address": node.Host,
					"port":    node.Port,
					"users":   []any{user},
				}},
			},
			"streamSettings": stream,
		}},
	}
	return json.Marshal(cfg)
}

// Manager 持有当前生效的出站资源（内嵌 xray 实例 / 标准代理 Transport），按配置指纹懒重建；并发安全。
// Transport 按配置复用：每次登录新建 Transport 会让连接池无法复用、空闲连接堆积到超时才释放。
// 配置切换（vless ↔ http/socks5 ↔ 关闭）时，旧实例与旧 Transport 在宽限期后关闭，不打断在途请求。
type Manager struct {
	mu      sync.Mutex
	key     string          // 当前 xray 实例的配置指纹
	inst    *core.Instance  // 当前 xray 实例
	vlessTr *http.Transport // 绑定 inst 的 Transport
	stdKey  string          // 当前标准代理的配置指纹
	stdTr   *http.Transport // 标准代理 Transport
}

func NewManager() *Manager { return &Manager{} }

func fingerprint(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// retire 宽限期后关闭被替换的实例与 Transport
func retire(inst *core.Instance, tr *http.Transport) {
	if inst == nil && tr == nil {
		return
	}
	time.AfterFunc(oldInstanceGrace, func() {
		if tr != nil {
			tr.CloseIdleConnections()
		}
		if inst != nil {
			_ = inst.Close()
		}
	})
}

// retireVLESSLocked / retireStdLocked 调用方须持有 m.mu
func (m *Manager) retireVLESSLocked() {
	retire(m.inst, m.vlessTr)
	m.inst, m.vlessTr, m.key = nil, nil, ""
}

func (m *Manager) retireStdLocked() {
	retire(nil, m.stdTr)
	m.stdTr, m.stdKey = nil, ""
}

// HTTPClient 返回经 rawURL 代理出站的 http.Client；rawURL 为空返回直连 client（并回收已不用的代理资源）。
// 支持 vless:// / http(s):// / socks5(h)://。
// 配置非法或实例启动失败时返回 error，调用方应拒绝请求而不是静默降级直连——
// 管理员显式配置代理后，静默直连会掩盖「代理已失效」的真实状态。
func (m *Manager) HTTPClient(rawURL string, timeout time.Duration) (*http.Client, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		m.mu.Lock()
		m.retireVLESSLocked()
		m.retireStdLocked()
		m.mu.Unlock()
		return &http.Client{Timeout: timeout}, nil
	}
	var (
		tr  *http.Transport
		err error
	)
	if isVLESS(rawURL) {
		tr, err = m.ensure(rawURL)
	} else {
		tr, err = m.ensureStd(rawURL)
	}
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: tr, Timeout: timeout}, nil
}

// ensureStd 幂等：标准代理配置未变则复用 Transport；切到标准代理时回收 xray 实例。
func (m *Manager) ensureStd(rawURL string) (*http.Transport, error) {
	key := fingerprint(rawURL)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stdTr != nil && m.stdKey == key {
		return m.stdTr, nil
	}
	tr, err := stdTransport(rawURL)
	if err != nil {
		return nil, err
	}
	m.retireStdLocked()
	m.retireVLESSLocked()
	m.stdTr, m.stdKey = tr, key
	return tr, nil
}

// IsolatedClient 为一次性用途（如管理员拨测未保存的链接）创建独立实例，
// 不触碰共享实例；返回的 close 必须在用完后调用以释放实例。
func (m *Manager) IsolatedClient(rawURL string, timeout time.Duration) (*http.Client, func(), error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL != "" && !isVLESS(rawURL) {
		tr, err := stdTransport(rawURL)
		if err != nil {
			return nil, nil, err
		}
		return &http.Client{Transport: tr, Timeout: timeout}, tr.CloseIdleConnections, nil
	}
	vlessURL := rawURL
	node, err := ParseVLESS(vlessURL)
	if err != nil {
		return nil, nil, err
	}
	cfgJSON, err := buildConfig(node)
	if err != nil {
		return nil, nil, fmt.Errorf("代理配置序列化失败: %w", err)
	}
	inst, err := core.StartInstance("json", cfgJSON)
	if err != nil {
		return nil, nil, fmt.Errorf("内嵌代理启动失败: %w", err)
	}
	tr := transportForInstance(inst)
	return &http.Client{Transport: tr, Timeout: timeout}, func() {
		tr.CloseIdleConnections()
		_ = inst.Close()
	}, nil
}

// transportForInstance 构造经指定 xray 实例出站的 Transport。
// 闭包捕获实例本身：实例被替换时在途请求仍走旧实例，直至其延迟关闭窗口结束。
func transportForInstance(inst *core.Instance) *http.Transport {
	return &http.Transport{
		// 显式置空 Proxy：经 xray 出站的请求不得再叠加环境变量里的 HTTP(S)_PROXY。
		Proxy: nil,
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			host, portStr, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			port, err := strconv.Atoi(portStr)
			if err != nil {
				return nil, fmt.Errorf("目标端口非法: %q", portStr)
			}
			return core.Dial(ctx, inst, xnet.Destination{
				Network: xnet.Network_TCP,
				Address: xnet.ParseAddress(host),
				Port:    xnet.Port(port),
			})
		},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// ensure 幂等：配置指纹未变则复用当前实例与 Transport；变化则重建，旧资源宽限期后关闭。
func (m *Manager) ensure(vlessURL string) (*http.Transport, error) {
	key := fingerprint(vlessURL)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inst != nil && m.key == key {
		return m.vlessTr, nil
	}
	node, err := ParseVLESS(vlessURL)
	if err != nil {
		return nil, err
	}
	cfgJSON, err := buildConfig(node)
	if err != nil {
		return nil, fmt.Errorf("代理配置序列化失败: %w", err)
	}
	inst, err := core.StartInstance("json", cfgJSON)
	if err != nil {
		return nil, fmt.Errorf("内嵌代理启动失败: %w", err)
	}
	m.retireVLESSLocked()
	m.retireStdLocked()
	m.inst, m.key, m.vlessTr = inst, key, transportForInstance(inst)
	return m.vlessTr, nil
}

// Close 立即关闭全部代理资源；进程退出前可调用，非必须。
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.vlessTr != nil {
		m.vlessTr.CloseIdleConnections()
	}
	if m.inst != nil {
		_ = m.inst.Close()
	}
	if m.stdTr != nil {
		m.stdTr.CloseIdleConnections()
	}
	m.inst, m.vlessTr, m.key = nil, nil, ""
	m.stdTr, m.stdKey = nil, ""
}
