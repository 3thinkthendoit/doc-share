package handler

import (
	"testing"
	"time"

	"doc-share/internal/model"
	"doc-share/internal/session"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestShareGatePayloadBindsPassword(t *testing.T) {
	tok := "0123456789abcdef0123456789abcdef"
	if shareGatePayload(tok, "") != tok {
		t.Fatal("empty password should be bare token")
	}
	a := shareGatePayload(tok, "$2a$10$abcdefghijklmnopqrstuv")
	b := shareGatePayload(tok, "$2a$10$abcdefghijklmnopqrstuv")
	c := shareGatePayload(tok, "$2a$10$CHANGED_____________")
	if a != b {
		t.Fatal("same hash must same payload")
	}
	if a == c {
		t.Fatal("password rotate must change payload")
	}
	if a == tok {
		t.Fatal("passworded gate must differ from bare token")
	}
}

func TestShareCookieInvalidAfterPasswordRotate(t *testing.T) {
	s := session.NewSigner("0123456789abcdef0123456789abcdef")
	tok := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	oldHash := "$2a$10$oldpasswordhashvaluexx"
	cred := s.MakeShareToken(shareGatePayload(tok, oldHash), time.Hour)
	if !s.VerifyShareToken(cred, shareGatePayload(tok, oldHash)) {
		t.Fatal("cred should verify against old hash")
	}
	newHash := "$2a$10$newpasswordhashvaluexx"
	if s.VerifyShareToken(cred, shareGatePayload(tok, newHash)) {
		t.Fatal("cred must NOT verify after password rotate")
	}
}

func TestDocContentIfPublicShareLive(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:share_live?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Document{}, &model.Share{}, &model.ProjectShare{}); err != nil {
		t.Fatal(err)
	}
	a := &App{DB: db}
	doc := model.Document{Title: "t", Content: "secret body", Slug: "sluglive1"}
	if err := db.Create(&doc).Error; err != nil {
		t.Fatal(err)
	}
	if _, ok := a.docContentIfPublicShareLive(doc.ID); ok {
		t.Fatal("no share => not live")
	}
	if err := db.Create(&model.Share{DocumentID: doc.ID, ShareToken: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}).Error; err != nil {
		t.Fatal(err)
	}
	got, ok := a.docContentIfPublicShareLive(doc.ID)
	if !ok || got.Content != "secret body" {
		t.Fatalf("live share should return content, ok=%v content=%q", ok, got.Content)
	}
	if err := db.Where("document_id = ?", doc.ID).Delete(&model.Share{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, ok := a.docContentIfPublicShareLive(doc.ID); ok {
		t.Fatal("after delete share => not live")
	}
}

func TestRevokeApprovedDocAccess(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:revoke_appr?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.ShareAccessRequest{}); err != nil {
		t.Fatal(err)
	}
	a := &App{DB: db}
	req := model.ShareAccessRequest{
		DocumentID: 9, Status: model.AccessApproved, RequestToken: "tokrevoketest0001",
	}
	pending := model.ShareAccessRequest{
		DocumentID: 9, Status: model.AccessPending, RequestToken: "tokrevoketest0002",
	}
	if err := db.Create(&req).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&pending).Error; err != nil {
		t.Fatal(err)
	}
	a.revokeApprovedDocAccess(9)
	var nApproved, nPending int64
	db.Model(&model.ShareAccessRequest{}).Where("document_id = ? AND status = ?", 9, model.AccessApproved).Count(&nApproved)
	db.Model(&model.ShareAccessRequest{}).Where("document_id = ? AND status = ?", 9, model.AccessPending).Count(&nPending)
	if nApproved != 0 {
		t.Fatalf("approved left=%d", nApproved)
	}
	if nPending != 1 {
		t.Fatalf("pending should remain, got %d", nPending)
	}
}
