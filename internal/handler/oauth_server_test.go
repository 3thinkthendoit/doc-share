package handler

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"doc-share/internal/middleware"
	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
)

const testRedirect = "https://app.example.com/cb"

func oauthSrvApp(t *testing.T) (*App, map[string]*model.User) {
	t.Helper()
	a := oauthTestApp(t)
	r := newRenderApp(t, a.DB)
	a.I18N, a.Tmpl = r.I18N, r.Tmpl
	users, err := loadTestUsers(a)
	if err != nil {
		t.Fatal(err)
	}
	return a, users
}

// call 以指定用户（可为 nil）调用 handler；form 非 nil 时按表单提交
func call(h gin.HandlerFunc, u *model.User, method, target string, form url.Values, json string, params gin.Params, hdr map[string]string) *httptest.ResponseRecorder {
	w, c := newCtx()
	switch {
	case form != nil:
		c.Request = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	case json != "":
		c.Request = httptest.NewRequest(method, target, strings.NewReader(json))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Request.Header.Set("X-Requested-With", "XMLHttpRequest")
	default:
		c.Request = httptest.NewRequest(method, target, nil)
	}
	for k, v := range hdr {
		c.Request.Header.Set(k, v)
	}
	if u != nil {
		c.Set(middleware.ContextUser, u)
	}
	c.Params = params
	h(c)
	c.Writer.WriteHeaderNow() // 直接调 handler 时补上路由末尾的状态码落盘（POST 重定向无响应体）
	return w
}

func authorizeURL(clientID, scope, state string) string {
	q := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {testRedirect}, "scope": {scope}, "state": {state}}
	return "/oauth/authorize?" + q.Encode()
}

func mustJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("json: %v body=%s", err, w.Body.String())
	}
	return m
}

func locationQuery(t *testing.T, w *httptest.ResponseRecorder) url.Values {
	t.Helper()
	if w.Code != http.StatusFound {
		t.Fatalf("want 302, got %d body=%s", w.Code, w.Body.String())
	}
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(loc.String(), testRedirect+"?") {
		t.Fatalf("redirected to %s", loc)
	}
	return loc.Query()
}

var csrfRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func TestOAuthServerFullFlow(t *testing.T) {
	a, users := oauthSrvApp(t)
	dev, admin, alice := users["owner"], users["boss"], users["viewm"]
	alice.Email, alice.EmailVerified = "alice@example.com", true
	a.DB.Save(alice)

	// 1) 开发者注册应用：待审核，Secret 只返回这一次
	w := call(a.CreateOAuthApp, dev, http.MethodPost, "/console/api/oauth-apps", nil,
		`{"name":"示例应用","homepage_url":"https://app.example.com","redirect_uris":"`+testRedirect+`\nhttp://localhost:3000/cb"}`, nil, nil)
	assertCode(t, w, http.StatusOK)
	created := mustJSON(t, w)
	secret := created["client_secret"].(string)
	data := created["data"].(map[string]any)
	clientID := data["client_id"].(string)
	appID := strconv.Itoa(int(data["id"].(float64)))
	if data["status"] != model.OAuthClientPending || !strings.HasPrefix(secret, "dss_") {
		t.Fatalf("created = %+v", created)
	}
	var stored model.OAuthClient
	a.DB.First(&stored)
	if stored.ClientSecretHash == secret || stored.ClientSecretHash != hashOAuthSecret(secret) {
		t.Fatal("secret 应只存摘要")
	}

	// 2) 未审核：授权页报错且不回跳
	w = call(a.OAuthAuthorizePage, alice, http.MethodGet, authorizeURL(clientID, "basic", "s1"), nil, "", nil, nil)
	assertCode(t, w, http.StatusBadRequest)
	if w.Header().Get("Location") != "" {
		t.Fatal("应用未审核时不应回跳")
	}

	// 3) 管理员审核通过，并给开发者发站内信
	w = call(a.ReviewOAuthApp, admin, http.MethodPost, "/x", nil, `{"action":"approve"}`, gin.Params{{Key: "id", Value: appID}}, nil)
	assertCode(t, w, http.StatusOK)
	var msgs int64
	a.DB.Model(&model.Message{}).Where("user_id = ? AND kind = ?", dev.ID, MsgOAuthApp).Count(&msgs)
	if msgs != 1 {
		t.Fatalf("开发者应收到审核结果消息, got %d", msgs)
	}

	// 4) 未登录：去登录页，并记下回跳地址
	w = call(a.OAuthAuthorizePage, nil, http.MethodGet, authorizeURL(clientID, "basic email", "s1"), nil, "", nil, nil)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/login" {
		t.Fatalf("未登录应跳登录, got %d %s", w.Code, w.Header().Get("Location"))
	}
	if !strings.Contains(w.Header().Get("Set-Cookie"), loginNextCookie+"=") {
		t.Fatal("应写入登录后回跳 cookie")
	}

	// 5) 已登录：渲染同意页（禁止被嵌入）
	w = call(a.OAuthAuthorizePage, alice, http.MethodGet, authorizeURL(clientID, "basic email", "s1"), nil, "", nil, nil)
	assertCode(t, w, http.StatusOK)
	page := w.Body.String()
	if !strings.Contains(page, "示例应用") || w.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("同意页内容或防嵌入头不对")
	}
	m := csrfRe.FindStringSubmatch(page)
	if m == nil {
		t.Fatal("同意页缺 csrf")
	}
	csrf := m[1]
	consentForm := func(decision, token string) url.Values {
		return url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {testRedirect},
			"scope": {"basic email"}, "state": {"s1"}, "csrf": {token}, "decision": {decision}}
	}

	// 6) 拒绝 → access_denied 回跳并带回 state
	q := locationQuery(t, call(a.OAuthAuthorizeSubmit, alice, http.MethodPost, "/oauth/authorize", consentForm("deny", csrf), "", nil, nil))
	if q.Get("error") != "access_denied" || q.Get("state") != "s1" {
		t.Fatalf("deny redirect = %v", q)
	}

	// 7) CSRF：别人的令牌 / 伪造令牌被拒
	bobToken := a.mintConsentToken(users["editm"].ID, clientID)
	for _, tok := range []string{"", "forged", bobToken} {
		w = call(a.OAuthAuthorizeSubmit, alice, http.MethodPost, "/oauth/authorize", consentForm("allow", tok), "", nil, nil)
		assertCode(t, w, http.StatusForbidden)
	}

	// 8) 同意 → 带 code + state 回跳
	q = locationQuery(t, call(a.OAuthAuthorizeSubmit, alice, http.MethodPost, "/oauth/authorize", consentForm("allow", csrf), "", nil, nil))
	code := q.Get("code")
	if code == "" || q.Get("state") != "s1" {
		t.Fatalf("allow redirect = %v", q)
	}

	tokenForm := func(code, redirect string) url.Values {
		return url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}}
	}
	basic := func(id, sec string) map[string]string {
		return map[string]string{"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(id+":"+sec))}
	}

	// 9) 换票：错误 Secret / 回调不一致被拒（不消耗 code）
	w = call(a.OAuthToken, nil, http.MethodPost, "/oauth/token", tokenForm(code, testRedirect), "", nil, basic(clientID, "wrong"))
	assertCode(t, w, http.StatusUnauthorized)
	if mustJSON(t, w)["error"] != "invalid_client" || w.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("wrong secret = %s", w.Body.String())
	}
	w = call(a.OAuthToken, nil, http.MethodPost, "/oauth/token", tokenForm(code, "http://localhost:3000/cb"), "", nil, basic(clientID, secret))
	assertCode(t, w, http.StatusBadRequest)

	// 10) 换票成功（client_secret_basic）；同一 code 第二次被拒
	w = call(a.OAuthToken, nil, http.MethodPost, "/oauth/token", tokenForm(code, testRedirect), "", nil, basic(clientID, secret))
	assertCode(t, w, http.StatusOK)
	tok := mustJSON(t, w)
	access := tok["access_token"].(string)
	if tok["token_type"] != "Bearer" || tok["scope"] != "basic email" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("token resp = %v", tok)
	}
	w = call(a.OAuthToken, nil, http.MethodPost, "/oauth/token", tokenForm(code, testRedirect), "", nil, basic(clientID, secret))
	if w.Code != http.StatusBadRequest || mustJSON(t, w)["error"] != "invalid_grant" {
		t.Fatalf("code 重放应被拒: %d %s", w.Code, w.Body.String())
	}

	// 11) userinfo：scope 含 email 才返回邮箱
	bearer := map[string]string{"Authorization": "Bearer " + access}
	w = call(a.OAuthUserInfo, nil, http.MethodGet, "/oauth/userinfo", nil, "", nil, bearer)
	assertCode(t, w, http.StatusOK)
	info := mustJSON(t, w)
	if info["sub"] != strconv.Itoa(int(alice.ID)) || info["username"] != "viewm" || info["email"] != "alice@example.com" || info["email_verified"] != true {
		t.Fatalf("userinfo = %v", info)
	}
	// 站内会话令牌不能当 access_token 用
	w = call(a.OAuthUserInfo, nil, http.MethodGet, "/oauth/userinfo", nil, "", nil,
		map[string]string{"Authorization": "Bearer " + a.Signer.MakeUserToken(alice.ID, time.Hour, false)})
	assertCode(t, w, http.StatusUnauthorized)

	// 12) 已授权且 scope 覆盖：直接回跳，不再弹同意页；scope 扩大时才需要重新同意
	q = locationQuery(t, call(a.OAuthAuthorizePage, alice, http.MethodGet, authorizeURL(clientID, "basic", "s2"), nil, "", nil, nil))
	if q.Get("code") == "" || q.Get("state") != "s2" {
		t.Fatalf("已授权应直接签发 code: %v", q)
	}

	// 13) 用户撤销授权 → 已签发的令牌立即失效，未用 code 也作废
	pendingCode := q.Get("code")
	w = call(a.RevokeOAuthConsent, alice, http.MethodDelete, "/x", nil, "", gin.Params{{Key: "clientId", Value: clientID}}, nil)
	assertCode(t, w, http.StatusOK)
	w = call(a.OAuthUserInfo, nil, http.MethodGet, "/oauth/userinfo", nil, "", nil, bearer)
	assertCode(t, w, http.StatusUnauthorized)
	w = call(a.OAuthToken, nil, http.MethodPost, "/oauth/token", tokenForm(pendingCode, testRedirect), "", nil, basic(clientID, secret))
	assertCode(t, w, http.StatusBadRequest)

	// 14) 停用应用 → 换票 invalid_client
	w = call(a.ReviewOAuthApp, admin, http.MethodPost, "/x", nil, `{"action":"disable","reason":"违规"}`, gin.Params{{Key: "id", Value: appID}}, nil)
	assertCode(t, w, http.StatusOK)
	form := tokenForm("whatever", testRedirect)
	form.Set("client_id", clientID)
	form.Set("client_secret", secret) // client_secret_post
	w = call(a.OAuthToken, nil, http.MethodPost, "/oauth/token", form, "", nil, nil)
	if w.Code != http.StatusUnauthorized || mustJSON(t, w)["error"] != "invalid_client" {
		t.Fatalf("停用后换票 = %d %s", w.Code, w.Body.String())
	}
}

