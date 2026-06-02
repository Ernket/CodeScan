package middleware

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"codescan/internal/database"
	"codescan/internal/model"
	authsvc "codescan/internal/service/auth"
)

func TestAuthMiddlewareAcceptsValidBearerToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupMiddlewareAuthDB(t)
	user := createMiddlewareAuthUser(t, db, "operator", model.RoleUser, true, 2)
	token := mustMiddlewareToken(t, "secret", user, time.Now(), authsvc.DefaultTokenTTL)

	w := performMiddlewareAuthRequest(token, "secret")

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), model.RoleUser) {
		t.Fatalf("expected normalized user role in response, got %s", w.Body.String())
	}
}

func TestAuthMiddlewareNormalizesLegacyOrdinaryRoles(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupMiddlewareAuthDB(t)
	user := createMiddlewareAuthUser(t, db, "legacy-admin", model.RoleAdmin, true, 2)
	token := mustMiddlewareToken(t, "secret", user, time.Now(), authsvc.DefaultTokenTTL)

	w := performMiddlewareAuthRequest(token, "secret")

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), model.RoleUser) {
		t.Fatalf("expected legacy admin to be normalized to user, got %s", w.Body.String())
	}
}

func TestAuthMiddlewareRejectsMissingToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupMiddlewareAuthDB(t)

	w := performMiddlewareAuthRequest("", "secret")

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusUnauthorized, w.Code, w.Body.String())
	}
}

func TestAuthMiddlewareRejectsExpiredToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupMiddlewareAuthDB(t)
	user := createMiddlewareAuthUser(t, db, "observer", model.RoleObserver, true, 1)
	token := mustMiddlewareToken(t, "secret", user, time.Now().Add(-2*time.Hour), time.Hour)

	w := performMiddlewareAuthRequest(token, "secret")

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusUnauthorized, w.Code, w.Body.String())
	}
}

func TestAuthMiddlewareRejectsBadSignature(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupMiddlewareAuthDB(t)
	user := createMiddlewareAuthUser(t, db, "observer", model.RoleObserver, true, 1)
	token := mustMiddlewareToken(t, "secret", user, time.Now(), authsvc.DefaultTokenTTL)

	w := performMiddlewareAuthRequest(token, "wrong-secret")

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusUnauthorized, w.Code, w.Body.String())
	}
}

func TestAuthMiddlewareRejectsDisabledUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupMiddlewareAuthDB(t)
	user := createMiddlewareAuthUser(t, db, "disabled", model.RoleObserver, false, 1)
	token := mustMiddlewareToken(t, "secret", user, time.Now(), authsvc.DefaultTokenTTL)

	w := performMiddlewareAuthRequest(token, "secret")

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusUnauthorized, w.Code, w.Body.String())
	}
}

func TestAuthMiddlewareRejectsStaleTokenVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupMiddlewareAuthDB(t)
	user := createMiddlewareAuthUser(t, db, "admin", model.RoleAdmin, true, 1)
	token := mustMiddlewareToken(t, "secret", user, time.Now(), authsvc.DefaultTokenTTL)
	if err := db.Model(&model.User{}).Where("id = ?", user.ID).Update("token_version", 2).Error; err != nil {
		t.Fatalf("update token version: %v", err)
	}

	w := performMiddlewareAuthRequest(token, "secret")

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusUnauthorized, w.Code, w.Body.String())
	}
}

func TestRequireWriteAllowsOnlySuperAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, tc := range []struct {
		name       string
		role       string
		wantStatus int
	}{
		{name: "legacy_admin", role: model.RoleAdmin, wantStatus: http.StatusForbidden},
		{name: "super_admin", role: model.RoleSuperAdmin, wantStatus: http.StatusOK},
		{name: "observer", role: model.RoleObserver, wantStatus: http.StatusForbidden},
		{name: "user", role: model.RoleUser, wantStatus: http.StatusForbidden},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			r.POST("/write", func(c *gin.Context) {
				c.Set("current_user_role", tc.role)
			}, RequireWrite(), func(c *gin.Context) {
				c.JSON(http.StatusOK, gin.H{"ok": true})
			})

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/write", nil)
			r.ServeHTTP(w, req)

			if w.Code != tc.wantStatus {
				t.Fatalf("expected status %d, got %d with body %s", tc.wantStatus, w.Code, w.Body.String())
			}
		})
	}
}

func setupMiddlewareAuthDB(t *testing.T) *gorm.DB {
	t.Helper()

	previousDB := database.DB
	dbPath := filepath.Join(t.TempDir(), "middleware-auth.sqlite")
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

func createMiddlewareAuthUser(t *testing.T, db *gorm.DB, username, role string, enabled bool, tokenVersion int) model.User {
	t.Helper()

	user := model.User{
		Username:     username,
		PasswordHash: "unused",
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

func mustMiddlewareToken(t *testing.T, signingKey string, user model.User, now time.Time, ttl time.Duration) string {
	t.Helper()

	token, err := authsvc.GenerateToken(signingKey, user, now, ttl)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	return token
}

func performMiddlewareAuthRequest(token, signingKey string) *httptest.ResponseRecorder {
	r := gin.New()
	r.GET("/protected", AuthMiddleware(signingKey), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"role": CurrentUserRole(c)})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	r.ServeHTTP(w, req)
	return w
}
