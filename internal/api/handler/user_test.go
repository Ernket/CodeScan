package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"codescan/internal/database"
	"codescan/internal/model"
	authsvc "codescan/internal/service/auth"
)

func TestCreateUserHandlerRollsBackWhenOrganizationAssignmentInvalid(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupUserHandlerDB(t)

	w := performCreateUserRequest(t, map[string]any{
		"username": "operator",
		"password": "secret-password",
		"role":     model.RoleAdmin,
		"organization_assignments": []map[string]any{
			{"organization_id": 99, "role": model.OrganizationRoleMember},
		},
	})

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusNotFound, w.Code, w.Body.String())
	}

	var userCount int64
	if err := database.DB.Model(&model.User{}).Where("username = ?", "operator").Count(&userCount).Error; err != nil {
		t.Fatalf("count users: %v", err)
	}
	if userCount != 0 {
		t.Fatalf("expected user creation to roll back, got %d matching users", userCount)
	}
}

func TestChangeOwnPasswordHandlerUpdatesPasswordAndRevokesTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupUserHandlerDB(t)
	user := createUserHandlerTestUser(t, db, "operator", "old-password", model.RoleAdmin, 4)

	w := performChangeOwnPasswordRequest(t, user, map[string]string{
		"current_password": "old-password",
		"new_password":     "new-password",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, w.Code, w.Body.String())
	}

	var updated model.User
	if err := db.First(&updated, "id = ?", user.ID).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if !authsvc.CheckPassword(updated.PasswordHash, "new-password") {
		t.Fatal("expected updated password hash to match the new password")
	}
	if authsvc.CheckPassword(updated.PasswordHash, "old-password") {
		t.Fatal("expected old password to stop matching")
	}
	if updated.TokenVersion != 5 {
		t.Fatalf("expected token version 5, got %d", updated.TokenVersion)
	}
}

func TestChangeOwnPasswordHandlerRejectsWrongCurrentPassword(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupUserHandlerDB(t)
	user := createUserHandlerTestUser(t, db, "operator", "old-password", model.RoleAdmin, 4)

	w := performChangeOwnPasswordRequest(t, user, map[string]string{
		"current_password": "wrong-password",
		"new_password":     "new-password",
	})

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusUnauthorized, w.Code, w.Body.String())
	}

	var updated model.User
	if err := db.First(&updated, "id = ?", user.ID).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if !authsvc.CheckPassword(updated.PasswordHash, "old-password") {
		t.Fatal("expected existing password to remain unchanged")
	}
	if updated.TokenVersion != 4 {
		t.Fatalf("expected token version 4, got %d", updated.TokenVersion)
	}
}

func TestChangeOwnPasswordHandlerRequiresBothPasswords(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupUserHandlerDB(t)
	user := createUserHandlerTestUser(t, db, "operator", "old-password", model.RoleAdmin, 4)

	w := performChangeOwnPasswordRequest(t, user, map[string]string{
		"current_password": "old-password",
	})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, w.Code, w.Body.String())
	}
}

func TestResetUserPasswordHandlerUpdatesTargetPasswordAndRevokesTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupUserHandlerDB(t)
	target := createUserHandlerTestUser(t, db, "observer", "old-password", model.RoleObserver, 2)

	w := performResetUserPasswordRequest(t, target.ID, map[string]string{
		"password": "new-password",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, w.Code, w.Body.String())
	}

	var updated model.User
	if err := db.First(&updated, "id = ?", target.ID).Error; err != nil {
		t.Fatalf("reload target user: %v", err)
	}
	if !authsvc.CheckPassword(updated.PasswordHash, "new-password") {
		t.Fatal("expected target password hash to match the new password")
	}
	if updated.TokenVersion != 3 {
		t.Fatalf("expected token version 3, got %d", updated.TokenVersion)
	}
}

func setupUserHandlerDB(t *testing.T) *gorm.DB {
	t.Helper()

	previousDB := database.DB
	dbPath := filepath.Join(t.TempDir(), "user-handler.sqlite")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("unwrap sqlite database: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Organization{}, &model.OrganizationMembership{}); err != nil {
		t.Fatalf("auto-migrate user schema: %v", err)
	}
	database.DB = db
	t.Cleanup(func() {
		database.DB = previousDB
		_ = sqlDB.Close()
	})
	return db
}

func createUserHandlerTestUser(t *testing.T, db *gorm.DB, username, password, role string, tokenVersion int) model.User {
	t.Helper()

	hash, err := authsvc.HashPassword(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	user := model.User{
		Username:     username,
		PasswordHash: hash,
		Role:         role,
		Enabled:      true,
		TokenVersion: tokenVersion,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create test user: %v", err)
	}
	return user
}

func performCreateUserRequest(t *testing.T, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal user payload: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/users", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	CreateUserHandler(c)
	return w
}

func performChangeOwnPasswordRequest(t *testing.T, currentUser model.User, payload map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal password payload: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/me/password", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("current_user", currentUser)
	c.Set("current_user_id", currentUser.ID)
	c.Set("current_user_role", currentUser.Role)
	ChangeOwnPasswordHandler(c)
	return w
}

func performResetUserPasswordRequest(t *testing.T, userID uint, payload map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal reset password payload: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatUint(uint64(userID), 10)}}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/users/"+strconv.FormatUint(uint64(userID), 10)+"/password", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	ResetUserPasswordHandler(c)
	return w
}