func TestOAuthAuthorizeErrors(t *testing.T) {
	a, users := oauthSrvApp(t)
	alice := users["viewm"]
	cl := model.OAuthClient{ClientID: "ds_test", ClientSecretHash: hashOAuthSecret("s"), Name: "T",
		RedirectURIs: testRedirect, OwnerID: users["owner"].ID, Status: model.OAuthClientApproved}
	a.DB.Create(&cl)

	// 回调地址不在白名单：本站报错，绝不回跳（防把 code 送给攻击者）
	q := url.Values{"response_type": {"code"}, "client_id": {"ds_test"}, "redirect_uri": {"https://evil.example/cb"}}
	w := call(a.OAuthAuthorizePage, alice, http.MethodGet, "/oauth/authorize?"+q.Encode(), nil, "", nil, nil)
	assertCode(t, w, http.StatusBadRequest)
	// 前缀相似也不行
	q.Set("redirect_uri", testRedirect+"/../x")
	w = call(a.OAuthAuthorizePage, alice, http.MethodGet, "/oauth/authorize?"+q.Encode(), nil, "", nil, nil)
	assertCode(t, w, http.StatusBadRequest)

	// 回调可信时，其他错误回跳给第三方
	q.Set("redirect_uri", testRedirect)
	q.Set("response_type", "token")
	q.Set("state", "x")
	if got := locationQuery(t, call(a.OAuthAuthorizePage, alice, http.MethodGet, "/oauth/authorize?"+q.Encode(), nil, "", nil, nil)); got.Get("error") != "unsupported_response_type" || got.Get("state") != "x" {
		t.Fatalf("response_type=token → %v", got)
	}
	q.Set("response_type", "code")
	q.Set("scope", "basic admin")
	if got := locationQuery(t, call(a.OAuthAuthorizePage, alice, http.MethodGet, "/oauth/authorize?"+q.Encode(), nil, "", nil, nil)); got.Get("error") != "invalid_scope" {
		t.Fatalf("scope=admin → %v", got)
	}
	// 不支持的 grant_type
	w = call(a.OAuthToken, nil, http.MethodPost, "/oauth/token", url.Values{"grant_type": {"password"}}, "", nil, nil)
	if w.Code != http.StatusBadRequest || mustJSON(t, w)["error"] != "unsupported_grant_type" {
		t.Fatalf("grant_type=password → %d %s", w.Code, w.Body.String())
	}
}

