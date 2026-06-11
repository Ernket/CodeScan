package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"codescan/internal/database"
	"codescan/internal/model"
	orgsvc "codescan/internal/service/organization"
	summarysvc "codescan/internal/service/summary"
)

func TestGetTasksHandlerFiltersByOrganizationSubtree(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTaskOrganizationFilterDB(t)

	root := createTaskFilterOrganization(t, db, "Root", nil)
	child := createTaskFilterOrganization(t, db, "Child", &root.ID)
	other := createTaskFilterOrganization(t, db, "Other", nil)

	createTaskFilterTask(t, db, "task-root", root.ID, "Root Task")
	createTaskFilterTask(t, db, "task-child", child.ID, "Child Task")
	createTaskFilterTask(t, db, "task-other", other.ID, "Other Task")

	w := performTaskFilterGetTasksRequest(root.ID, model.User{ID: 1, Username: "super", Role: model.RoleSuperAdmin})

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, w.Code, w.Body.String())
	}

	var payload []summarysvc.TaskListItem
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal tasks response: %v", err)
	}
	if got := taskFilterIDs(payload); strings.Join(got, ",") != "task-child,task-root" {
		t.Fatalf("expected root subtree tasks, got %v", got)
	}
}

func TestGetStatsHandlerFiltersByOrganizationSubtree(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTaskOrganizationFilterDB(t)

	root := createTaskFilterOrganization(t, db, "Root", nil)
	child := createTaskFilterOrganization(t, db, "Child", &root.ID)
	other := createTaskFilterOrganization(t, db, "Other", nil)

	createTaskFilterTask(t, db, "task-root", root.ID, "Root Task")
	createTaskFilterTask(t, db, "task-child", child.ID, "Child Task")
	createTaskFilterTask(t, db, "task-other", other.ID, "Other Task")

	w := performTaskFilterGetStatsRequest(root.ID, model.User{ID: 1, Username: "super", Role: model.RoleSuperAdmin})

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, w.Code, w.Body.String())
	}

	var payload summarysvc.Stats
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal stats response: %v", err)
	}
	if payload.Projects != 2 {
		t.Fatalf("expected 2 projects in selected subtree, got %d", payload.Projects)
	}
	if payload.StatusBreakdown.Pending != 2 {
		t.Fatalf("expected 2 pending tasks in selected subtree, got %+v", payload.StatusBreakdown)
	}
}

func TestGetTasksHandlerRejectsInaccessibleOrganizationFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTaskOrganizationFilterDB(t)

	allowed := createTaskFilterOrganization(t, db, "Allowed", nil)
	denied := createTaskFilterOrganization(t, db, "Denied", nil)
	user := model.User{ID: 42, Username: "operator", Role: model.RoleUser, Enabled: true}

	if err := db.Create(&model.OrganizationMembership{
		UserID:         user.ID,
		OrganizationID: allowed.ID,
		Role:           model.OrganizationRoleMember,
	}).Error; err != nil {
		t.Fatalf("create membership: %v", err)
	}

	w := performTaskFilterGetTasksRequest(denied.ID, user)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusNotFound, w.Code, w.Body.String())
	}
}

func TestGetTasksHandlerRejectsInvalidOrganizationFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupTaskOrganizationFilterDB(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	setTaskFilterCurrentUser(c, model.User{ID: 1, Username: "super", Role: model.RoleSuperAdmin})
	c.Request = httptest.NewRequest(http.MethodGet, "/api/tasks?organization_id=bad", nil)

	GetTasksHandler(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, w.Code, w.Body.String())
	}
}

func setupTaskOrganizationFilterDB(t *testing.T) *gorm.DB {
	t.Helper()

	previousDB := database.DB
	dbPath := filepath.Join(t.TempDir(), "task-organization-filter.sqlite")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("unwrap sqlite database: %v", err)
	}
	if err := db.AutoMigrate(&model.Organization{}, &model.OrganizationMembership{}, &model.Task{}, &model.TaskStage{}); err != nil {
		t.Fatalf("auto-migrate sqlite schema: %v", err)
	}
	database.DB = db
	t.Cleanup(func() {
		database.DB = previousDB
		_ = sqlDB.Close()
	})
	return db
}

func createTaskFilterOrganization(t *testing.T, db *gorm.DB, name string, parentID *uint) model.Organization {
	t.Helper()
	org, err := orgsvc.Create(db, name, parentID)
	if err != nil {
		t.Fatalf("create organization %s: %v", name, err)
	}
	return org
}

func createTaskFilterTask(t *testing.T, db *gorm.DB, id string, organizationID uint, name string) {
	t.Helper()
	task := model.Task{
		ID:             id,
		Name:           name,
		Status:         "pending",
		OrganizationID: &organizationID,
		CreatedAt:      time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC),
		Logs:           []string{},
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create task %s: %v", id, err)
	}
}

func performTaskFilterGetTasksRequest(organizationID uint, user model.User) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	setTaskFilterCurrentUser(c, user)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/tasks?organization_id="+taskFilterIDString(organizationID), nil)
	GetTasksHandler(c)
	return w
}

func performTaskFilterGetStatsRequest(organizationID uint, user model.User) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	setTaskFilterCurrentUser(c, user)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/stats?organization_id="+taskFilterIDString(organizationID), nil)
	GetStatsHandler(c)
	return w
}

func setTaskFilterCurrentUser(c *gin.Context, user model.User) {
	if user.Enabled == false {
		user.Enabled = true
	}
	c.Set("current_user", user)
	c.Set("current_user_id", user.ID)
	c.Set("current_user_role", user.Role)
}

func taskFilterIDs(items []summarysvc.TaskListItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	sort.Strings(ids)
	return ids
}

func taskFilterIDString(id uint) string {
	return strconv.FormatUint(uint64(id), 10)
}
