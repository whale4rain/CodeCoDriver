package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimiterRejectsRequestsWithinWindow(t *testing.T) {
	limiter := newRateLimiter(1, time.Minute)
	handler := limiter.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.RemoteAddr = "192.0.2.10:1234"
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, request)
	if first.Code != http.StatusNoContent {
		t.Fatalf("first=%d", first.Code)
	}
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, request)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second=%d", second.Code)
	}
}

func TestRateLimiterUsesForwardedClientIP(t *testing.T) {
	limiter := newRateLimiter(1, time.Minute)
	handler := limiter.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	first := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	first.RemoteAddr = "192.0.2.10:1234"
	first.Header.Set("X-Forwarded-For", "198.51.100.3")
	second := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	second.RemoteAddr = "192.0.2.11:1234"
	second.Header.Set("X-Forwarded-For", "198.51.100.3")
	handler.ServeHTTP(httptest.NewRecorder(), first)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, second)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d", recorder.Code)
	}
}
