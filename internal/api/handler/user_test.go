package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"codescan/internal/database"
	"codescan/internal/model"
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

func setupUserHandlerDB(t *testing.T) {
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
