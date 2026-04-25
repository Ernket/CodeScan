package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"codescan/internal/model"
	"codescan/internal/service/orchestration"

	"github.com/gin-gonic/gin"
)

func TestOrchestrationReadHandlersReturnNotFoundWhenTaskMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)

	restore := setOrchestrationHandlerTestDeps(t, orchestrationHandlerTestDeps{
		taskExists: func(string) (bool, error) {
			return false, nil
		},
		subscribe: func(string) (<-chan model.TaskEvent, func()) {
			t.Fatalf("subscribe should not run when task is missing")
			return nil, func() {}
		},
	})
	defer restore()

	tests := []struct {
		name    string
		handler gin.HandlerFunc
		path    string
		query   string
	}{
		{name: "snapshot", handler: GetOrchestrationSnapshotHandler, path: "/api/tasks/task-1/orchestration"},
		{name: "subtasks", handler: GetOrchestrationSubtasksHandler, path: "/api/tasks/task-1/orchestration/subtasks"},
		{name: "agents", handler: GetOrchestrationAgentsHandler, path: "/api/tasks/task-1/orchestration/agents"},
		{name: "events", handler: OrchestrationEventsHandler, path: "/api/tasks/task-1/orchestration/events", query: "?after=10"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, tc.path+tc.query, nil)
			c.Params = gin.Params{{Key: "id", Value: "task-1"}}

			tc.handler(c)

			if w.Code != http.StatusNotFound {
				t.Fatalf("expected status %d, got %d with body %s", http.StatusNotFound, w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "Task not found") {
				t.Fatalf("expected task not found body, got %s", w.Body.String())
			}
		})
	}
}

func TestOrchestrationEventsHandlerStreamsHistoryForExistingTask(t *testing.T) {
	gin.SetMode(gin.TestMode)

	restore := setOrchestrationHandlerTestDeps(t, orchestrationHandlerTestDeps{
		taskExists: func(taskID string) (bool, error) {
			return true, nil
		},
		loadEvents: func(taskID string, after uint64, limit int) ([]model.TaskEvent, error) {
			return []model.TaskEvent{
				{Sequence: 11, ID: "ev-11", TaskID: taskID, Message: "history"},
			}, nil
		},
		subscribe: func(taskID string) (<-chan model.TaskEvent, func()) {
			ch := make(chan model.TaskEvent)
			close(ch)
			return ch, func() {}
		},
	})
	defer restore()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/tasks/task-1/orchestration/events?after=10", nil)
	c.Params = gin.Params{{Key: "id", Value: "task-1"}}
	OrchestrationEventsHandler(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, w.Code, w.Body.String())
	}
	if contentType := w.Header().Get("Content-Type"); contentType != "text/event-stream" {
		t.Fatalf("expected SSE content type, got %q", contentType)
	}
	if !strings.Contains(w.Body.String(), `"message":"history"`) {
		t.Fatalf("expected history event in stream body, got %s", w.Body.String())
	}
}

type orchestrationHandlerTestDeps struct {
	taskExists   func(taskID string) (bool, error)
	loadSnapshot func(taskID string) (*orchestration.Snapshot, error)
	loadSubtasks func(taskID string) ([]model.TaskSubtask, error)
	loadAgents   func(taskID string) ([]model.TaskAgentRun, error)
	loadEvents   func(taskID string, after uint64, limit int) ([]model.TaskEvent, error)
	subscribe    func(taskID string) (<-chan model.TaskEvent, func())
}

func setOrchestrationHandlerTestDeps(t *testing.T, deps orchestrationHandlerTestDeps) func() {
	t.Helper()

	oldTaskExists := taskExistsForOrchestration
	oldLoadSnapshot := loadOrchestrationSnapshot
	oldLoadSubtasks := loadOrchestrationSubtasks
	oldLoadAgents := loadOrchestrationAgents
	oldLoadEvents := loadOrchestrationEvents
	oldSubscribe := subscribeOrchestrationEvents

	taskExistsForOrchestration = func(taskID string) (bool, error) {
		if deps.taskExists != nil {
			return deps.taskExists(taskID)
		}
		return true, nil
	}
	loadOrchestrationSnapshot = func(taskID string) (*orchestration.Snapshot, error) {
		if deps.loadSnapshot != nil {
			return deps.loadSnapshot(taskID)
		}
		return &orchestration.Snapshot{}, nil
	}
	loadOrchestrationSubtasks = func(taskID string) ([]model.TaskSubtask, error) {
		if deps.loadSubtasks != nil {
			return deps.loadSubtasks(taskID)
		}
		return []model.TaskSubtask{}, nil
	}
	loadOrchestrationAgents = func(taskID string) ([]model.TaskAgentRun, error) {
		if deps.loadAgents != nil {
			return deps.loadAgents(taskID)
		}
		return []model.TaskAgentRun{}, nil
	}
	loadOrchestrationEvents = func(taskID string, after uint64, limit int) ([]model.TaskEvent, error) {
		if deps.loadEvents != nil {
			return deps.loadEvents(taskID, after, limit)
		}
		return []model.TaskEvent{}, nil
	}
	subscribeOrchestrationEvents = func(taskID string) (<-chan model.TaskEvent, func()) {
		if deps.subscribe != nil {
			return deps.subscribe(taskID)
		}
		ch := make(chan model.TaskEvent)
		close(ch)
		return ch, func() {}
	}

	return func() {
		taskExistsForOrchestration = oldTaskExists
		loadOrchestrationSnapshot = oldLoadSnapshot
		loadOrchestrationSubtasks = oldLoadSubtasks
		loadOrchestrationAgents = oldLoadAgents
		loadOrchestrationEvents = oldLoadEvents
		subscribeOrchestrationEvents = oldSubscribe
	}
}
