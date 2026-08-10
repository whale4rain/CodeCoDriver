package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"codecodriver/internal/runtime"
	"codecodriver/internal/skills"
	"codecodriver/internal/store"
)

type Server struct {
	store   store.Store
	runtime *runtime.Service
}

func New(s store.Store, r *runtime.Service) http.Handler {
	api := &Server{store: s, runtime: r}
	mux := http.NewServeMux()
	api.routes(mux)
	return logging(newRateLimiter(rateLimitFromEnv(), time.Minute).middleware(mux))
}

func (s *Server) routes(m *http.ServeMux) {
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		write(w, http.StatusOK, map[string]string{"status": "ok", "service": "CodeCoDriver"})
	})
	m.HandleFunc("GET /skills", s.listSkills)
	m.HandleFunc("POST /skills", s.registerSkill)
	m.HandleFunc("POST /skills/import", s.importSkill)
	m.HandleFunc("POST /skills/reload", s.reloadSkills)
	m.HandleFunc("GET /dashboard/overview", s.dashboardOverview)
	m.HandleFunc("GET /human-reviews", s.listHumanReviews)
	m.HandleFunc("POST /human-reviews/{taskId}/approve", s.resolveHumanReview(true))
	m.HandleFunc("POST /human-reviews/{taskId}/reject", s.resolveHumanReview(false))
	m.HandleFunc("POST /human-reviews/{taskId}/feedback", s.sendHumanFeedback)
	m.HandleFunc("GET /evaluations", s.evaluations)
	m.HandleFunc("POST /evaluations/cases", s.createBenchmarkCase)
	m.HandleFunc("PUT /evaluations/cases/{id}", s.updateBenchmarkCase)
	m.HandleFunc("POST /evaluations/runs", s.createEvaluationRun)
	m.HandleFunc("POST /evaluations/suites", s.createEvaluationSuite)
	m.HandleFunc("GET /repositories", func(w http.ResponseWriter, _ *http.Request) {
		items, err := s.store.Repositories()
		if err != nil {
			problem(w, http.StatusInternalServerError, err)
			return
		}
		write(w, http.StatusOK, items)
	})
	m.HandleFunc("POST /repositories", s.createRepository)
	m.HandleFunc("GET /repositories/{id}", s.getRepository)
	m.HandleFunc("GET /repositories/{id}/overview", s.repositoryOverview)
	m.HandleFunc("POST /repositories/{id}/index", s.indexRepository)
	m.HandleFunc("GET /repositories/{id}/files", func(w http.ResponseWriter, r *http.Request) {
		items, err := s.store.Files(r.PathValue("id"))
		if err != nil {
			problem(w, http.StatusInternalServerError, err)
			return
		}
		write(w, http.StatusOK, items)
	})
	m.HandleFunc("GET /repositories/{id}/symbols", func(w http.ResponseWriter, r *http.Request) {
		items, err := s.store.Symbols(r.PathValue("id"))
		if err != nil {
			problem(w, http.StatusInternalServerError, err)
			return
		}
		write(w, http.StatusOK, items)
	})
	m.HandleFunc("GET /tasks", func(w http.ResponseWriter, _ *http.Request) {
		items, err := s.store.Tasks()
		if err != nil {
			problem(w, http.StatusInternalServerError, err)
			return
		}
		write(w, http.StatusOK, items)
	})
	m.HandleFunc("POST /tasks", s.createTask)
	m.HandleFunc("GET /tasks/{id}", s.getTask)
	m.HandleFunc("POST /tasks/{id}/cancel", s.cancelTask)
	m.HandleFunc("POST /tasks/{id}/apply", s.applyTaskPatch)
	m.HandleFunc("POST /tasks/{id}/rerun", s.rerunTask)
	m.HandleFunc("GET /tasks/{id}/runs", func(w http.ResponseWriter, r *http.Request) {
		items, err := s.store.Runs(r.PathValue("id"))
		if err != nil {
			problem(w, http.StatusInternalServerError, err)
			return
		}
		write(w, http.StatusOK, items)
	})
	m.HandleFunc("GET /tasks/{id}/trace", s.trace)
	m.HandleFunc("GET /tasks/{id}/timeline", s.timeline)
	m.HandleFunc("GET /memory/search", func(w http.ResponseWriter, r *http.Request) {
		items, err := s.store.SearchMemory(r.URL.Query().Get("repository_id"), r.URL.Query().Get("query"))
		if err != nil {
			problem(w, http.StatusInternalServerError, err)
			return
		}
		write(w, http.StatusOK, items)
	})
}

