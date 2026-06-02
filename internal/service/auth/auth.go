package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"codescan/internal/model"
)

const DefaultTokenTTL = 24 * time.Hour

var (
	ErrInvalidToken = errors.New("invalid token")
	ErrExpiredToken = errors.New("token expired")
	ErrInvalidRole  = errors.New("invalid role")
)

type Claims struct {
	UserID       uint   `json:"sub"`
	Username     string `json:"username"`
	Role         string `json:"role"`
	TokenVersion int    `json:"ver"`
	ExpiresAt    int64  `json:"exp"`
}

func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func GenerateToken(signingKey string, user model.User, now time.Time, ttl time.Duration) (string, error) {
	signingKey = strings.TrimSpace(signingKey)
	if signingKey == "" {
		return "", fmt.Errorf("token signing key is empty")
	}
	if ttl <= 0 {
		ttl = DefaultTokenTTL
	}

	claims := Claims{
		UserID:       user.ID,
		Username:     user.Username,
		Role:         ResponseRole(user.Role),
		TokenVersion: user.TokenVersion,
		ExpiresAt:    now.Add(ttl).Unix(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}

	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	signature := sign(encodedPayload, signingKey)
	return encodedPayload + "." + signature, nil
}

func ParseToken(token, signingKey string, now time.Time) (Claims, error) {
	var claims Claims
	signingKey = strings.TrimSpace(signingKey)
	if signingKey == "" {
		return claims, ErrInvalidToken
	}
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return claims, ErrInvalidToken
	}
	if !hmac.Equal([]byte(parts[1]), []byte(sign(parts[0], signingKey))) {
		return claims, ErrInvalidToken
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return claims, ErrInvalidToken
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return claims, ErrInvalidToken
	}
	if claims.UserID == 0 || claims.TokenVersion <= 0 || claims.ExpiresAt <= 0 {
		return claims, ErrInvalidToken
	}
	if now.Unix() >= claims.ExpiresAt {
		return claims, ErrExpiredToken
	}
	return claims, nil
}

func NormalizeRole(role string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case model.RoleSuperAdmin:
		return model.RoleSuperAdmin, nil
	case model.RoleUser:
		return model.RoleUser, nil
	case model.RoleAdmin:
		return model.RoleUser, nil
	case model.RoleObserver:
		return model.RoleUser, nil
	default:
		return "", ErrInvalidRole
	}
}

func ResponseRole(role string) string {
	normalized, err := NormalizeRole(role)
	if err != nil {
		return ""
	}
	return normalized
}

func CanRead(role string) bool {
	_, err := NormalizeRole(role)
	return err == nil
}

func CanWrite(role string) bool {
	normalized, err := NormalizeRole(role)
	return err == nil && normalized == model.RoleSuperAdmin
}

func CanDelete(role string) bool {
	normalized, err := NormalizeRole(role)
	return err == nil && normalized == model.RoleSuperAdmin
}

func CanManageUsers(role string) bool {
	normalized, err := NormalizeRole(role)
	return err == nil && normalized == model.RoleSuperAdmin
}

func sign(payload, signingKey string) string {
	mac := hmac.New(sha256.New, []byte(signingKey))
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
