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
	"codecodriver/internal/store"
)

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
		if current.Status == domain.TaskCompleted {
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
	if got := len(data.Steps(task.ID)); got != 11 {
		t.Fatalf("steps=%d", got)
	}
	if got := len(data.Artifacts(task.ID)); got != 11 {
		t.Fatalf("artifacts=%d", got)
	}
	if got := len(data.SearchMemory(repo.ID, "completed")); got != 1 {
		t.Fatalf("memories=%d", got)
	}
}
