package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"doc-share/internal/config"
	"doc-share/internal/middleware"
	"doc-share/internal/model"
	"doc-share/internal/session"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

func TestPreviewExport_rejectsNonMarkdownAndOutsider(t *testing.T) {
	a, doc := newAuthApp(t)
	a.Cfg = &config.Config{}
	a.Limiter = middleware.NewShareLimiter(5, time.Minute)
	users, err := loadTestUsers(a)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("outsider_forbidden", func(t *testing.T) {
		w, c := newCtx()
		c.Params = ginParamsID(doc.ID)
		asUser(c, users["outsider"], http.MethodGet, "/console/api/docs/"+itoa(int(doc.ID))+"/export?format=pdf", "")
		a.PreviewExport(c)
		assertCode(t, w, http.StatusForbidden)
	})

	t.Run("mindmap_unsupported", func(t *testing.T) {
		if err := a.DB.Model(doc).Update("type", model.DocTypeMindmap).Error; err != nil {
			t.Fatal(err)
		}
		w, c := newCtx()
		c.Params = ginParamsID(doc.ID)
		asUser(c, users["owner"], http.MethodGet, "/console/api/docs/"+itoa(int(doc.ID))+"/export?format=pdf", "")
		a.PreviewExport(c)
		assertCode(t, w, http.StatusBadRequest)
	})

	t.Run("bad_format", func(t *testing.T) {
		if err := a.DB.Model(doc).Update("type", model.DocTypeMarkdown).Error; err != nil {
			t.Fatal(err)
		}
		w, c := newCtx()
		c.Params = ginParamsID(doc.ID)
		asUser(c, users["owner"], http.MethodGet, "/console/api/docs/"+itoa(int(doc.ID))+"/export?format=zip", "")
		c.Request.URL.RawQuery = "format=zip"
		a.PreviewExport(c)
		assertCode(t, w, http.StatusBadRequest)
	})
}

func TestShareExport_requiresUnlock(t *testing.T) {
	a, doc := newAuthApp(t)
	a.Cfg = &config.Config{}
	a.Limiter = middleware.NewShareLimiter(5, time.Minute)
	a.Signer = session.NewSigner("test-secret-key-at-least-32-bytes!!")

	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	share := model.Share{
		DocumentID: doc.ID,
		ShareToken: "exptok01",
		Password:   string(hash),
	}
	if err := a.DB.Create(&share).Error; err != nil {
		t.Fatal(err)
	}

	w, c := newCtx()
	c.Params = gin.Params{{Key: "token", Value: "exptok01"}}
	c.Request = httptest.NewRequest(http.MethodGet, "/s/exptok01/export?format=docx", nil)
	a.ShareExport(c)
	assertCode(t, w, http.StatusForbidden)
}

func TestProjectShareExport_requiresUnlock(t *testing.T) {
	a, doc := newAuthApp(t)
	if err := a.DB.AutoMigrate(&model.ProjectShare{}); err != nil {
		t.Fatal(err)
	}
	a.Cfg = &config.Config{}
	a.Signer = session.NewSigner("test-secret-key-at-least-32-bytes!!")

	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	ps := model.ProjectShare{
		ProjectID:  doc.ProjectID,
		ShareToken: "psexptok1",
		Password:   string(hash),
	}
	if err := a.DB.Create(&ps).Error; err != nil {
		t.Fatal(err)
	}

	w, c := newCtx()
	c.Params = gin.Params{
		{Key: "token", Value: "psexptok1"},
		{Key: "docId", Value: itoa(int(doc.ID))},
	}
	c.Request = httptest.NewRequest(http.MethodGet, "/ps/psexptok1/d/"+itoa(int(doc.ID))+"/export?format=pdf", nil)
	a.ProjectShareExport(c)
	assertCode(t, w, http.StatusForbidden)
}

func ginParamsID(id uint) gin.Params {
	return gin.Params{{Key: "id", Value: itoa(int(id))}}
}