func TestOAuthCodeExpiry(t *testing.T) {
	a, users := oauthSrvApp(t)
	alice := users["viewm"]
	a.DB.Create(&model.OAuthClient{ClientID: "ds_exp", ClientSecretHash: hashOAuthSecret("sec"), Name: "E",
		RedirectURIs: testRedirect, OwnerID: users["owner"].ID, Status: model.OAuthClientApproved})
	a.DB.Create(&model.OAuthConsent{UserID: alice.ID, ClientID: "ds_exp", Scopes: "basic", GrantedAt: time.Now()})
	a.DB.Create(&model.OAuthCode{CodeHash: hashOAuthSecret("old"), ClientID: "ds_exp", UserID: alice.ID, Scope: "basic",
		RedirectURI: testRedirect, ExpiresAt: time.Now().Add(-time.Second)})
	form := url.Values{"grant_type": {"authorization_code"}, "code": {"old"}, "redirect_uri": {testRedirect},
		"client_id": {"ds_exp"}, "client_secret": {"sec"}}
	w := call(a.OAuthToken, nil, http.MethodPost, "/oauth/token", form, "", nil, nil)
	if w.Code != http.StatusBadRequest || mustJSON(t, w)["error"] != "invalid_grant" {
		t.Fatalf("过期 code → %d %s", w.Code, w.Body.String())
	}
	// JSON 请求体也支持
	w = call(a.OAuthToken, nil, http.MethodPost, "/oauth/token", nil,
		`{"grant_type":"authorization_code","code":"old","redirect_uri":"`+testRedirect+`","client_id":"ds_exp","client_secret":"sec"}`, nil, nil)
	if w.Code != http.StatusBadRequest || mustJSON(t, w)["error"] != "invalid_grant" {
		t.Fatalf("JSON 请求体 → %d %s", w.Code, w.Body.String())
	}
}

