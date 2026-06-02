package handler

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"codescan/internal/config"
	"codescan/internal/database"
	"codescan/internal/model"
	"codescan/internal/utils"
)

func TestUploadHandlerRejectsOversizedFile(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("name", "oversized"); err != nil {
		t.Fatalf("WriteField(name) error = %v", err)
	}
	if err := writer.WriteField("remark", "test"); err != nil {
		t.Fatalf("WriteField(remark) error = %v", err)
	}
	part, err := writer.CreateFormFile("file", "source.zip")
	if err != nil {
		t.Fatalf("CreateFormFile() error = %v", err)
	}
	if _, err := part.Write([]byte("zip")); err != nil {
		t.Fatalf("file part write error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("multipart writer close error = %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	setTestCurrentUser(c)

	req := httptest.NewRequest(http.MethodPost, "/api/tasks", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if err := req.ParseMultipartForm(int64(body.Len()) + 1024); err != nil {
		t.Fatalf("ParseMultipartForm() error = %v", err)
	}
	req.MultipartForm.File["file"][0].Size = utils.MaxUploadFileSize + 1
	c.Request = req

	UploadHandler(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, w.Code)
	}
	if !strings.Contains(w.Body.String(), "200MB") {
		t.Fatalf("expected upload size limit to mention 200MB, got %s", w.Body.String())
	}
}

func TestUploadHandlerCreatesPendingTaskWithoutAutoStart(t *testing.T) {
	gin.SetMode(gin.TestMode)

	root := t.TempDir()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWd)
	})
	if err := os.MkdirAll(config.ProjectsDir, 0755); err != nil {
		t.Fatalf("MkdirAll(projects) error = %v", err)
	}

	previousDB := database.DB
	db, err := gorm.Open(sqlite.Open(filepath.Join(root, "upload.sqlite")), &gorm.Config{})
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

	oldTaskID := newTaskID
	newTaskID = func() string { return "task-pending" }
	t.Cleanup(func() {
		newTaskID = oldTaskID
	})

	org := model.Organization{Name: "Engineering", Path: "/1/", Depth: 0}
	if err := database.DB.Create(&org).Error; err != nil {
		t.Fatalf("create organization: %v", err)
	}
	if err := database.DB.Model(&org).Update("path", "/1/").Error; err != nil {
		t.Fatalf("update organization path: %v", err)
	}

	body, contentType := buildUploadRequestBody(t, "demo.zip", map[string]string{
		"main.go": "package main\nfunc main() {}\n",
	})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	setTestCurrentUser(c)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/tasks", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", contentType)

	UploadHandler(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, w.Code, w.Body.String())
	}

	var payload model.Task
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal upload response: %v", err)
	}
	if payload.ID != "task-pending" {
		t.Fatalf("expected task id task-pending, got %q", payload.ID)
	}
	if payload.Status != "pending" {
		t.Fatalf("expected pending task after upload, got %q", payload.Status)
	}

	var saved model.Task
	if err := database.DB.First(&saved, "id = ?", "task-pending").Error; err != nil {
		t.Fatalf("expected task to be persisted: %v", err)
	}
	if saved.Status != "pending" {
		t.Fatalf("expected persisted task pending, got %q", saved.Status)
	}
	if saved.OrganizationID == nil || *saved.OrganizationID != org.ID {
		t.Fatalf("expected persisted task organization %d, got %v", org.ID, saved.OrganizationID)
	}
	if _, err := os.Stat(filepath.Join(config.ProjectsDir, "task-pending", "main.go")); err != nil {
		t.Fatalf("expected project files to be extracted: %v", err)
	}
}

func buildUploadRequestBody(t *testing.T, filename string, files map[string]string) ([]byte, string) {
	t.Helper()

	zipBytes := buildZipBytes(t, files)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("name", "Demo Project"); err != nil {
		t.Fatalf("WriteField(name) error = %v", err)
	}
	if err := writer.WriteField("remark", "created by test"); err != nil {
		t.Fatalf("WriteField(remark) error = %v", err)
	}
	if err := writer.WriteField("organization_id", "1"); err != nil {
		t.Fatalf("WriteField(organization_id) error = %v", err)
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("CreateFormFile() error = %v", err)
	}
	if _, err := part.Write(zipBytes); err != nil {
		t.Fatalf("write zip part error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("multipart writer close error = %v", err)
	}
	return body.Bytes(), writer.FormDataContentType()
}

func buildZipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", name, err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatalf("write zip entry %s: %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	return buffer.Bytes()
}
