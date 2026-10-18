package session

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// Signer 基于 HMAC 的无状态签名工具，用于 session 与分享凭证
type Signer struct {
	secret []byte
}

func NewSigner(secret string) *Signer {
	return &Signer{secret: []byte(secret)}
}

// Sign 对 payload 生成签名，返回 "payload.hexmac"
func (s *Signer) Sign(payload string) string {
	mac := s.mac(payload)
	return payload + "." + hex.EncodeToString(mac)
}

// Verify 校验签名，成功返回 payload
func (s *Signer) Verify(token string) (string, bool) {
	i := strings.LastIndex(token, ".")
	if i < 0 {
		return "", false
	}
	payload := token[:i]
	sig, err := hex.DecodeString(token[i+1:])
	if err != nil {
		return "", false
	}
	expected := s.mac(payload)
	if !hmac.Equal(sig, expected) {
		return "", false
	}
	return payload, true
}

func (s *Signer) mac(payload string) []byte {
	h := hmac.New(sha256.New, s.secret)
	h.Write([]byte(payload))
	return h.Sum(nil)
}

// DeriveKey 用会话密钥派生用途固定的 32 字节密钥（例如加密 TOTP 密钥）。
func (s *Signer) DeriveKey(purpose string) []byte {
	return s.mac("key:" + purpose)
}

// UserClaims 登录会话声明
type UserClaims struct {
	UID          uint
	TOTPVerified bool // 本会话是否已通过动态口令（开启 TOTP 后必须为 true）
}

// MakeUserToken 生成用户登录凭证。
// 格式：uid:exp 或 uid:exp:t（末段 t = 已通过动态口令）
func (s *Signer) MakeUserToken(uid uint, ttl time.Duration, totpVerified bool) string {
	exp := time.Now().Add(ttl).Unix()
	payload := strconv.FormatUint(uint64(uid), 10) + ":" + strconv.FormatInt(exp, 10)
	if totpVerified {
		payload += ":t"
	}
	return s.Sign(payload)
}

// ParseUserToken 解析登录凭证
func (s *Signer) ParseUserToken(token string) (UserClaims, error) {
	payload, ok := s.Verify(token)
	if !ok {
		return UserClaims{}, ErrInvalidToken
	}
	parts := strings.Split(payload, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return UserClaims{}, ErrInvalidToken
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return UserClaims{}, ErrExpiredToken
	}
	uid, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return UserClaims{}, ErrInvalidToken
	}
	claims := UserClaims{UID: uint(uid)}
	if len(parts) == 3 {
		if parts[2] != "t" {
			return UserClaims{}, ErrInvalidToken
		}
		claims.TOTPVerified = true
	}
	return claims, nil
}

// MakeShareToken 生成分享验证凭证： "shareToken.exp" 签名
func (s *Signer) MakeShareToken(shareToken string, ttl time.Duration) string {
	exp := time.Now().Add(ttl).Unix()
	return s.Sign(shareToken + ":" + strconv.FormatInt(exp, 10))
}

// VerifyShareToken 校验分享凭证是否对给定 shareToken 有效
func (s *Signer) VerifyShareToken(credential, shareToken string) bool {
	payload, ok := s.Verify(credential)
	if !ok {
		return false
	}
	parts := strings.SplitN(payload, ":", 2)
	if len(parts) != 2 || parts[0] != shareToken {
		return false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	return true
}