func TestOAuthAccessTokenIntegrity(t *testing.T) {
	a, _ := oauthSrvApp(t)
	now := time.Now()
	tok := a.signOAuthAccessToken(7, "ds_x", "basic", now)
	if uid, cid, sc, err := a.verifyOAuthAccessToken(tok); err != nil || uid != 7 || cid != "ds_x" || sc != "basic" {
		t.Fatalf("verify = %d %s %s %v", uid, cid, sc, err)
	}
	parts := strings.Split(tok, ".")
	// 篡改载荷（提升 scope）
	forged, _ := json.Marshal(map[string]any{"iss": oauthSrvIssuer, "sub": "7", "aud": "ds_x", "scope": "basic email", "exp": now.Add(time.Hour).Unix()})
	if _, _, _, err := a.verifyOAuthAccessToken(parts[0] + "." + base64.RawURLEncoding.EncodeToString(forged) + "." + parts[2]); err == nil {
		t.Fatal("篡改载荷应失败")
	}
	// alg=none
	none := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	if _, _, _, err := a.verifyOAuthAccessToken(none + "." + parts[1] + "."); err == nil {
		t.Fatal("alg=none 应失败")
	}
	// 过期
	if _, _, _, err := a.verifyOAuthAccessToken(a.signOAuthAccessToken(7, "ds_x", "basic", now.Add(-3*time.Hour))); err == nil {
		t.Fatal("过期令牌应失败")
	}
}

func TestOAuthAppValidationAndResubmit(t *testing.T) {
	a, users := oauthSrvApp(t)
	dev := users["owner"]
	bad := []string{
		"javascript:alert(1)",
		"http://app.example.com/cb", // 非本机不许 http
		"https://app.example.com/cb#frag",
		"https://user:pw@app.example.com/cb",
		"/relative",
	}
	for _, u := range bad {
		if _, key := validateOAuthRedirectURIs(u); key == "" {
			t.Errorf("回调 %q 应被拒", u)
		}
	}
	if uris, key := validateOAuthRedirectURIs("http://localhost:3000/cb\r\nhttps://a.example/cb\nhttps://a.example/cb"); key != "" || len(uris) != 2 {
		t.Fatalf("合法回调去重 = %v %s", uris, key)
	}

	// 别人不能改我的应用；我改回调后回到待审核
	app := model.OAuthClient{ClientID: "ds_mine", ClientSecretHash: "x", Name: "M", RedirectURIs: testRedirect,
		OwnerID: dev.ID, Status: model.OAuthClientApproved}
	a.DB.Create(&app)
	id := gin.Params{{Key: "id", Value: strconv.Itoa(int(app.ID))}}
	body := `{"name":"M","redirect_uris":"https://new.example/cb"}`
	assertCode(t, call(a.UpdateOAuthApp, users["viewm"], http.MethodPut, "/x", nil, body, id, nil), http.StatusForbidden)
	w := call(a.UpdateOAuthApp, dev, http.MethodPut, "/x", nil, body, id, nil)
	assertCode(t, w, http.StatusOK)
	a.DB.First(&app, app.ID)
	if app.Status != model.OAuthClientPending || mustJSON(t, w)["resubmitted"] != true {
		t.Fatalf("改回调后应待审核, status=%s", app.Status)
	}
	// 非管理员不能审核（路由层 RequireAdmin；这里验证管理员自己注册直接通过）
	w = call(a.CreateOAuthApp, users["boss"], http.MethodPost, "/x", nil, `{"name":"A","redirect_uris":"`+testRedirect+`"}`, nil, nil)
	assertCode(t, w, http.StatusOK)
	if mustJSON(t, w)["data"].(map[string]any)["status"] != model.OAuthClientApproved {
		t.Fatal("管理员注册的应用应直接通过")
	}
}

