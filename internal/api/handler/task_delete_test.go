package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"codescan/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type fakeTaskDeletionStore struct {
	tasks     map[string]model.Task
	stages    map[string][]model.TaskStage
	deleteErr error
}

func (s *fakeTaskDeletionStore) DeleteTask(id string) (model.Task, error) {
	task, ok := s.tasks[id]
	if !ok {
		return model.Task{}, errTaskDeleteNotFound
	}
	if task.Status == "running" {
		return model.Task{}, errTaskDeleteRunning
	}
	if s.deleteErr != nil {
		return model.Task{}, s.deleteErr
	}

	delete(s.tasks, id)
	delete(s.stages, id)
	return task, nil
}

func TestDeleteTaskHandlerDeletesTaskAndStages(t *testing.T) {
	gin.SetMode(gin.TestMode)

	store := &fakeTaskDeletionStore{
		tasks: map[string]model.Task{
			"task-1": {
				ID:       "task-1",
				Status:   "completed",
				BasePath: `E:\code\CodeScan-Claude\projects\task-1`,
			},
		},
		stages: map[string][]model.TaskStage{
			"task-1": {
				{ID: 1, TaskID: "task-1", Name: "init"},
				{ID: 2, TaskID: "task-1", Name: "auth"},
			},
		},
	}

	var removedPath string
	setDeleteTestDeps(t, store, func(path string) error {
		removedPath = path
		return nil
	})

	w := performDeleteTaskRequest("task-1")

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, w.Code, w.Body.String())
	}
	if _, ok := store.tasks["task-1"]; ok {
		t.Fatalf("expected task to be removed from store")
	}
	if _, ok := store.stages["task-1"]; ok {
		t.Fatalf("expected task stages to be removed from store")
	}
	if removedPath != `E:\code\CodeScan-Claude\projects\task-1` {
		t.Fatalf("expected task path to be removed, got %q", removedPath)
	}
	if !strings.Contains(w.Body.String(), `"status":"deleted"`) {
		t.Fatalf("expected deleted response body, got %s", w.Body.String())
	}
}

func TestDeleteTaskHandlerReturnsNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)

	store := &fakeTaskDeletionStore{
		tasks:  map[string]model.Task{},
		stages: map[string][]model.TaskStage{},
	}

	removed := false
	setDeleteTestDeps(t, store, func(string) error {
		removed = true
		return nil
	})

	w := performDeleteTaskRequest("missing-task")

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusNotFound, w.Code, w.Body.String())
	}
	if removed {
		t.Fatalf("expected file removal not to run for missing tasks")
	}
	if !strings.Contains(w.Body.String(), "Task not found") {
		t.Fatalf("expected not found response body, got %s", w.Body.String())
	}
}

func TestDeleteTaskHandlerRejectsRunningTask(t *testing.T) {
	gin.SetMode(gin.TestMode)

	store := &fakeTaskDeletionStore{
		tasks: map[string]model.Task{
			"task-1": {
				ID:       "task-1",
				Status:   "running",
				BasePath: `E:\code\CodeScan-Claude\projects\task-1`,
			},
		},
		stages: map[string][]model.TaskStage{
			"task-1": {
				{ID: 1, TaskID: "task-1", Name: "init", Status: "running"},
			},
		},
	}

	removed := false
	setDeleteTestDeps(t, store, func(string) error {
		removed = true
		return nil
	})

	w := performDeleteTaskRequest("task-1")

	if w.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusConflict, w.Code, w.Body.String())
	}
	if _, ok := store.tasks["task-1"]; !ok {
		t.Fatalf("expected running task to remain in store")
	}
	if _, ok := store.stages["task-1"]; !ok {
		t.Fatalf("expected running task stages to remain in store")
	}
	if removed {
		t.Fatalf("expected file removal not to run for running tasks")
	}
	if !strings.Contains(w.Body.String(), "Pause it before deleting") {
		t.Fatalf("expected running-task response body, got %s", w.Body.String())
	}
}

func TestDeleteTaskHandlerReturnsInternalServerErrorOnStoreFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)

	store := &fakeTaskDeletionStore{
		tasks: map[string]model.Task{
			"task-1": {
				ID:       "task-1",
				Status:   "completed",
				BasePath: `E:\code\CodeScan-Claude\projects\task-1`,
			},
		},
		stages: map[string][]model.TaskStage{
			"task-1": {
				{ID: 1, TaskID: "task-1", Name: "init"},
			},
		},
		deleteErr: errors.New("db write failed"),
	}

	removed := false
	setDeleteTestDeps(t, store, func(string) error {
		removed = true
		return nil
	})

	w := performDeleteTaskRequest("task-1")

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusInternalServerError, w.Code, w.Body.String())
	}
	if removed {
		t.Fatalf("expected file removal not to run when deletion fails")
	}
	if !strings.Contains(w.Body.String(), "Failed to delete task") {
		t.Fatalf("expected internal error response body, got %s", w.Body.String())
	}
}

func setDeleteTestDeps(t *testing.T, store taskDeletionStore, remove func(string) error) {
	t.Helper()

	oldFactory := newTaskDeletionStore
	oldRemove := removeTaskPath

	newTaskDeletionStore = func(_ *gorm.DB) taskDeletionStore {
		return store
	}
	removeTaskPath = remove

	t.Cleanup(func() {
		newTaskDeletionStore = oldFactory
		removeTaskPath = oldRemove
	})
}

func performDeleteTaskRequest(taskID string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodDelete, "/api/tasks/"+taskID, nil)
	c.Params = gin.Params{{Key: "id", Value: taskID}}
	DeleteTaskHandler(c)
	return w
}
