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

func TestEvaluationMetricsSeparateHumanReview(t *testing.T) {
	data := store.NewMemory()
	repo := domain.Repository{ID: "repo-metrics", Name: "sample", Path: t.TempDir(), CreatedAt: time.Now()}
	if err := data.AddRepository(repo); err != nil {
		t.Fatal(err)
	}
	if err := data.AddBenchmarkCase(domain.BenchmarkCase{ID: "case-metrics", Name: "metrics", RepositoryID: repo.ID, Title: "metrics", Description: "metrics", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	runs := []domain.EvaluationRun{
		{ID: "run-completed", CaseID: "case-metrics", Mode: "with_memory", Status: "completed", Passed: true, DurationMS: 100, StartedAt: now, CreatedAt: now},
		{ID: "run-human", CaseID: "case-metrics", Mode: "with_memory", Status: "human_review_required", Passed: false, DurationMS: 200, StartedAt: now, CreatedAt: now},
		{ID: "run-failed", CaseID: "case-metrics", Mode: "with_memory", Status: "failed", Passed: false, DurationMS: 300, StartedAt: now, CreatedAt: now},
	}
	for _, run := range runs {
		if err := data.AddEvaluationRun(run); err != nil {
			t.Fatal(err)
		}
	}
	engine := runtime.NewService(data, indexer.New())
	handler := New(data, engine)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/evaluations", nil)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Metrics struct {
			Total       int     `json:"total"`
			Passed      int     `json:"passed"`
			HumanReview int     `json:"human_review"`
			Failed      int     `json:"failed"`
			PassRate    float64 `json:"pass_rate"`
		} `json:"metrics"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Metrics.Total != 3 || response.Metrics.Passed != 1 || response.Metrics.HumanReview != 1 || response.Metrics.Failed != 1 {
		t.Fatalf("metrics=%+v", response.Metrics)
	}
	if response.Metrics.PassRate != 0.5 {
		t.Fatalf("pass_rate=%v", response.Metrics.PassRate)
	}
}
