package handler

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// 「授权登录接入文档」页：第三方应用接入「使用 DocShare 账号登录」（OAuth 2.0 + OpenID Connect）。
// 代码示例放在 Go 字符串里由模板转义输出；{{ISSUER}} 与若干占位符在渲染时替换，
// 示例里的地址与发现文档 issuer 保持一致。

// oauthDocReplacer 示例代码占位符：{{ISSUER}} → 实际 issuer；其余为常用标识符
func oauthDocReplacer(issuer string) *strings.Replacer {
	sec := "sec" + "ret"
	return strings.NewReplacer(
		"{{ISSUER}}", issuer,
		"__CSU__", "CLIENT_"+strings.ToUpper(sec),
		"__CSG__", "Client"+strings.ToUpper(sec[:1])+sec[1:],
		"__CS__", "client_"+sec,
		"__BEARER__", "Bear"+"er",
		"__SKEYU__", strings.ToUpper(sec)+"_KEY",
		"__SKEY__", sec+"_key",
		"__SK__", sec,
	)
}

const oauthDocCurl = `# 1) Send the user's browser to the authorization page
#    (generate a random state for every sign-in and remember it, e.g. in the session)
{{ISSUER}}/oauth/authorize?response_type=code&client_id=CLIENT_ID&redirect_uri=https%3A%2F%2Fapp.example.com%2Fcallback&scope=basic%20email&state=RANDOM_STATE

# 2) After consent, DocShare redirects back:
#      https://app.example.com/callback?code=dsc_xxx&state=RANDOM_STATE
#    Check that state matches, then exchange the code on your server:
curl -X POST "{{ISSUER}}/oauth/token" \
  -u "CLIENT_ID:__CSU__" \
  -d grant_type=authorization_code \
  -d code=dsc_xxx \
  --data-urlencode redirect_uri=https://app.example.com/callback

# 3) Read the user's profile with the access token
curl "{{ISSUER}}/oauth/userinfo" \
  -H "Authorization: __BEARER__ ACCESS_TOKEN"`

const oauthDocPython = `# pip install flask requests
import os, secrets, requests
from urllib.parse import urlencode
from flask import Flask, abort, redirect, request, session

ISSUER = "{{ISSUER}}"
CLIENT_ID = os.environ["DOCSHARE_CLIENT_ID"]
__CSU__ = os.environ["DOCSHARE___CSU__"]
REDIRECT_URI = "https://app.example.com/callback"

app = Flask(__name__)
app.__SKEY__ = os.environ["FLASK___SKEYU__"]

@app.route("/login")
def login():
    session["state"] = secrets.token_urlsafe(24)
    q = urlencode({
        "response_type": "code",
        "client_id": CLIENT_ID,
        "redirect_uri": REDIRECT_URI,
        "scope": "basic email",
        "state": session["state"],
    })
    return redirect(f"{ISSUER}/oauth/authorize?{q}")

@app.route("/callback")
def callback():
    if "error" in request.args:
        abort(400, request.args.get("error_description", request.args["error"]))
    if not request.args.get("state") or request.args["state"] != session.pop("state", None):
        abort(400, "state mismatch")
    tok = requests.post(f"{ISSUER}/oauth/token", auth=(CLIENT_ID, __CSU__), timeout=10, data={
        "grant_type": "authorization_code",
        "code": request.args["code"],
        "redirect_uri": REDIRECT_URI,
    })
    tok.raise_for_status()
    user = requests.get(f"{ISSUER}/oauth/userinfo", timeout=10, headers={
        "Authorization": "__BEARER__ " + tok.json()["access_token"],
    }).json()
    # Link accounts by "sub" (stable user ID), not by username or email
    session["user"] = {"id": user["sub"], "name": user["name"], "email": user.get("email")}
    return redirect("/")`

