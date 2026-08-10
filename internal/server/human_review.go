package server

import (
	"errors"
	"net/http"

	"codecodriver/internal/domain"
	"codecodriver/internal/store"
)

func (s *Server) listHumanReviews(w http.ResponseWriter, _ *http.Request) {
	tasks, err := s.store.Tasks()
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	reviews := make([]domain.Task, 0)
	for _, task := range tasks {
		if task.Status == domain.TaskHumanReview {
			reviews = append(reviews, task)
		}
	}
	write(w, http.StatusOK, reviews)
}

func (s *Server) resolveHumanReview(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Reason string `json:"reason"`
		}
		if r.ContentLength != 0 {
			if err := decode(r, &request); err != nil {
				problem(w, http.StatusBadRequest, err)
				return
			}
		}
		task, err := s.runtime.ResolveHumanReview(r.PathValue("taskId"), approve, request.Reason)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, store.ErrNotFound) {
				status = http.StatusNotFound
			}
			problem(w, status, err)
			return
		}
		write(w, http.StatusOK, task)
	}
}

func (s *Server) sendHumanFeedback(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Feedback string `json:"feedback"`
	}
	if err := decode(r, &request); err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	task, err := s.runtime.ContinueTaskWithFeedback(r.PathValue("taskId"), request.Feedback)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		problem(w, status, err)
		return
	}
	write(w, http.StatusOK, task)
}
