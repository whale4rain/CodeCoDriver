package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"codecodriver/internal/domain"
	"codecodriver/internal/indexer"
	"codecodriver/internal/runtime"
	"codecodriver/internal/store"
)

func TestDashboardOverviewAndTimeline(t *testing.T) {
	data := store.NewMemory()
	now := time.Now().UTC()
	repo := domain.Repository{ID: "repo-dashboard", Name: "dashboard", Path: t.TempDir(), CreatedAt: now}
	if err := data.AddRepository(repo); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task-dashboard", RepositoryID: repo.ID, Title: "dashboard task", Status: domain.TaskCompleted, CreatedAt: now, UpdatedAt: now}
	if err := data.AddTask(task); err != nil {
		t.Fatal(err)
	}
	run := domain.TaskRun{ID: "run-dashboard", TaskID: task.ID, Status: domain.TaskCompleted, StartedAt: now.Add(-time.Second), EndedAt: now}
	if err := data.AddRun(run); err != nil {
		t.Fatal(err)
	}
	if err := data.AddStep(domain.TaskStep{ID: "step-dashboard", TaskID: task.ID, RunID: run.ID, AgentName: "planner", Status: "COMPLETED", StartedAt: now.Add(-time.Second), EndedAt: now}); err != nil {
		t.Fatal(err)
	}
	handler := New(data, runtime.NewService(data, indexer.New()))
	request := httptest.NewRequest(http.MethodGet, "/dashboard/overview", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("overview status=%d body=%s", response.Code, response.Body.String())
	}
	var overview map[string]any
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil || overview["completed"] != float64(1) {
		t.Fatalf("overview=%v err=%v", overview, err)
	}
	request = httptest.NewRequest(http.MethodGet, "/tasks/task-dashboard/timeline", nil).WithContext(context.Background())
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("timeline status=%d", response.Code)
	}
	var timeline struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.NewDecoder(response.Body).Decode(&timeline); err != nil || len(timeline.Events) != 1 {
		t.Fatalf("timeline=%v err=%v", timeline, err)
	}
}

func TestTimelineIncludesPersistedAppliedState(t *testing.T) {
	data := store.NewMemory()
	now := time.Now().UTC()
	task := domain.Task{ID: "task-applied", RepositoryID: "repo-applied", Status: domain.TaskCompleted, CreatedAt: now, UpdatedAt: now}
	if err := data.AddTask(task); err != nil {
		t.Fatal(err)
	}
	if err := data.AddArtifact(domain.Artifact{
		ID:        "artifact-applied",
		TaskID:    task.ID,
		Type:      "applied_patch",
		Name:      "applied-patch.json",
		Content:   `{"status":"applied","applied":true,"passed":true,"changed_files":["internal/auth/api.go"],"output":"ok\napply warning"}`,
		CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	handler := New(data, runtime.NewService(data, indexer.New()))
	request := httptest.NewRequest(http.MethodGet, "/tasks/task-applied/timeline", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var timeline struct {
		Applied map[string]any `json:"applied"`
	}
	if err := json.NewDecoder(response.Body).Decode(&timeline); err != nil {
		t.Fatal(err)
	}
	if timeline.Applied["status"] != "applied" {
		t.Fatalf("applied=%v", timeline.Applied)
	}
	warnings, ok := timeline.Applied["warnings"].([]any)
	if !ok || len(warnings) != 2 {
		t.Fatalf("warnings=%v ok=%v", timeline.Applied["warnings"], ok)
	}
}
