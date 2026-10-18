package session

import (
	"testing"
	"time"
)

func TestUserTokenTOTPFlag(t *testing.T) {
	s := NewSigner("0123456789abcdef0123456789abcdef")
	plain := s.MakeUserToken(7, time.Hour, false)
	claims, err := s.ParseUserToken(plain)
	if err != nil || claims.UID != 7 || claims.TOTPVerified {
		t.Fatalf("plain: %+v err=%v", claims, err)
	}
	verified := s.MakeUserToken(7, time.Hour, true)
	claims, err = s.ParseUserToken(verified)
	if err != nil || claims.UID != 7 || !claims.TOTPVerified {
		t.Fatalf("verified: %+v err=%v", claims, err)
	}
}
