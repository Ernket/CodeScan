package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"codescan/internal/database"
	"codescan/internal/model"
	authsvc "codescan/internal/service/auth"
)

func TestLoginHandlerSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupAuthHandlerDB(t)
	user := createAuthTestUser(t, db, "admin", "correct-password", model.RoleSuperAdmin, true, 3)
	now := time.Unix(100, 0)
	restore := setAuthClockForTest(now)
	defer restore()

	w := performLoginRequest(t, "token-secret", map[string]string{
		"username": "admin",
		"password": "correct-password",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, w.Code, w.Body.String())
	}

	var payload struct {
		Token string       `json:"token"`
		User  userResponse `json:"user"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal login response: %v", err)
	}
	if payload.Token == "" {
		t.Fatal("expected token in login response")
	}
	if payload.User.ID != user.ID || payload.User.Username != "admin" || payload.User.Role != model.RoleSuperAdmin {
		t.Fatalf("unexpected user response: %+v", payload.User)
	}
	claims, err := authsvc.ParseToken(payload.Token, "token-secret", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("parse issued token: %v", err)
	}
	if claims.UserID != user.ID || claims.TokenVersion != 3 {
		t.Fatalf("unexpected token claims: %+v", claims)
	}
}

func TestLoginHandlerNormalizesLegacyOrdinaryRole(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupAuthHandlerDB(t)
	user := createAuthTestUser(t, db, "operator", "correct-password", model.RoleAdmin, true, 2)
	now := time.Unix(100, 0)
	restore := setAuthClockForTest(now)
	defer restore()

	w := performLoginRequest(t, "token-secret", map[string]string{
		"username": "operator",
		"password": "correct-password",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, w.Code, w.Body.String())
	}

	var payload struct {
		Token string       `json:"token"`
		User  userResponse `json:"user"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal login response: %v", err)
	}
	if payload.User.ID != user.ID || payload.User.Username != "operator" {
		t.Fatalf("unexpected user response: %+v", payload.User)
	}
	if payload.User.Role != model.RoleUser || payload.User.IsSuperAdmin {
		t.Fatalf("expected ordinary user response, got %+v", payload.User)
	}

	claims, err := authsvc.ParseToken(payload.Token, "token-secret", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("parse issued token: %v", err)
	}
	if claims.Role != model.RoleUser {
		t.Fatalf("expected token role %q, got %q", model.RoleUser, claims.Role)
	}
}

func TestLoginHandlerRejectsWrongPassword(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupAuthHandlerDB(t)
	createAuthTestUser(t, db, "admin", "correct-password", model.RoleSuperAdmin, true, 1)

	w := performLoginRequest(t, "token-secret", map[string]string{
		"username": "admin",
		"password": "wrong-password",
	})

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusUnauthorized, w.Code, w.Body.String())
	}
}

func TestLoginHandlerRejectsDisabledUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupAuthHandlerDB(t)
	createAuthTestUser(t, db, "observer", "password", model.RoleObserver, false, 1)

	w := performLoginRequest(t, "token-secret", map[string]string{
		"username": "observer",
		"password": "password",
	})

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusForbidden, w.Code, w.Body.String())
	}
}

func TestLoginHandlerRejectsMissingUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupAuthHandlerDB(t)

	w := performLoginRequest(t, "token-secret", map[string]string{
		"username": "missing",
		"password": "password",
	})

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusUnauthorized, w.Code, w.Body.String())
	}
}

func setupAuthHandlerDB(t *testing.T) *gorm.DB {
	t.Helper()

	previousDB := database.DB
	dbPath := filepath.Join(t.TempDir(), "auth-handler.sqlite")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("unwrap sqlite database: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("auto-migrate user schema: %v", err)
	}
	database.DB = db
	t.Cleanup(func() {
		database.DB = previousDB
		_ = sqlDB.Close()
	})
	return db
}

func createAuthTestUser(t *testing.T, db *gorm.DB, username, password, role string, enabled bool, tokenVersion int) model.User {
	t.Helper()

	hash, err := authsvc.HashPassword(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	user := model.User{
		Username:     username,
		PasswordHash: hash,
		Role:         role,
		Enabled:      enabled,
		TokenVersion: tokenVersion,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create test user: %v", err)
	}
	if !enabled {
		if err := db.Model(&model.User{}).Where("id = ?", user.ID).Update("enabled", false).Error; err != nil {
			t.Fatalf("disable test user: %v", err)
		}
		user.Enabled = false
	}
	return user
}

func performLoginRequest(t *testing.T, signingKey string, payload map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal login payload: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	LoginHandler(signingKey)(c)
	return w
}

func setAuthClockForTest(now time.Time) func() {
	oldClock := authClock
	authClock = func() time.Time {
		return now
	}
	return func() {
		authClock = oldClock
	}
}
