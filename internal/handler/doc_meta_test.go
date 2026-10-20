package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"doc-share/internal/model"
	"doc-share/internal/session"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

func TestDocContentMeta(t *testing.T) {
	a, doc := newAuthApp(t)
	users, err := loadTestUsers(a)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Model(doc).Update("content_version", int64(7)).Error; err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: strconv.Itoa(int(doc.ID))}}
	asUser(c, users["viewm"], http.MethodGet, "/console/api/docs/"+strconv.Itoa(int(doc.ID))+"/meta", "")
	a.DocContentMeta(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		ContentVersion int64 `json:"content_version"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ContentVersion != 7 {
		t.Fatalf("content_version=%d want 7", resp.ContentVersion)
	}
}

func TestDocContentMetaForbiddenOutsider(t *testing.T) {
	a, doc := newAuthApp(t)
	users, err := loadTestUsers(a)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: strconv.Itoa(int(doc.ID))}}
	asUser(c, users["outsider"], http.MethodGet, "/x", "")
	a.DocContentMeta(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", w.Code)
	}
}

func TestShareContentMeta(t *testing.T) {
	a, doc := newAuthApp(t)
	a.Signer = session.NewSigner("0123456789abcdef0123456789abcdef")
	exp := time.Now().Add(24 * time.Hour)
	share := model.Share{
		DocumentID: doc.ID,
		ShareToken: "metatok12",
		ExpireAt:   &exp,
	}
	if err := a.DB.Create(&share).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Model(doc).Updates(map[string]any{"content_version": int64(3), "version": "v1.0.3"}).Error; err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "token", Value: share.ShareToken}}
	c.Request = httptest.NewRequest(http.MethodGet, "/s/"+share.ShareToken+"/meta", nil)
	c.Request.Header.Set("X-Requested-With", "XMLHttpRequest")
	a.ShareContentMeta(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		ContentVersion int64  `json:"content_version"`
		Version        string `json:"version"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ContentVersion != 3 || resp.Version != "v1.0.3" {
		t.Fatalf("resp=%+v", resp)
	}
}

func TestProjectShareContentMeta(t *testing.T) {
	a, doc := newAuthApp(t)
	if err := a.DB.AutoMigrate(&model.ProjectShare{}); err != nil {
		t.Fatal(err)
	}
	a.Signer = session.NewSigner("0123456789abcdef0123456789abcdef")
	ps := model.ProjectShare{ProjectID: doc.ProjectID, ShareToken: "psmetatok1"}
	if err := a.DB.Create(&ps).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Model(doc).Update("content_version", int64(5)).Error; err != nil {
		t.Fatal(err)
	}

	docID := strconv.Itoa(int(doc.ID))
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{
		{Key: "token", Value: ps.ShareToken},
		{Key: "docId", Value: docID},
	}
	c.Request = httptest.NewRequest(http.MethodGet, "/ps/"+ps.ShareToken+"/d/"+docID+"/meta", nil)
	c.Request.Header.Set("X-Requested-With", "XMLHttpRequest")
	a.ProjectShareContentMeta(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		ContentVersion int64 `json:"content_version"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ContentVersion != 5 {
		t.Fatalf("content_version=%d want 5", resp.ContentVersion)
	}
}

func TestProjectShareContentMetaLocked(t *testing.T) {
	a, doc := newAuthApp(t)
	if err := a.DB.AutoMigrate(&model.ProjectShare{}); err != nil {
		t.Fatal(err)
	}
	a.Signer = session.NewSigner("0123456789abcdef0123456789abcdef")
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	ps := model.ProjectShare{
		ProjectID:  doc.ProjectID,
		ShareToken: "psmetalock",
		Password:   string(hash),
	}
	if err := a.DB.Create(&ps).Error; err != nil {
		t.Fatal(err)
	}

	docID := strconv.Itoa(int(doc.ID))
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{
		{Key: "token", Value: ps.ShareToken},
		{Key: "docId", Value: docID},
	}
	c.Request = httptest.NewRequest(http.MethodGet, "/ps/"+ps.ShareToken+"/d/"+docID+"/meta", nil)
	c.Request.Header.Set("X-Requested-With", "XMLHttpRequest")
	a.ProjectShareContentMeta(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403 body=%s", w.Code, w.Body.String())
	}
}

func TestContentMetaPath(t *testing.T) {
	if got := contentMetaPath(9, "", "/ps/tok/d/9"); got != "/ps/tok/d/9/meta" {
		t.Fatalf("apiBase: %s", got)
	}
	if got := contentMetaPath(9, "sharetok", ""); got != "/s/sharetok/meta" {
		t.Fatalf("share: %s", got)
	}
	if got := contentMetaPath(9, "", ""); got != "/console/api/docs/9/meta" {
		t.Fatalf("preview: %s", got)
	}
}