func (s *Server) createRepository(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Path        string `json:"path"`
		TestCommand string `json:"test_command"`
	}
	if err := decode(r, &req); err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	repo, err := s.runtime.RegisterRepository(req.Name, req.Path, req.TestCommand)
	if err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	write(w, http.StatusCreated, repo)
}
func (s *Server) getRepository(w http.ResponseWriter, r *http.Request) {
	repo, err := s.store.Repository(r.PathValue("id"))
	if err != nil {
		problem(w, http.StatusNotFound, err)
		return
	}
	write(w, http.StatusOK, repo)
}
func (s *Server) indexRepository(w http.ResponseWriter, r *http.Request) {
	repo, err := s.runtime.IndexRepository(r.PathValue("id"))
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		problem(w, status, err)
		return
	}
	write(w, http.StatusOK, repo)
}
func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryID string `json:"repository_id"`
		Title        string `json:"title"`
		Description  string `json:"description"`
		SkillName    string `json:"skill_name"`
	}
	if err := decode(r, &req); err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	task, err := s.runtime.CreateTaskWithSkill(req.RepositoryID, req.Title, req.Description, req.SkillName)
	if err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	write(w, http.StatusAccepted, task)
}

func (s *Server) listSkills(w http.ResponseWriter, _ *http.Request) {
	write(w, http.StatusOK, s.runtime.ListSkills())
}

func (s *Server) registerSkill(w http.ResponseWriter, r *http.Request) {
	var skill skills.Skill
	if err := decode(r, &skill); err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	if err := s.runtime.RegisterSkill(skill); err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	write(w, http.StatusCreated, skill)
}

func (s *Server) importSkill(w http.ResponseWriter, r *http.Request) {
	var request struct {
		URL string `json:"url"`
	}
	if err := decode(r, &request); err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	imported, err := s.runtime.ImportSkillFromGitHub(r.Context(), request.URL)
	if err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"imported": imported})
}

func (s *Server) reloadSkills(w http.ResponseWriter, _ *http.Request) {
	if err := s.runtime.ReloadSkills(); err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"skills": s.runtime.ListSkills()})
}
func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	task, err := s.store.Task(r.PathValue("id"))
	if err != nil {
		problem(w, http.StatusNotFound, err)
		return
	}
	write(w, http.StatusOK, task)
}

func (s *Server) cancelTask(w http.ResponseWriter, r *http.Request) {
	if err := s.runtime.CancelTask(r.PathValue("id")); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		problem(w, status, err)
		return
	}
	task, err := s.store.Task(r.PathValue("id"))
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	write(w, http.StatusOK, task)
}

func (s *Server) applyTaskPatch(w http.ResponseWriter, r *http.Request) {
	result, err := s.runtime.ApplyTaskPatch(r.PathValue("id"))
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		problem(w, status, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"task_id": r.PathValue("id"), "status": result.Status, "applied_files": result.Files, "warnings": result.Warnings})
}

func (s *Server) rerunTask(w http.ResponseWriter, r *http.Request) {
	task, err := s.runtime.RerunTask(r.PathValue("id"))
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		problem(w, status, err)
		return
	}
	write(w, http.StatusAccepted, task)
}

func (s *Server) trace(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.store.Task(id); err != nil {
		problem(w, http.StatusNotFound, err)
		return
	}
	runs, err := s.store.Runs(id)
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	steps, err := s.store.Steps(id)
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	artifacts, err := s.store.Artifacts(id)
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	toolCalls, err := s.store.ToolCalls(id)
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	llmUsages, err := s.store.LLMUsages(id)
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"task_id": id, "runs": runs, "steps": steps, "tool_calls": toolCalls, "llm_usages": llmUsages, "artifacts": artifacts})
}

func decode(r *http.Request, v any) error {
	defer r.Body.Close()
	d := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	d.DisallowUnknownFields()
	return d.Decode(v)
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func problem(w http.ResponseWriter, status int, err error) {
	write(w, status, map[string]string{"error": err.Error()})
}
func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = strings.TrimSuffix(r.URL.Path, "/")
		if r.URL.Path == "" {
			r.URL.Path = "/"
		}
		next.ServeHTTP(w, r)
	})
}
