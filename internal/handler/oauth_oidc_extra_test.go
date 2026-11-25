package handler

import (
	"net/http"
	"strconv"
	"testing"

	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
)

func TestPublicClientCannotRotate(t *testing.T) {
	a, users := oauthSrvApp(t)
	newPublicApp(t, a, users["boss"])
	var app model.OAuthClient
	a.DB.Where("client_type = ?", model.OAuthClientPublic).First(&app)
	if app.ClientSecretHash != "" {
		t.Fatal("公开客户端不应存 secret 摘要")
	}
	w := call(a.RotateOAuthAppSecret, users["boss"], http.MethodPost, "/x", nil, "{}",
		gin.Params{{Key: "id", Value: strconv.Itoa(int(app.ID))}}, nil)
	assertCode(t, w, http.StatusBadRequest)
	// 非法类型
	w = call(a.CreateOAuthApp, users["boss"], http.MethodPost, "/x", nil,
		`{"name":"X","client_type":"weird","redirect_uris":"`+testRedirect+`"}`, nil, nil)
	assertCode(t, w, http.StatusBadRequest)
}
