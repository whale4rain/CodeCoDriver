package server

import (
	"context"
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

func TestCreateEvaluationRunQueuesRealTask(t *testing.T) {
	data := store.NewMemory()
	repo := domain.Repository{ID: "repo-eval", Name: "sample", Path: t.TempDir(), CreatedAt: time.Now()}
	if err := data.AddRepository(repo); err != nil {
		t.Fatal(err)
	}
	if err := data.AddBenchmarkCase(domain.BenchmarkCase{ID: "case-eval", Name: "smoke", RepositoryID: repo.ID, Title: "smoke", Description: "run smoke", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	engine := runtime.NewService(data, indexer.New())
	engine.Start(context.Background())
	handler := New(data, engine)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/evaluations/runs", strings.NewReader(`{"case_id":"case-eval","mode":"agent"}`))
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Evaluation domain.EvaluationRun `json:"evaluation"`
		Task       domain.Task          `json:"task"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Evaluation.TaskID != response.Task.ID || response.Evaluation.Status != "queued" {
		t.Fatalf("response=%+v", response)
	}
	runs, err := data.AllEvaluationRuns()
	if err != nil || len(runs) != 1 || runs[0].TaskID != response.Task.ID {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
}

func TestUpdateBenchmarkCaseEndpoint(t *testing.T) {
	data := store.NewMemory()
	repo := domain.Repository{ID: "repo-eval", Name: "sample", Path: t.TempDir(), CreatedAt: time.Now()}
	if err := data.AddRepository(repo); err != nil {
		t.Fatal(err)
	}
	if err := data.AddBenchmarkCase(domain.BenchmarkCase{ID: "case-update", Name: "old-name", RepositoryID: repo.ID, Title: "old title", Description: "old description", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	engine := runtime.NewService(data, indexer.New())
	handler := New(data, engine)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/evaluations/cases/case-update", strings.NewReader(`{"name":"new-name","title":"new title","description":"new description"}`))
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	item, err := data.BenchmarkCase("case-update")
	if err != nil {
		t.Fatal(err)
	}
	if item.Name != "new-name" || item.Description != "new description" {
		t.Fatalf("item=%+v", item)
	}
}
