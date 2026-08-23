package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codecodriver/internal/domain"
	"codecodriver/internal/indexer"
	"codecodriver/internal/runtime"
	"codecodriver/internal/sandbox"
	"codecodriver/internal/store"
)

type serverTestWorkspace struct{}

func (serverTestWorkspace) ReadFile(_ context.Context, _ string, _, _ int) (map[string]any, error) {
	return map[string]any{"content": ""}, nil
}

func (serverTestWorkspace) SearchFiles(_ context.Context, _ string, _ int) ([]map[string]any, error) {
	return nil, nil
}

func (serverTestWorkspace) ReadSymbols(_ context.Context, _ string, _ int) ([]map[string]any, error) {
	return nil, nil
}

func (serverTestWorkspace) EditFile(_ context.Context, _ string, _, _, _ string, _, _ int) (map[string]any, error) {
	return map[string]any{"changed": true}, nil
}

func (serverTestWorkspace) WriteFile(_ context.Context, _ string, _ string) (map[string]any, error) {
	return map[string]any{"changed": true}, nil
}

func (serverTestWorkspace) GeneratePatch(_ context.Context) (string, error) {
	return "--- a/sample.go\n+++ b/sample.go\n", nil
}

func (serverTestWorkspace) Reset(_ context.Context) error { return nil }

func (serverTestWorkspace) RunTest(_ context.Context, _ string) sandbox.Report {
	return sandbox.Report{Status: "passed", Applied: true, Passed: true}
}

func (serverTestWorkspace) Close(_ context.Context) error { return nil }

func TestHealthAndValidation(t *testing.T) {
	data := store.NewMemory()
	engine := runtime.NewService(data, indexer.New())
	engine.Start(context.Background())
	handler := New(data, engine)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d", rec.Code)
	}
	var health map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	if health["service"] != "CodeCoDriver" {
		t.Fatalf("unexpected health response: %+v", health)
	}

	req = httptest.NewRequest(http.MethodPost, "/tasks", strings.NewReader(`{"repository_id":"missing","description":"test"}`))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("validation status = %d", rec.Code)
	}
}

func TestCancelTaskEndpoint(t *testing.T) {
	data := store.NewMemory()
	repo := domain.Repository{ID: "repo-1", Name: "sample", Path: t.TempDir(), CreatedAt: time.Now()}
	if err := data.AddRepository(repo); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task-1", RepositoryID: repo.ID, Status: domain.TaskCreated, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := data.AddTask(task); err != nil {
		t.Fatal(err)
	}
	engine := runtime.NewService(data, indexer.New())
	handler := New(data, engine)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/tasks/task-1/cancel", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	got, err := data.Task(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.TaskCancelled {
		t.Fatalf("status=%s", got.Status)
	}
}

func TestHumanReviewEndpoint(t *testing.T) {
	data := store.NewMemory()
	repo := domain.Repository{ID: "repo-1", Name: "sample", Path: t.TempDir(), CreatedAt: time.Now()}
	if err := data.AddRepository(repo); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task-human", RepositoryID: repo.ID, Status: domain.TaskHumanReview, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := data.AddTask(task); err != nil {
		t.Fatal(err)
	}
	engine := runtime.NewService(data, indexer.New())
	handler := New(data, engine)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/human-reviews/task-human/approve", strings.NewReader(`{}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("approve status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	got, err := data.Task(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.TaskCompleted {
		t.Fatalf("status=%s", got.Status)
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/human-reviews/task-human/reject", strings.NewReader(`{"reason":"unsafe patch"}`)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("reject status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestTaskExecutionEndToEnd(t *testing.T) {
	repoDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module sample\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "sample.go"), []byte("package sample\n\nfunc Add(a, b int) int { return a + b }\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	data := store.NewMemory()
	engine := runtime.NewService(data, indexer.New())
	engine.SetWorkspaceFactory(func(_ context.Context, _ string) (sandbox.Workspace, error) {
		return serverTestWorkspace{}, nil
	})
	engine.Start(ctx)
	handler := New(data, engine)

	repoPayload, _ := json.Marshal(map[string]string{"name": "sample", "path": repoDir})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/repositories", strings.NewReader(string(repoPayload))))
	if rec.Code != http.StatusCreated {
		t.Fatalf("repository status=%d body=%s", rec.Code, rec.Body.String())
	}
	var repo struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&repo); err != nil {
		t.Fatal(err)
	}

	taskPayload, _ := json.Marshal(map[string]string{"repository_id": repo.ID, "title": "Improve Add", "description": "Inspect Add and propose a reliability improvement"})
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/tasks", strings.NewReader(string(taskPayload))))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("task status=%d body=%s", rec.Code, rec.Body.String())
	}
	var task struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		current, err := data.Task(task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status == domain.TaskCompleted || current.Status == domain.TaskHumanReview {
			break
		}
		if current.Status == domain.TaskFailed {
			t.Fatalf("task failed: %s", current.Error)
		}
		if time.Now().After(deadline) {
			t.Fatal("task execution timed out")
		}
		time.Sleep(20 * time.Millisecond)
	}
	steps, err := data.Steps(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(steps); got != 11 {
		t.Fatalf("steps=%d", got)
	}
	artifacts, err := data.Artifacts(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(artifacts); got != 12 {
		t.Fatalf("artifacts=%d", got)
	}
	memories, err := data.SearchMemory(repo.ID, "execution ended")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(memories); got != 1 {
		t.Fatalf("memories=%d", got)
	}
}
