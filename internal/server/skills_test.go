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
	"codecodriver/internal/skills"
	"codecodriver/internal/store"
)

func TestSkillsAPI(t *testing.T) {
	data := store.NewMemory()
	engine := runtime.NewService(data, indexer.New())
	handler := New(data, engine)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/skills", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var initial []skills.Skill
	if err := json.NewDecoder(rec.Body).Decode(&initial); err != nil {
		t.Fatal(err)
	}
	if len(initial) < 3 {
		t.Fatalf("default skills=%d", len(initial))
	}

	custom := `{"name":"api-review","description":"API contract review","keywords":["api","contract"],"prompts":{"reviewer":{"user":"Review {{task_title}}."}}}`
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/skills", strings.NewReader(custom)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("register status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/skills", nil))
	var updated []skills.Skill
	if err := json.NewDecoder(rec.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, skill := range updated {
		if skill.Name == "api-review" {
			found = true
		}
	}
	if !found {
		t.Fatalf("custom skill missing: %+v", updated)
	}
}

func TestCreateTaskPersistsSkillName(t *testing.T) {
	data := store.NewMemory()
	repo := domain.Repository{ID: "repo-skill-api", Name: "sample", Path: t.TempDir(), IndexedAt: time.Now(), CreatedAt: time.Now()}
	if err := data.AddRepository(repo); err != nil {
		t.Fatal(err)
	}
	engine := runtime.NewService(data, indexer.New())
	handler := New(data, engine)
	payload := `{"repository_id":"repo-skill-api","title":"build readme","description":"build readme","skill_name":"documentation"}`
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/tasks", strings.NewReader(payload)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var task domain.Task
	if err := json.NewDecoder(rec.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	got, err := data.Task(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SkillName != "documentation" {
		t.Fatalf("skill_name=%s", got.SkillName)
	}
}

func TestCreateTaskRejectsUnknownSkill(t *testing.T) {
	data := store.NewMemory()
	repo := domain.Repository{ID: "repo-skill-api", Name: "sample", Path: t.TempDir(), CreatedAt: time.Now()}
	if err := data.AddRepository(repo); err != nil {
		t.Fatal(err)
	}
	engine := runtime.NewService(data, indexer.New())
	handler := New(data, engine)
	payload := `{"repository_id":"repo-skill-api","title":"build readme","description":"build readme","skill_name":"missing"}`
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/tasks", strings.NewReader(payload)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
}