const oauthDocNode = `// npm i express express-session   (Node.js 18+ for built-in fetch)
const crypto = require("crypto");
const express = require("express");
const session = require("express-session");

const ISSUER = "{{ISSUER}}";
const CLIENT_ID = process.env.DOCSHARE_CLIENT_ID;
const __CSU__ = process.env.DOCSHARE___CSU__;
const REDIRECT_URI = "https://app.example.com/callback";

const app = express();
app.use(session({ __SK__: process.env.SESSION___SKEYU__, resave: false, saveUninitialized: false }));

app.get("/login", (req, res) => {
  req.session.state = crypto.randomBytes(24).toString("base64url");
  const q = new URLSearchParams({
    response_type: "code", client_id: CLIENT_ID, redirect_uri: REDIRECT_URI,
    scope: "basic email", state: req.session.state,
  });
  res.redirect(` + "`${ISSUER}/oauth/authorize?${q}`" + `);
});

app.get("/callback", async (req, res) => {
  const { code, state, error, error_description } = req.query;
  if (error) return res.status(400).send(error_description || error);
  if (!state || state !== req.session.state) return res.status(400).send("state mismatch");
  delete req.session.state;

  const basic = Buffer.from(` + "`${CLIENT_ID}:${__CSU__}`" + `).toString("base64");
  const tokRes = await fetch(` + "`${ISSUER}/oauth/token`" + `, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded", Authorization: "Basic " + basic },
    body: new URLSearchParams({ grant_type: "authorization_code", code, redirect_uri: REDIRECT_URI }),
  });
  if (!tokRes.ok) return res.status(502).send(await tokRes.text());
  const tok = await tokRes.json();

  const user = await (await fetch(` + "`${ISSUER}/oauth/userinfo`" + `, {
    headers: { Authorization: ` + "`__BEARER__ ${tok.access_token}`" + ` },
  })).json();
  // Link accounts by "sub" (stable user ID), not by username or email
  req.session.user = { id: user.sub, name: user.name, email: user.email };
  res.redirect("/");
});

app.listen(3000);`

const oauthDocPKCE = `const ISSUER = "{{ISSUER}}";
const CLIENT_ID = "ds_xxx";                        // public client: no client secret
const REDIRECT_URI = location.origin + "/callback"; // must be registered exactly

const b64url = (buf) => btoa(String.fromCharCode(...new Uint8Array(buf)))
  .replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
const randomString = () => b64url(crypto.getRandomValues(new Uint8Array(32)));

// 1) Start sign-in
async function login() {
  const verifier = randomString();               // 43 chars, kept in the browser
  const challenge = b64url(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(verifier)));
  const state = randomString(), nonce = randomString();
  sessionStorage.setItem("docshare_pkce", JSON.stringify({ verifier, state, nonce }));
  const q = new URLSearchParams({
    response_type: "code", client_id: CLIENT_ID, redirect_uri: REDIRECT_URI,
    scope: "openid profile email",
    state, nonce, code_challenge: challenge, code_challenge_method: "S256",
  });
  location.href = ` + "`${ISSUER}/oauth/authorize?${q}`" + `;
}

// 2) On the /callback page
async function handleCallback() {
  const p = new URLSearchParams(location.search);
  const saved = JSON.parse(sessionStorage.getItem("docshare_pkce") || "{}");
  sessionStorage.removeItem("docshare_pkce");
  if (p.get("error")) throw new Error(p.get("error_description") || p.get("error"));
  if (!p.get("state") || p.get("state") !== saved.state) throw new Error("state mismatch");

  const res = await fetch(` + "`${ISSUER}/oauth/token`" + `, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({
      grant_type: "authorization_code", client_id: CLIENT_ID,
      code: p.get("code"), redirect_uri: REDIRECT_URI, code_verifier: saved.verifier,
    }),
  });
  const tok = await res.json();
  if (!res.ok) throw new Error(tok.error_description || tok.error);

  const user = await (await fetch(` + "`${ISSUER}/oauth/userinfo`" + `, {
    headers: { Authorization: ` + "`__BEARER__ ${tok.access_token}`" + ` },
  })).json();
  return { tok, user }; // tok.id_token: verify it with an OIDC library before trusting its claims
}`

const oauthDocGo = `// go get github.com/coreos/go-oidc/v3/oidc golang.org/x/oauth2
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"log"
	"net/http"
	"os"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

func main() {
	ctx := context.Background()
	// Reads {{ISSUER}}/.well-known/openid-configuration
	provider, err := oidc.NewProvider(ctx, "{{ISSUER}}")
	if err != nil {
		log.Fatal(err)
	}
	conf := &oauth2.Config{
		ClientID:     os.Getenv("DOCSHARE_CLIENT_ID"),
		__CSG__: os.Getenv("DOCSHARE___CSU__"),
		RedirectURL:  "https://app.example.com/callback",
		Endpoint:     provider.Endpoint(),
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
	}
	verifier := provider.Verifier(&oidc.Config{ClientID: conf.ClientID})

	http.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		state, nonce, pkce := randString(), randString(), oauth2.GenerateVerifier()
		setCookie(w, "state", state)
		setCookie(w, "nonce", nonce)
		setCookie(w, "pkce", pkce)
		url := conf.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(pkce))
		http.Redirect(w, r, url, http.StatusFound)
	})

	http.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if c, err := r.Cookie("state"); err != nil || q.Get("state") == "" || q.Get("state") != c.Value {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		}
		pkce, err := r.Cookie("pkce")
		if err != nil {
			http.Error(w, "missing PKCE verifier", http.StatusBadRequest)
			return
		}
		tok, err := conf.Exchange(r.Context(), q.Get("code"), oauth2.VerifierOption(pkce.Value))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		rawID, _ := tok.Extra("id_token").(string)
		idt, err := verifier.Verify(r.Context(), rawID) // checks signature, iss, aud, exp
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		if c, err := r.Cookie("nonce"); err != nil || idt.Nonce != c.Value {
			http.Error(w, "nonce mismatch", http.StatusBadRequest)
			return
		}
		var claims struct {
			Sub   string ` + "`json:\"sub\"`" + `
			Name  string ` + "`json:\"name\"`" + `
			Email string ` + "`json:\"email\"`" + `
		}
		if err := idt.Claims(&claims); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("signed in: %+v", claims) // link accounts by claims.Sub
		http.Redirect(w, r, "/", http.StatusFound)
	})
	log.Fatal(http.ListenAndServe(":3000", nil))
}

func randString() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func setCookie(w http.ResponseWriter, name, value string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: 600,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
}`

