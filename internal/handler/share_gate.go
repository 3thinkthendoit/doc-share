package handler

import (
	"crypto/sha256"
	"encoding/hex"

	"doc-share/internal/model"
)

// shareGatePayload 分享密码门与 HTML /raw 签名的材料：无密码时仅为 token；
// 有密码时附加密码哈希指纹，改密/清密后旧 cookie 与 raw 签名立即失效。
func shareGatePayload(shareToken, passwordHash string) string {
	if passwordHash == "" {
		return shareToken
	}
	sum := sha256.Sum256([]byte(passwordHash))
	return shareToken + "." + hex.EncodeToString(sum[:8])
}

// docContentIfPublicShareLive 文档仍存在有效文档分享或项目分享时返回正文，供非成员访客注入。
// 分享已关闭/过期时返回 ok=false，避免聊天/评论智能体在撤销后继续读私密正文。
func (a *App) docContentIfPublicShareLive(docID uint) (model.Document, bool) {
	var doc model.Document
	if a.DB.First(&doc, docID).Error != nil {
		return doc, false
	}
	var sh model.Share
	if a.DB.Where("document_id = ?", docID).First(&sh).Error == nil && !sh.IsExpired() {
		return doc, true
	}
	if doc.ProjectID > 0 {
		var ps model.ProjectShare
		if a.DB.Where("project_id = ?", doc.ProjectID).First(&ps).Error == nil && !ps.IsExpired() {
			return doc, true
		}
	}
	return doc, false
}

// revokeApprovedDocAccess 改密或清密后作废已批准的文档访问申请（cookie 仍在也找不到有效行）。
func (a *App) revokeApprovedDocAccess(docID uint) {
	a.DB.Where("document_id = ? AND status = ?", docID, model.AccessApproved).
		Delete(&model.ShareAccessRequest{})
}

// revokeApprovedProjectAccess 改密或清密后作废已批准的项目访问申请。
func (a *App) revokeApprovedProjectAccess(projectID uint) {
	a.DB.Where("project_id = ? AND status = ?", projectID, model.AccessApproved).
		Delete(&model.ShareAccessRequest{})
}
