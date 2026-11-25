package handler

// OpenID Connect（在 OAuth2 授权码模式之上）：id_token（RS256）、发现文档、JWKS。
//
//	GET /.well-known/openid-configuration      OIDC 发现文档
//	GET /.well-known/oauth-authorization-server RFC 8414（内容相同）
//	GET /oauth/jwks                             id_token 验签公钥
//
// 签名密钥：RSA-2048，首次使用时生成，用会话密钥派生的 AES-GCM 加密后存 system_settings，
// 多实例共用同一把；会话密钥变更导致无法解密时自动换新（id_token 短期有效，影响可忽略）。

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	settingOIDCSigningKey = "oauth_oidc_signing_key"
	oidcIDTokenTTL        = time.Hour
	oidcKeyPurpose        = "oauth-oidc-signing-key"
)

// oidcIssuer 签发者标识：优先「系统设置 → 站点域名」（生产务必配置，保证各入口一致）；
// 未配置时按本次请求推导（兼容反向代理 X-Forwarded-Proto）。
func (a *App) oidcIssuer(c *gin.Context) string {
	if d := strings.TrimRight(strings.TrimSpace(a.Settings().SiteDomain), "/"); d != "" {
		return d
	}
	scheme, host := oauthRequestOrigin(c)
	return scheme + "://" + host
}

// ---------- 签名密钥 ----------

func (a *App) sealWithPurpose(purpose string, plain []byte) (string, error) {
	block, err := aes.NewCipher(a.Signer.DeriveKey(purpose))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	return "v1:" + base64.RawStdEncoding.EncodeToString(gcm.Seal(nonce, nonce, plain, nil)), nil
}

func (a *App) openWithPurpose(purpose, sealed string) ([]byte, error) {
	raw, ok := strings.CutPrefix(sealed, "v1:")
	if !ok {
		return nil, errors.New("unknown format")
	}
	buf, err := base64.RawStdEncoding.DecodeString(raw)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(a.Signer.DeriveKey(purpose))
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(buf) < gcm.NonceSize() {
		return nil, errors.New("short")
	}
	return gcm.Open(nil, buf[:gcm.NonceSize()], buf[gcm.NonceSize():], nil)
}

func oidcKidOf(pub *rsa.PublicKey) string {
	der, _ := x509.MarshalPKIXPublicKey(pub)
	sum := sha256.Sum256(der)
	return base64.RawURLEncoding.EncodeToString(sum[:])[:16]
}

func (a *App) loadOIDCKeyFromDB() (*rsa.PrivateKey, error) {
	var row model.SystemSetting
	// 结构体条件由 GORM 负责给 key（MySQL 保留字）加引号
	if err := a.DB.Where(&model.SystemSetting{Key: settingOIDCSigningKey}).First(&row).Error; err != nil {
		return nil, err
	}
	der, err := a.openWithPurpose(oidcKeyPurpose, row.Value)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("not an RSA key")
	}
	return rk, nil
}

// oidcSigningKey 取签名密钥：内存缓存 → 库 → 生成并落库（并发首建以库中胜出者为准）
func (a *App) oidcSigningKey() (*rsa.PrivateKey, string, error) {
	a.oidcKeyMu.Lock()
	defer a.oidcKeyMu.Unlock()
	if a.oidcKey != nil {
		return a.oidcKey, a.oidcKid, nil
	}
	key, err := a.loadOIDCKeyFromDB()
	if err != nil {
		replace := !errors.Is(err, gorm.ErrRecordNotFound)
		if replace {
			log.Printf("[oidc] 签名密钥无法读取（会话密钥变更？），将重新生成: %v", err)
		}
		key, err = a.createOIDCKey(replace)
		if err != nil {
			return nil, "", err
		}
	}
	a.oidcKey, a.oidcKid = key, oidcKidOf(&key.PublicKey)
	return a.oidcKey, a.oidcKid, nil
}

func (a *App) createOIDCKey(overwrite bool) (*rsa.PrivateKey, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	sealed, err := a.sealWithPurpose(oidcKeyPurpose, der)
	if err != nil {
		return nil, err
	}
	row := model.SystemSetting{Key: settingOIDCSigningKey, Value: sealed}
	q := a.DB.Clauses(clause.OnConflict{DoNothing: true})
	if overwrite {
		q = a.DB.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"})})
	}
	if err := q.Create(&row).Error; err != nil {
		return nil, err
	}
	if overwrite {
		return key, nil
	}
	// 并发首建：另一实例可能先写入，以库中为准
	return a.loadOIDCKeyFromDB()
}

// ---------- id_token ----------

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// oidcAtHash OIDC Core §3.1.3.6：access_token SHA-256 左半部分
func oidcAtHash(accessToken string) string {
	sum := sha256.Sum256([]byte(accessToken))
	return b64url(sum[:16])
}

