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

func TestCreateUserHandlerIgnoresRequestedSystemRole(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupUserHandlerDB(t)
	org := createUserHandlerTestOrganization(t, db, "Engineering")

	w := performCreateUserRequest(t, map[string]any{
		"username": "operator",
		"password": "secret-password",
		"role":     model.RoleSuperAdmin,
		"organization_assignments": []map[string]any{
			{"organization_id": org.ID, "role": model.OrganizationRoleAdmin},
		},
	})

	if w.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusCreated, w.Code, w.Body.String())
	}

	var payload userResponse
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal create user response: %v", err)
	}
	if payload.Role != model.RoleUser {
		t.Fatalf("expected response role %q, got %q", model.RoleUser, payload.Role)
	}
	if payload.IsSuperAdmin {
		t.Fatal("expected created user not to be a super admin")
	}

	var user model.User
	if err := db.Preload("OrganizationMemberships").First(&user, "username = ?", "operator").Error; err != nil {
		t.Fatalf("reload created user: %v", err)
	}
	if user.Role != model.RoleUser {
		t.Fatalf("expected stored role %q, got %q", model.RoleUser, user.Role)
	}
	if len(user.OrganizationMemberships) != 1 {
		t.Fatalf("expected one organization membership, got %d", len(user.OrganizationMemberships))
	}
	if user.OrganizationMemberships[0].OrganizationID != org.ID || user.OrganizationMemberships[0].Role != model.OrganizationRoleAdmin {
		t.Fatalf("unexpected organization membership: %+v", user.OrganizationMemberships[0])
	}
}

func TestChangeOwnPasswordHandlerUpdatesPasswordAndRevokesTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupUserHandlerDB(t)
	user := createUserHandlerTestUser(t, db, "operator", "old-password", model.RoleUser, 4)

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
	user := createUserHandlerTestUser(t, db, "operator", "old-password", model.RoleUser, 4)

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
	user := createUserHandlerTestUser(t, db, "operator", "old-password", model.RoleUser, 4)

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
	target := createUserHandlerTestUser(t, db, "observer", "old-password", model.RoleUser, 2)

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

func createUserHandlerTestOrganization(t *testing.T, db *gorm.DB, name string) model.Organization {
	t.Helper()

	org := model.Organization{Name: name, Path: "", Depth: 0}
	if err := db.Create(&org).Error; err != nil {
		t.Fatalf("create test organization: %v", err)
	}
	org.Path = "/" + strconv.FormatUint(uint64(org.ID), 10) + "/"
	if err := db.Model(&model.Organization{}).Where("id = ?", org.ID).Update("path", org.Path).Error; err != nil {
		t.Fatalf("update test organization path: %v", err)
	}
	return org
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
