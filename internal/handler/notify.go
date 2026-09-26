package handler

import (
	"fmt"
	"html"
	"log"
	"net/mail"
	"strings"
	"unicode/utf8"

	"doc-share/internal/model"
	"doc-share/internal/util"
)

// notifyPayload 一条待投递消息（站内必达登录用户；邮件旁路）
type notifyPayload struct {
	UserID  uint   // 站内收件人；0 则只尝试 EmailTo
	Kind    string
	Title   string
	Body    string
	Link    string // 相对路径
	RefType string
	RefID   uint
	EmailTo string // 可选：游客联系方式邮箱等，无 UserID 时仅邮件
}

func clipRunes(s string, n int) string {
	if n <= 0 || s == "" {
		return s
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	// 截断结果必须 ≤ n（含省略号），避免撑破 gorm size 约束导致写入失败
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

// docNotifyLink 评论类通知优先走分享页（外部评论者通常无管理端预览权限）
func (a *App) docNotifyLink(doc *model.Document) string {
	if doc == nil {
		return ""
	}
	var share model.Share
	if a.DB.Select("share_token").Where("document_id = ?", doc.ID).First(&share).Error == nil && share.ShareToken != "" {
		return "/s/" + share.ShareToken
	}
	return fmt.Sprintf("/admin/docs/%d/preview", doc.ID)
}

func looksLikeEmail(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || !strings.Contains(s, "@") {
		return false
	}
	_, err := mail.ParseAddress(s)
	return err == nil
}

// siteOrigin 邮件绝对链接前缀（仅系统域名；异步场景无请求上下文）
func (a *App) siteOrigin() string {
	d := strings.TrimRight(strings.TrimSpace(a.Settings().SiteDomain), "/")
	return d
}

func (a *App) absoluteLink(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return a.siteOrigin()
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if origin := a.siteOrigin(); origin != "" {
		return origin + path
	}
	return path
}

// notify 写入站内消息并异步尝试发邮件；失败只打日志，不影响主流程。
func (a *App) notify(p notifyPayload) {
	p.Title = strings.TrimSpace(p.Title)
	p.Body = strings.TrimSpace(p.Body)
	p.Link = strings.TrimSpace(p.Link)
	p.Kind = strings.TrimSpace(p.Kind)
	if p.Title == "" || p.Kind == "" {
		return
	}
	p.Title = clipRunes(p.Title, 200)
	p.Body = clipRunes(p.Body, 1000)
	p.Link = clipRunes(p.Link, 500)

	if p.UserID > 0 {
		m := model.Message{
			UserID:  p.UserID,
			Kind:    p.Kind,
			Title:   p.Title,
			Body:    p.Body,
			Link:    p.Link,
			RefType: p.RefType,
			RefID:   p.RefID,
		}
		if err := a.DB.Create(&m).Error; err != nil {
			log.Printf("[notify] 写站内消息失败 user=%d kind=%s: %v", p.UserID, p.Kind, err)
		}
	}

	emailTo := strings.TrimSpace(p.EmailTo)
	if emailTo == "" && p.UserID > 0 {
		var u model.User
		if a.DB.Select("id, email").First(&u, p.UserID).Error == nil {
			emailTo = strings.TrimSpace(u.Email)
		}
	}
	if !looksLikeEmail(emailTo) {
		return
	}
	s := a.Settings()
	if s.SMTPHost == "" {
		return
	}
	cfg := s.MailConfig()
	subject := p.Title
	if name := strings.TrimSpace(s.SiteName); name != "" {
		subject = name + " · " + p.Title
	}
	abs := a.absoluteLink(p.Link)
	var b strings.Builder
	b.WriteString("<div style=\"font-family:sans-serif;font-size:14px;line-height:1.6;color:#222\">")
	b.WriteString("<p style=\"margin:0 0 12px;font-size:16px;font-weight:600\">")
	b.WriteString(html.EscapeString(p.Title))
	b.WriteString("</p>")
	if p.Body != "" {
		b.WriteString("<p style=\"margin:0 0 16px;color:#555\">")
		b.WriteString(html.EscapeString(p.Body))
		b.WriteString("</p>")
	}
	if abs != "" {
		b.WriteString("<p style=\"margin:0\"><a href=\"")
		b.WriteString(html.EscapeString(abs))
		b.WriteString("\">查看详情</a></p>")
	}
	b.WriteString("</div>")
	body := b.String()
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[notify] 发信 panic: %v", rec)
			}
		}()
		if err := util.SendMail(cfg, emailTo, subject, body); err != nil {
			log.Printf("[notify] 发信失败 to=%s kind=%s: %v", emailTo, p.Kind, err)
		}
	}()
}

func roleLabel(role string) string {
	switch role {
	case model.MemberRoleEdit:
		return "编辑"
	case model.MemberRoleView:
		return "查看"
	default:
		return role
	}
}

func (a *App) notifyAccessApply(doc *model.Document, applicantName string) {
	if doc == nil || doc.OwnerID == 0 {
		return
	}
	name := strings.TrimSpace(applicantName)
	if name == "" {
		name = "访客"
	}
	a.notify(notifyPayload{
		UserID:  doc.OwnerID,
		Kind:    model.MsgAccessApply,
		Title:   "收到文档查看申请",
		Body:    fmt.Sprintf("%s 申请查看「%s」", name, clipRunes(doc.Title, 80)),
		Link:    fmt.Sprintf("/admin/docs/%d/edit", doc.ID),
		RefType: "document",
		RefID:   doc.ID,
	})
}

