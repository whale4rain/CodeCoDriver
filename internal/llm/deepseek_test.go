package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeepSeekComplete(t *testing.T) {
	var gotModel, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var request chatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		gotModel = request.Model
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": "pong"}}}})
	}))
	defer server.Close()
	client := NewDeepSeek("secret", server.URL, DefaultDeepSeekModel, server.Client())
	got, err := client.Complete(context.Background(), "system", "ping")
	if err != nil {
		t.Fatal(err)
	}
	if got != "pong" {
		t.Fatalf("completion=%q", got)
	}
	if gotModel != "deepseek-v4-flash" {
		t.Fatalf("model=%q", gotModel)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("authorization header=%q", gotAuth)
	}
}