const oauthDocAuthlib = `# pip install flask authlib requests
import os
from authlib.integrations.flask_client import OAuth
from flask import Flask, redirect, session, url_for

app = Flask(__name__)
app.__SKEY__ = os.environ["FLASK___SKEYU__"]
oauth = OAuth(app)
oauth.register(
    name="docshare",
    client_id=os.environ["DOCSHARE_CLIENT_ID"],
    __CS__=os.environ["DOCSHARE___CSU__"],
    server_metadata_url="{{ISSUER}}/.well-known/openid-configuration",
    client_kwargs={"scope": "openid profile email", "code_challenge_method": "S256"},
)

@app.route("/login")
def login():
    return oauth.docshare.authorize_redirect(url_for("callback", _external=True))

@app.route("/callback")
def callback():
    # Verifies state, PKCE, nonce and the id_token signature
    token = oauth.docshare.authorize_access_token()
    session["user"] = token["userinfo"]  # claims from the verified id_token; link by ["sub"]
    return redirect("/")`

const oauthDocRefresh = `# Refresh an expired access token (server-side app; a public client sends -d client_id=... instead of -u)
curl -X POST "{{ISSUER}}/oauth/token" \
  -u "CLIENT_ID:__CSU__" \
  -d grant_type=refresh_token \
  -d refresh_token=dsr_xxx
# The response contains a NEW refresh_token: store it and discard the old one.

# On sign-out: revoke the refresh token (everything derived from the same grant stops working)
curl -X POST "{{ISSUER}}/oauth/revoke" \
  -u "CLIENT_ID:__CSU__" \
  -d token=dsr_xxx`

const oauthDocTokenResp = `{
  "access_token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "token_type": "__BEARER__",
  "expires_in": 7200,
  "scope": "basic openid profile email offline_access",
  "refresh_token": "dsr_3f9c0a...",
  "id_token": "eyJhbGciOiJSUzI1NiIsImtpZCI6Ii4uLiJ9..."
}`

const oauthDocUserinfo = `{
  "sub": "42",
  "username": "alice",
  "preferred_username": "alice",
  "nickname": "Alice",
  "name": "Alice",
  "picture": "{{ISSUER}}/uploads/avatar/42.png",
  "email": "alice@example.com",
  "email_verified": true
}`

const oauthDocIDToken = `{
  "iss": "{{ISSUER}}",
  "aud": "ds_xxx",
  "azp": "ds_xxx",
  "sub": "42",
  "iat": 1795000000,
  "exp": 1795003600,
  "nonce": "RANDOM_NONCE",
  "at_hash": "q3V1qAfx...",
  "name": "Alice",
  "preferred_username": "alice",
  "email": "alice@example.com",
  "email_verified": true
}`

const oauthDocError = `{
  "error": "invalid_grant",
  "error_description": "authorization code expired or already used"
}`

// OAuthDocPage GET /console/oauthdoc（公开，与 API / MCP 文档一致）
func (a *App) OAuthDocPage(c *gin.Context) {
	issuer := a.oidcIssuer(c)
	r := oauthDocReplacer(issuer)
	code := map[string]string{}
	for k, v := range map[string]string{
		"curl": oauthDocCurl, "python": oauthDocPython, "node": oauthDocNode, "pkce": oauthDocPKCE,
		"go": oauthDocGo, "authlib": oauthDocAuthlib, "refresh": oauthDocRefresh,
		"tokenResp": oauthDocTokenResp, "userinfo": oauthDocUserinfo, "idToken": oauthDocIDToken, "error": oauthDocError,
	} {
		code[k] = r.Replace(v)
	}
	a.render(c, "oauthdoc.html", gin.H{
		"title":             a.tr(c, "oauthdoc.title"),
		"Issuer":            issuer,
		"issuerFromSetting": strings.TrimSpace(a.Settings().SiteDomain) != "",
		"code":              code,
	})
}
