package handler

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"doc-share/internal/middleware"
	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
)

func pkcePair(seed string) (verifier, challenge string) {
	verifier = strings.Repeat(seed, 64)[:64]
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

func authzParams(clientID, scope string, extra map[string]string) url.Values {
	q := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {testRedirect}, "scope": {scope}, "state": {"st"}}
	for k, v := range extra {
		q.Set(k, v)
	}
	return q
}

// obtainCode 走完整授权（已授权时直接回跳，否则解析同意页并点「同意」），返回 code
func obtainCode(t *testing.T, a *App, u *model.User, q url.Values) string {
	t.Helper()
	w := call(a.OAuthAuthorizePage, u, http.MethodGet, "/oauth/authorize?"+q.Encode(), nil, "", nil, nil)
	if w.Code == http.StatusOK {
		m := csrfRe.FindStringSubmatch(w.Body.String())
		if m == nil {
			t.Fatal("同意页缺 csrf")
		}
		form := url.Values{}
		for k, v := range q {
			form[k] = v
		}
		form.Set("csrf", m[1])
		form.Set("decision", "allow")
		w = call(a.OAuthAuthorizeSubmit, u, http.MethodPost, "/oauth/authorize", form, "", nil, nil)
	}
	got := locationQuery(t, w)
	if got.Get("code") == "" {
		t.Fatalf("未拿到 code: %v", got)
	}
	return got.Get("code")
}

func tokenCall(a *App, form url.Values) *httptest.ResponseRecorder {
	return call(a.OAuthToken, nil, http.MethodPost, "/oauth/token", form, "", nil, nil)
}