// userClaims 按 scope 生成用户资料声明（id_token 与 userinfo 共用，保证一致）
func (a *App) userClaims(c *gin.Context, user *model.User, scope string) map[string]any {
	nick := strings.TrimSpace(user.Nickname)
	if nick == "" {
		nick = user.Username
	}
	out := map[string]any{
		"sub":                strconv.FormatUint(uint64(user.ID), 10),
		"username":           user.Username,
		"preferred_username": user.Username,
		"nickname":           nick,
		"name":               nick,
	}
	if av := strings.TrimSpace(user.Avatar); av != "" {
		if strings.HasPrefix(av, "/") {
			av = a.oidcIssuer(c) + av // 站内相对路径转绝对，第三方才能直接显示
		}
		out["picture"] = av
	}
	if oauthHasScope(scope, oauthScopeEmail) {
		out["email"] = user.Email
		out["email_verified"] = user.Email != "" && user.EmailVerified
	}
	return out
}

func (a *App) signIDToken(c *gin.Context, user *model.User, clientID, scope, nonce, accessToken string, now time.Time) (string, error) {
	key, kid, err := a.oidcSigningKey()
	if err != nil {
		return "", err
	}
	claims := a.userClaims(c, user, scope)
	claims["iss"] = a.oidcIssuer(c)
	claims["aud"] = clientID
	claims["azp"] = clientID
	claims["iat"] = now.Unix()
	claims["exp"] = now.Add(oidcIDTokenTTL).Unix()
	if accessToken != "" {
		claims["at_hash"] = oidcAtHash(accessToken)
	}
	if nonce != "" {
		claims["nonce"] = nonce
	}
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid})
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signing := b64url(header) + "." + b64url(payload)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signing + "." + b64url(sig), nil
}

// ---------- 发现文档 / JWKS ----------

// OIDCDiscovery GET /.well-known/openid-configuration（及 /.well-known/oauth-authorization-server）
func (a *App) OIDCDiscovery(c *gin.Context) {
	iss := a.oidcIssuer(c)
	c.Header("Cache-Control", "public, max-age=300")
	authMethods := []string{"client_secret_basic", "client_secret_post", "none"}
	c.JSON(http.StatusOK, gin.H{
		"issuer":                                     iss,
		"authorization_endpoint":                     iss + "/oauth/authorize",
		"token_endpoint":                             iss + "/oauth/token",
		"userinfo_endpoint":                          iss + "/oauth/userinfo",
		"jwks_uri":                                   iss + "/oauth/jwks",
		"revocation_endpoint":                        iss + "/oauth/revoke",
		"response_types_supported":                   []string{"code"},
		"response_modes_supported":                   []string{"query"},
		"grant_types_supported":                      []string{"authorization_code", "refresh_token"},
		"subject_types_supported":                    []string{"public"},
		"id_token_signing_alg_values_supported":      []string{"RS256"},
		"scopes_supported":                           []string{oauthScopeOpenID, oauthScopeProfile, oauthScopeEmail, oauthScopeOffline, oauthScopeBasic},
		"token_endpoint_auth_methods_supported":      authMethods,
		"revocation_endpoint_auth_methods_supported": authMethods,
		"code_challenge_methods_supported":           []string{"S256"},
		"prompt_values_supported":                    []string{"none", "login", "consent"},
		"claims_supported": []string{"sub", "iss", "aud", "exp", "iat", "azp", "nonce", "at_hash",
			"name", "nickname", "preferred_username", "picture", "email", "email_verified"},
		"claims_parameter_supported":                     false,
		"request_parameter_supported":                    false,
		"request_uri_parameter_supported":                false,
		"authorization_response_iss_parameter_supported": false,
	})
}

// OIDCJWKS GET /oauth/jwks
func (a *App) OIDCJWKS(c *gin.Context) {
	key, kid, err := a.oidcSigningKey()
	if err != nil {
		log.Printf("[oidc] jwks: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error"})
		return
	}
	c.Header("Cache-Control", "public, max-age=300")
	c.JSON(http.StatusOK, gin.H{"keys": []gin.H{{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": kid,
		"n": b64url(key.PublicKey.N.Bytes()),
		"e": b64url(big.NewInt(int64(key.PublicKey.E)).Bytes()),
	}}})
}

// OAuthCORS 供浏览器端（单页应用等公开客户端）跨域调用 token / userinfo / revoke / 发现 / JWKS。
// 这些端点不认 Cookie，凭 client 凭据或 Bearer 令牌，放开任意来源是安全的。
func OAuthCORS(c *gin.Context) {
	h := c.Writer.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	h.Set("Access-Control-Max-Age", "600")
	if c.Request.Method == http.MethodOptions {
		c.AbortWithStatus(http.StatusNoContent)
		return
	}
	c.Next()
}