func TestOAuthAppsPageRenders(t *testing.T) {
	a, users := oauthSrvApp(t)
	dev, alice := users["owner"], users["viewm"]
	a.DB.Create(&model.OAuthClient{ClientID: "ds_p1", ClientSecretHash: "x", Name: "待审应用", RedirectURIs: testRedirect,
		OwnerID: dev.ID, Status: model.OAuthClientPending, RejectReason: ""})
	a.DB.Create(&model.OAuthClient{ClientID: "ds_p2", ClientSecretHash: "x", Name: "别人的应用", RedirectURIs: testRedirect,
		OwnerID: users["editm"].ID, Status: model.OAuthClientRejected, RejectReason: "信息不全"})
	a.DB.Create(&model.OAuthConsent{UserID: dev.ID, ClientID: "ds_p2", Scopes: "basic", GrantedAt: time.Now()})

	page := func(u *model.User, target string) string {
		w := call(a.OAuthAppsPage, u, http.MethodGet, target, nil, "", nil, nil)
		assertCode(t, w, http.StatusOK)
		return w.Body.String()
	}
	// 普通用户只看到自己的应用，能看到自己授权过的应用
	body := page(dev, "/console/oauth-apps")
	if !strings.Contains(body, "待审应用") || strings.Contains(body, "ds_p2</td>") || !strings.Contains(body, "别人的应用") {
		t.Fatal("开发者视图不对")
	}
	if strings.Contains(body, `data-action="approve"`) {
		t.Fatal("非管理员不应看到审核按钮")
	}
	// 管理员看全部 + 审核按钮 + 待审数；按状态筛选
	body = page(users["boss"], "/console/oauth-apps")
	if !strings.Contains(body, "ds_p1") || !strings.Contains(body, "ds_p2") || !strings.Contains(body, `data-action="approve"`) || !strings.Contains(body, "信息不全") {
		t.Fatal("管理员视图不对")
	}
	body = page(users["boss"], "/console/oauth-apps?status=rejected")
	if strings.Contains(body, "ds_p1") || !strings.Contains(body, "ds_p2") {
		t.Fatal("状态筛选不对")
	}
	_ = alice
}

// 页面上不应残留未翻译的 i18n key（中英文各渲染一遍）
func TestOAuthPagesNoRawKeys(t *testing.T) {
	a, users := oauthSrvApp(t)
	a.DB.Create(&model.OAuthClient{ClientID: "ds_k", ClientSecretHash: "x", Name: "K", HomepageURL: "https://k.example",
		RedirectURIs: testRedirect, OwnerID: users["owner"].ID, Status: model.OAuthClientApproved})
	a.DB.Create(&model.OAuthClient{ClientID: "ds_pub", ClientType: model.OAuthClientPublic, Name: "Pub",
		RedirectURIs: testRedirect, OwnerID: users["owner"].ID, Status: model.OAuthClientApproved})
	l10n := regexp.MustCompile(`(?s)<script>window\.__LANG=.*?</script>`)
	raw := regexp.MustCompile(`oauthsrv\.[A-Za-z.]+`)
	for _, lang := range []string{"zh-CN", "en-US"} {
		for name, h := range map[string]gin.HandlerFunc{
			"consent": a.OAuthAuthorizePage, "apps": a.OAuthAppsPage,
		} {
			target := authorizeURL("ds_k", "openid email offline_access", "s")
			if name == "apps" {
				target = "/console/oauth-apps"
			}
			w, c := newCtx()
			c.Request = httptest.NewRequest(http.MethodGet, target, nil)
			c.Set(middleware.ContextUser, users["boss"])
			c.Set(middleware.ContextLang, lang)
			h(c)
			assertCode(t, w, http.StatusOK)
			body := l10n.ReplaceAllString(w.Body.String(), "")
			body = strings.ReplaceAll(body, `src="/static/oauth_apps.js"`, "")
			if m := raw.FindAllString(body, 5); len(m) > 0 {
				t.Errorf("%s/%s 残留未翻译 key: %v", lang, name, m)
			}
			if lang == "en-US" && name == "consent" && !strings.Contains(body, "wants to sign you in") {
				t.Errorf("英文同意页文案不对")
			}
		}
	}
}