// verifyIDToken 用 JWKS 端点的公钥验签并返回声明
func verifyIDToken(t *testing.T, a *App, idt string) map[string]any {
	t.Helper()
	w := call(a.OIDCJWKS, nil, http.MethodGet, "/oauth/jwks", nil, "", nil, nil)
	assertCode(t, w, http.StatusOK)
	var jwks struct {
		Keys []struct{ Kty, Alg, Kid, N, E string } `json:"keys"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &jwks); err != nil || len(jwks.Keys) != 1 {
		t.Fatalf("jwks = %s", w.Body.String())
	}
	k := jwks.Keys[0]
	nb, _ := base64.RawURLEncoding.DecodeString(k.N)
	eb, _ := base64.RawURLEncoding.DecodeString(k.E)
	pub := &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: int(new(big.Int).SetBytes(eb).Int64())}
	parts := strings.Split(idt, ".")
	if len(parts) != 3 {
		t.Fatalf("id_token 段数不对")
	}
	hdrRaw, _ := base64.RawURLEncoding.DecodeString(parts[0])
	var hdr map[string]string
	_ = json.Unmarshal(hdrRaw, &hdr)
	if hdr["alg"] != "RS256" || hdr["kid"] != k.Kid {
		t.Fatalf("header = %v, jwks kid = %s", hdr, k.Kid)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		t.Fatalf("id_token 验签失败: %v", err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	_ = json.Unmarshal(raw, &claims)
	return claims
}

func newPublicApp(t *testing.T, a *App, admin *model.User) string {
	t.Helper()
	w := call(a.CreateOAuthApp, admin, http.MethodPost, "/x", nil,
		`{"name":"SPA","client_type":"public","redirect_uris":"`+testRedirect+`"}`, nil, nil)
	assertCode(t, w, http.StatusOK)
	res := mustJSON(t, w)
	if res["client_secret"] != "" {
		t.Fatalf("公开客户端不应有 secret: %v", res)
	}
	return res["data"].(map[string]any)["client_id"].(string)
}

func TestOIDCPublicClientPKCEFlow(t *testing.T) {
	a, users := oauthSrvApp(t)
	alice := users["viewm"]
	alice.Email, alice.EmailVerified, alice.Avatar = "alice@example.com", true, "/uploads/a.png"
	a.DB.Save(alice)
	a.setCache = &SiteSettings{SiteName: "DocShare", RegMethod: "username", SiteDomain: "https://docs.example.com"}
	clientID := newPublicApp(t, a, users["boss"])

	// 公开客户端不带 PKCE → invalid_request 回跳；plain 方法也不接受
	got := locationQuery(t, call(a.OAuthAuthorizePage, alice, http.MethodGet,
		"/oauth/authorize?"+authzParams(clientID, "openid", nil).Encode(), nil, "", nil, nil))
	if got.Get("error") != "invalid_request" {
		t.Fatalf("无 PKCE = %v", got)
	}
	verifier, challenge := pkcePair("abcXYZ019-._~")
	got = locationQuery(t, call(a.OAuthAuthorizePage, alice, http.MethodGet, "/oauth/authorize?"+authzParams(clientID, "openid",
		map[string]string{"code_challenge": challenge, "code_challenge_method": "plain"}).Encode(), nil, "", nil, nil))
	if got.Get("error") != "invalid_request" {
		t.Fatalf("plain = %v", got)
	}

	q := authzParams(clientID, "openid profile email offline_access", map[string]string{
		"code_challenge": challenge, "code_challenge_method": "S256", "nonce": "n-123"})
	code := obtainCode(t, a, alice, q)
	exchange := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {testRedirect}, "client_id": {clientID}}

	// 不带 / 带错 code_verifier 被拒，且不消耗 code
	assertCode(t, tokenCall(a, exchange), http.StatusBadRequest)
	exchange.Set("code_verifier", strings.Repeat("w", 64))
	if w := tokenCall(a, exchange); w.Code != http.StatusBadRequest || mustJSON(t, w)["error"] != "invalid_grant" {
		t.Fatalf("错 verifier = %d %s", w.Code, w.Body.String())
	}
	// 公开客户端带 secret → invalid_client
	bad := url.Values{}
	for k, v := range exchange {
		bad[k] = v
	}
	bad.Set("client_secret", "anything")
	if w := tokenCall(a, bad); w.Code != http.StatusUnauthorized {
		t.Fatalf("公开客户端带 secret 应 401, got %d", w.Code)
	}

	exchange.Set("code_verifier", verifier)
	w := tokenCall(a, exchange)
	assertCode(t, w, http.StatusOK)
	tok := mustJSON(t, w)
	access, refresh, idt := tok["access_token"].(string), tok["refresh_token"].(string), tok["id_token"].(string)
	if access == "" || !strings.HasPrefix(refresh, "dsr_") || idt == "" {
		t.Fatalf("token resp = %v", tok)
	}

	claims := verifyIDToken(t, a, idt)
	if claims["iss"] != "https://docs.example.com" || claims["aud"] != clientID || claims["nonce"] != "n-123" ||
		claims["sub"] != strconv.Itoa(int(alice.ID)) || claims["email"] != "alice@example.com" ||
		claims["picture"] != "https://docs.example.com/uploads/a.png" {
		t.Fatalf("claims = %v", claims)
	}
	if claims["at_hash"] != oidcAtHash(access) {
		t.Fatal("at_hash 不对")
	}
	// userinfo 的 sub 与 id_token 一致
	w = call(a.OAuthUserInfo, nil, http.MethodGet, "/oauth/userinfo", nil, "", nil, map[string]string{"Authorization": "Bearer " + access})
	assertCode(t, w, http.StatusOK)
	if mustJSON(t, w)["sub"] != claims["sub"] {
		t.Fatal("userinfo sub 与 id_token 不一致")
	}

	// ---- 刷新令牌：轮换 + 重放检测 ----
	refreshForm := func(rt, scope string) url.Values {
		f := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}, "client_id": {clientID}}
		if scope != "" {
			f.Set("scope", scope)
		}
		return f
	}
	// 超出原授权的 scope 被拒（且不消耗令牌）
	if w := tokenCall(a, refreshForm(refresh, "openid email offline_access basic profile admin")); w.Code != http.StatusBadRequest || mustJSON(t, w)["error"] != "invalid_scope" {
		t.Fatalf("扩大 scope = %d %s", w.Code, w.Body.String())
	}
	w = tokenCall(a, refreshForm(refresh, ""))
	assertCode(t, w, http.StatusOK)
	r1 := mustJSON(t, w)
	refresh2 := r1["refresh_token"].(string)
	if refresh2 == refresh || r1["id_token"] == nil {
		t.Fatalf("刷新应轮换并返回 id_token: %v", r1)
	}
	if c := verifyIDToken(t, a, r1["id_token"].(string)); c["nonce"] != nil {
		t.Fatal("刷新签发的 id_token 不应带 nonce")
	}
	// 收窄 scope：只要 basic，不返回 id_token / 邮箱
	w = tokenCall(a, refreshForm(refresh2, "basic"))
	assertCode(t, w, http.StatusOK)
	r2 := mustJSON(t, w)
	if r2["scope"] != "basic" || r2["id_token"] != nil {
		t.Fatalf("收窄 scope = %v", r2)
	}
	refresh3 := r2["refresh_token"].(string)
	// 旧令牌被重复使用 → 整条链作废，连最新的 refresh3 也不能用
	if w := tokenCall(a, refreshForm(refresh, "")); w.Code != http.StatusBadRequest {
		t.Fatalf("重放旧令牌应失败, got %d", w.Code)
	}
	if w := tokenCall(a, refreshForm(refresh3, "")); w.Code != http.StatusBadRequest {
		t.Fatalf("链作废后最新令牌也应失败, got %d", w.Code)
	}
}

func TestOIDCRevokeAndConsentRevocation(t *testing.T) {
	a, users := oauthSrvApp(t)
	alice := users["viewm"]
	clientID := newPublicApp(t, a, users["boss"])
	verifier, challenge := pkcePair("q")
	issue := func() string {
		code := obtainCode(t, a, alice, authzParams(clientID, "offline_access", map[string]string{
			"code_challenge": challenge, "code_challenge_method": "S256"}))
		w := tokenCall(a, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {testRedirect},
			"client_id": {clientID}, "code_verifier": {verifier}})
		assertCode(t, w, http.StatusOK)
		res := mustJSON(t, w)
		if res["id_token"] != nil {
			t.Fatal("没有 openid scope 不应签发 id_token")
		}
		return res["refresh_token"].(string)
	}
	refreshOK := func(rt string) int {
		return tokenCall(a, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}, "client_id": {clientID}}).Code
	}

	// RFC 7009 撤销：之后无法刷新；未知令牌也回 200
	rt := issue()
	w := call(a.OAuthRevoke, nil, http.MethodPost, "/oauth/revoke", url.Values{"token": {rt}, "client_id": {clientID}}, "", nil, nil)
	assertCode(t, w, http.StatusOK)
	if refreshOK(rt) != http.StatusBadRequest {
		t.Fatal("撤销后不能再刷新")
	}
	w = call(a.OAuthRevoke, nil, http.MethodPost, "/oauth/revoke", url.Values{"token": {"dsr_unknown"}, "client_id": {clientID}}, "", nil, nil)
	assertCode(t, w, http.StatusOK)

	// 用户在控制台撤销授权 → 刷新令牌一并失效
	rt = issue()
	w = call(a.RevokeOAuthConsent, alice, http.MethodDelete, "/x", nil, "", gin.Params{{Key: "clientId", Value: clientID}}, nil)
	assertCode(t, w, http.StatusOK)
	if refreshOK(rt) != http.StatusBadRequest {
		t.Fatal("撤销授权后刷新令牌应失效")
	}
}

func TestOIDCConfidentialPKCEAndNoOffline(t *testing.T) {
	a, users := oauthSrvApp(t)
	alice := users["viewm"]
	w := call(a.CreateOAuthApp, users["boss"], http.MethodPost, "/x", nil, `{"name":"Srv","redirect_uris":"`+testRedirect+`"}`, nil, nil)
	assertCode(t, w, http.StatusOK)
	res := mustJSON(t, w)
	clientID := res["data"].(map[string]any)["client_id"].(string)
	sec := res["client_secret"].(string)

	// 服务端应用不带 PKCE 也可；但发了 code_verifier 却没用 challenge 要拒绝
	code := obtainCode(t, a, alice, authzParams(clientID, "openid", nil))
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {testRedirect},
		"client_id": {clientID}, "client_secret": {sec}, "code_verifier": {strings.Repeat("v", 50)}}
	if w := tokenCall(a, form); w.Code != http.StatusBadRequest {
		t.Fatalf("多余 verifier 应拒绝, got %d", w.Code)
	}
	form.Del("code_verifier")
	w = tokenCall(a, form)
	assertCode(t, w, http.StatusOK)
	tok := mustJSON(t, w)
	if tok["refresh_token"] != nil || tok["id_token"] == nil {
		t.Fatalf("无 offline_access 不应有 refresh_token，有 openid 应有 id_token: %v", tok)
	}
	// 服务端应用不带 secret → invalid_client
	form = url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"x"}, "client_id": {clientID}}
	if w := tokenCall(a, form); w.Code != http.StatusUnauthorized {
		t.Fatalf("服务端应用缺 secret 应 401, got %d", w.Code)
	}
}

func TestOIDCPrompt(t *testing.T) {
	a, users := oauthSrvApp(t)
	alice := users["viewm"]
	w := call(a.CreateOAuthApp, users["boss"], http.MethodPost, "/x", nil, `{"name":"P","redirect_uris":"`+testRedirect+`"}`, nil, nil)
	clientID := mustJSON(t, w)["data"].(map[string]any)["client_id"].(string)
	page := func(u *model.User, extra map[string]string) *httptest.ResponseRecorder {
		return call(a.OAuthAuthorizePage, u, http.MethodGet, "/oauth/authorize?"+authzParams(clientID, "openid", extra).Encode(), nil, "", nil, nil)
	}
	if q := locationQuery(t, page(nil, map[string]string{"prompt": "none"})); q.Get("error") != "login_required" {
		t.Fatalf("未登录 prompt=none = %v", q)
	}
	if q := locationQuery(t, page(alice, map[string]string{"prompt": "none"})); q.Get("error") != "consent_required" {
		t.Fatalf("未授权 prompt=none = %v", q)
	}
	if q := locationQuery(t, page(alice, map[string]string{"prompt": "none login"})); q.Get("error") != "invalid_request" {
		t.Fatalf("prompt=none login = %v", q)
	}
	obtainCode(t, a, alice, authzParams(clientID, "openid", nil))
	// 已授权：prompt=none 静默签发；prompt=consent 仍要确认
	if q := locationQuery(t, page(alice, map[string]string{"prompt": "none"})); q.Get("code") == "" {
		t.Fatalf("已授权 prompt=none 应直接回跳 code: %v", q)
	}
	if w := page(alice, map[string]string{"prompt": "consent"}); w.Code != http.StatusOK || !csrfRe.MatchString(w.Body.String()) {
		t.Fatalf("prompt=consent 应展示同意页, got %d", w.Code)
	}
	// prompt=login：清会话、去登录；登录后回到去掉 prompt=login 的请求
	w = page(alice, map[string]string{"prompt": "login consent"})
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/login" {
		t.Fatalf("prompt=login = %d %s", w.Code, w.Header().Get("Location"))
	}
	var sessionCleared bool
	var next string
	for _, ck := range (&http.Response{Header: w.Header()}).Cookies() {
		if ck.Name == middleware.CookieSession && ck.MaxAge < 0 {
			sessionCleared = true
		}
		if ck.Name == loginNextCookie {
			next, _ = url.QueryUnescape(ck.Value)
			next, _ = url.QueryUnescape(next)
		}
	}
	if !sessionCleared || !strings.Contains(next, "prompt=consent") || strings.Contains(next, "login") {
		t.Fatalf("prompt=login 处理不对: cleared=%v next=%s", sessionCleared, next)
	}
}

func TestOIDCDiscoveryJWKSAndKeyPersistence(t *testing.T) {
	a, _ := oauthSrvApp(t)
	a.setCache = &SiteSettings{SiteName: "DocShare", RegMethod: "username", SiteDomain: "https://docs.example.com/"}
	w := call(a.OIDCDiscovery, nil, http.MethodGet, "/.well-known/openid-configuration", nil, "", nil, nil)
	assertCode(t, w, http.StatusOK)
	d := mustJSON(t, w)
	if d["issuer"] != "https://docs.example.com" || d["jwks_uri"] != "https://docs.example.com/oauth/jwks" ||
		d["token_endpoint"] != "https://docs.example.com/oauth/token" {
		t.Fatalf("discovery = %v", d)
	}
	if m, _ := json.Marshal(d["code_challenge_methods_supported"]); string(m) != `["S256"]` {
		t.Fatalf("code_challenge_methods_supported = %s", m)
	}
	_, kid1, err := a.oidcSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	// 私钥加密落库：库里没有明文 PEM / DER
	var row model.SystemSetting
	a.DB.Where(&model.SystemSetting{Key: settingOIDCSigningKey}).First(&row)
	if !strings.HasPrefix(row.Value, "v1:") || strings.Contains(row.Value, "PRIVATE KEY") {
		t.Fatalf("签名私钥应加密存储: %.20s", row.Value)
	}
	// 另一实例（同库同会话密钥）读到同一把
	b := &App{DB: a.DB, Signer: a.Signer, Cfg: a.Cfg}
	_, kid2, err := b.oidcSigningKey()
	if err != nil || kid2 != kid1 {
		t.Fatalf("多实例应共用密钥: %s vs %s (%v)", kid1, kid2, err)
	}
}

func TestOAuthCORSPreflight(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.OPTIONS("/oauth/token", OAuthCORS, func(*gin.Context) { t.Fatal("预检不应进入 handler") })
	r.POST("/oauth/token", OAuthCORS, func(c *gin.Context) { c.Status(http.StatusTeapot) })
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/oauth/token", nil)
	req.Header.Set("Origin", "https://spa.example")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != "*" ||
		!strings.Contains(w.Header().Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Fatalf("preflight = %d %v", w.Code, w.Header())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/oauth/token", nil))
	if w.Code != http.StatusTeapot || w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("post = %d", w.Code)
	}
}

func TestUserinfoWWWAuthenticate(t *testing.T) {
	a, _ := oauthSrvApp(t)
	w := call(a.OAuthUserInfo, nil, http.MethodGet, "/oauth/userinfo", nil, "", nil, nil)
	assertCode(t, w, http.StatusUnauthorized)
	h := w.Header().Get("WWW-Authenticate")
	if !strings.HasPrefix(h, "Bear"+`er error="invalid_token"`) {
		t.Fatalf("WWW-Authenticate = %q", h)
	}
	_ = time.Now
}