func (a *App) notifyAccessReviewed(req *model.ShareAccessRequest, doc *model.Document, approved bool, shareToken string) {
	if req == nil || doc == nil {
		return
	}
	kind := model.MsgAccessRejected
	title := "查看申请未通过"
	body := fmt.Sprintf("你对「%s」的查看申请未通过", clipRunes(doc.Title, 80))
	link := ""
	if shareToken != "" {
		link = "/s/" + shareToken
	}
	if approved {
		kind = model.MsgAccessApproved
		title = "查看申请已通过"
		body = fmt.Sprintf("你对「%s」的查看申请已通过，可以打开分享链接阅读", clipRunes(doc.Title, 80))
	}
	p := notifyPayload{
		UserID:  req.UserID,
		Kind:    kind,
		Title:   title,
		Body:    body,
		Link:    link,
		RefType: "access_request",
		RefID:   req.ID,
	}
	if req.UserID == 0 && looksLikeEmail(req.GuestContact) {
		p.EmailTo = strings.TrimSpace(req.GuestContact)
	}
	a.notify(p)
}

func (a *App) notifyNewComment(doc *model.Document, cm *model.Comment, actorName string, parent *model.Comment) {
	if doc == nil || cm == nil {
		return
	}
	name := strings.TrimSpace(actorName)
	if name == "" {
		name = "访客"
	}
	snippet := clipRunes(strings.TrimSpace(cm.Content), 80)
	if snippet == "" && cm.Images != "" {
		snippet = "[图片]"
	}
	link := a.docNotifyLink(doc)
	// 回复：通知被回复的登录用户
	if parent != nil && parent.UserID > 0 && parent.UserID != cm.UserID {
		a.notify(notifyPayload{
			UserID:  parent.UserID,
			Kind:    model.MsgCommentReply,
			Title:   "收到评论回复",
			Body:    fmt.Sprintf("%s 回复了你在「%s」的评论：%s", name, clipRunes(doc.Title, 60), snippet),
			Link:    link,
			RefType: "comment",
			RefID:   cm.ID,
		})
	}
	// 文档属主（非自己评论时）
	if doc.OwnerID > 0 && doc.OwnerID != cm.UserID {
		if parent != nil && parent.UserID == doc.OwnerID {
			return // 属主已作为被回复者收到回复通知
		}
		a.notify(notifyPayload{
			UserID:  doc.OwnerID,
			Kind:    model.MsgComment,
			Title:   "文档收到新评论",
			Body:    fmt.Sprintf("%s 评论了「%s」：%s", name, clipRunes(doc.Title, 60), snippet),
			Link:    link,
			RefType: "document",
			RefID:   doc.ID,
		})
	}
}

func (a *App) notifyProjectMemberAdded(project *model.Project, userID uint, role string) {
	if project == nil || userID == 0 || userID == project.OwnerID {
		return
	}
	a.notify(notifyPayload{
		UserID:  userID,
		Kind:    model.MsgProjectAdded,
		Title:   "你被加入项目",
		Body:    fmt.Sprintf("你已被加入项目「%s」，角色：%s", clipRunes(project.Name, 80), roleLabel(role)),
		Link:    "/admin/projects",
		RefType: "project",
		RefID:   project.ID,
	})
}

func (a *App) notifyProjectMemberRole(project *model.Project, userID uint, role string) {
	if project == nil || userID == 0 {
		return
	}
	a.notify(notifyPayload{
		UserID:  userID,
		Kind:    model.MsgProjectRole,
		Title:   "项目角色已更新",
		Body:    fmt.Sprintf("你在项目「%s」的角色已变更为：%s", clipRunes(project.Name, 80), roleLabel(role)),
		Link:    "/admin/projects",
		RefType: "project",
		RefID:   project.ID,
	})
}

func (a *App) notifyProjectMemberRemoved(project *model.Project, userID uint) {
	if project == nil || userID == 0 {
		return
	}
	a.notify(notifyPayload{
		UserID:  userID,
		Kind:    model.MsgProjectRemoved,
		Title:   "你已离开项目",
		Body:    fmt.Sprintf("你已从项目「%s」中被移除", clipRunes(project.Name, 80)),
		Link:    "/admin/projects",
		RefType: "project",
		RefID:   project.ID,
	})
}

func (a *App) notifyShareEdited(doc *model.Document, editorName string, editorID uint) {
	if doc == nil || doc.OwnerID == 0 || editorID == doc.OwnerID {
		return
	}
	name := strings.TrimSpace(editorName)
	if name == "" {
		name = "协作者"
	}
	a.notify(notifyPayload{
		UserID:  doc.OwnerID,
		Kind:    model.MsgShareEdited,
		Title:   "分享文档被编辑",
		Body:    fmt.Sprintf("%s 通过分享链接更新了「%s」", name, clipRunes(doc.Title, 80)),
		Link:    fmt.Sprintf("/admin/docs/%d/edit", doc.ID),
		RefType: "document",
		RefID:   doc.ID,
	})
}

func (a *App) notifyDocUpdated(doc *model.Document, editorName string, editorID uint) {
	if doc == nil || doc.OwnerID == 0 || editorID == doc.OwnerID {
		return
	}
	name := strings.TrimSpace(editorName)
	if name == "" {
		name = "协作者"
	}
	a.notify(notifyPayload{
		UserID:  doc.OwnerID,
		Kind:    model.MsgDocUpdated,
		Title:   "你的文档被更新",
		Body:    fmt.Sprintf("%s 更新了文档「%s」", name, clipRunes(doc.Title, 80)),
		Link:    fmt.Sprintf("/admin/docs/%d/edit", doc.ID),
		RefType: "document",
		RefID:   doc.ID,
	})
}
