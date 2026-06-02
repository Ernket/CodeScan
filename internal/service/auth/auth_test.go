package auth

import (
	"errors"
	"testing"
	"time"

	"codescan/internal/model"
)

func TestParseTokenRejectsEmptySigningKey(t *testing.T) {
	user := model.User{
		ID:           1,
		Username:     "admin",
		Role:         model.RoleSuperAdmin,
		TokenVersion: 1,
	}
	now := time.Unix(100, 0)
	token, err := GenerateToken("token-secret", user, now, time.Hour)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	if _, err := ParseToken(token, "   ", now.Add(time.Minute)); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken for empty signing key, got %v", err)
	}
}

func TestParseTokenTrimsSigningKeyLikeGenerateToken(t *testing.T) {
	user := model.User{
		ID:           1,
		Username:     "admin",
		Role:         model.RoleSuperAdmin,
		TokenVersion: 1,
	}
	now := time.Unix(100, 0)
	token, err := GenerateToken(" token-secret ", user, now, time.Hour)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	claims, err := ParseToken(token, " token-secret ", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("parse token: %v", err)
	}
	if claims.UserID != user.ID || claims.TokenVersion != user.TokenVersion {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}