func TestLoginNextRedirect(t *testing.T) {
	a, users := oauthSrvApp(t)
	next := authorizeURL("ds_x", "basic", "s")
	for target, ok := range map[string]bool{
		next: true, "/console": false, "https://evil.example/oauth/authorize?x": false,
		"//evil.example/oauth/authorize?x": false, "/oauth/authorize?a=1 b": false,
	} {
		if safeLoginNext(target) != ok {
			t.Errorf("safeLoginNext(%q) != %v", target, ok)
		}
	}
	// 用真实写出的 Set-Cookie 回放（gin 读写 cookie 时各自再做一次 URL 编解码）
	sw, sc := newCtx()
	sc.Request = httptest.NewRequest(http.MethodGet, next, nil)
	a.setLoginNext(sc, next)
	var nextCookie *http.Cookie
	for _, ck := range (&http.Response{Header: sw.Header()}).Cookies() {
		if ck.Name == loginNextCookie {
			nextCookie = ck
		}
	}
	if nextCookie == nil {
		t.Fatal("setLoginNext 未写 cookie")
	}
	w, c := newCtx()
	c.Request = httptest.NewRequest(http.MethodPost, "/login", nil)
	c.Request.AddCookie(&http.Cookie{Name: loginNextCookie, Value: nextCookie.Value})
	a.issueSession(c, users["viewm"])
	c.Writer.WriteHeaderNow()
	if w.Code != http.StatusFound || w.Header().Get("Location") != next {
		t.Fatalf("登录后应回到授权页, got %d %s", w.Code, w.Header().Get("Location"))
	}
	// 伪造成外站地址的 cookie 被丢弃，回到控制台
	w, c = newCtx()
	c.Request = httptest.NewRequest(http.MethodPost, "/login", nil)
	c.Request.AddCookie(&http.Cookie{Name: loginNextCookie, Value: url.QueryEscape(url.QueryEscape("https://evil.example/"))})
	a.issueSession(c, users["viewm"])
	c.Writer.WriteHeaderNow()
	if w.Header().Get("Location") != "/console" {
		t.Fatalf("非法回跳应回控制台, got %s", w.Header().Get("Location"))
	}
}

func TestDeleteUserPurgesOAuthData(t *testing.T) {
	a, users := oauthSrvApp(t)
	dev, alice := users["owner"], users["viewm"]
	a.DB.Create(&model.OAuthClient{ClientID: "ds_dev", ClientSecretHash: "x", Name: "D", RedirectURIs: testRedirect,
		OwnerID: dev.ID, Status: model.OAuthClientApproved})
	a.DB.Create(&model.OAuthConsent{UserID: alice.ID, ClientID: "ds_dev", Scopes: "basic", GrantedAt: time.Now()})
	a.DB.Create(&model.OAuthIdentity{Provider: "github", Subject: "42", UserID: dev.ID})

	w := call(a.DeleteUser, users["boss"], http.MethodDelete, "/x", nil, "", gin.Params{{Key: "id", Value: strconv.Itoa(int(dev.ID))}}, nil)
	assertCode(t, w, http.StatusOK)
	var n int64
	a.DB.Model(&model.OAuthClient{}).Count(&n)
	if n != 0 {
		t.Fatal("开发者名下应用应被删除")
	}
	a.DB.Model(&model.OAuthConsent{}).Where("client_id = ?", "ds_dev").Count(&n)
	if n != 0 {
		t.Fatal("别人对该应用的授权应一并清理")
	}
	a.DB.Model(&model.OAuthIdentity{}).Where("user_id = ?", dev.ID).Count(&n)
	if n != 0 {
		t.Fatal("销户应清理第三方登录绑定")
	}
}
