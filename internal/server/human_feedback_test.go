package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"codecodriver/internal/domain"
	"codecodriver/internal/indexer"
	"codecodriver/internal/runtime"
	"codecodriver/internal/store"
)

func TestHumanFeedbackEndpointRequeuesTask(t *testing.T) {
	data := store.NewMemory()
	task := domain.Task{ID: "task-feedback-api", RepositoryID: "repo-1", Status: domain.TaskHumanReview, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := data.AddTask(task); err != nil {
		t.Fatal(err)
	}
	engine := runtime.NewService(data, indexer.New())
	handler := New(data, engine)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/human-reviews/task-feedback-api/feedback", strings.NewReader(`{"feedback":"Re-run go test ./internal/auth/"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var updated domain.Task
	if err := json.NewDecoder(rec.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status != domain.TaskCreated {
		t.Fatalf("status=%s", updated.Status)
	}
}
