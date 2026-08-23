package retrieval

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"codecodriver/internal/domain"
)

type fakeContextReader struct {
	contents map[string]string
}

func (r fakeContextReader) ReadFile(_ context.Context, path string, _, _ int) (map[string]any, error) {
	content, ok := r.contents[path]
	if !ok {
		return nil, fmt.Errorf("file not found: %s", path)
	}
	return map[string]any{"content": content}, nil
}

func TestBuilderReadsNumberedSourceAndFiltersSecrets(t *testing.T) {
	reader := fakeContextReader{contents: map[string]string{
		"main.go": "package main\n\nfunc main() {}\n",
		".env":    "TOKEN=secret\n",
	}}
	files := []domain.RepositoryFile{{Path: "main.go", Language: "go"}, {Path: ".env"}}
	pack := New(Config{}).Build(context.Background(), files, reader)
	if len(pack.Snippets) != 1 {
		t.Fatalf("snippets=%d", len(pack.Snippets))
	}
	if !strings.Contains(pack.Snippets[0].Content, "   1 | package main") {
		t.Fatalf("missing line numbers: %q", pack.Snippets[0].Content)
	}
	if len(pack.Skipped) != 1 || pack.Skipped[0].Path != ".env" {
		t.Fatalf("skipped=%+v", pack.Skipped)
	}
}

func TestBuilderEnforcesBudgets(t *testing.T) {
	reader := fakeContextReader{contents: map[string]string{
		"large.go": strings.Repeat("x", 100),
	}}
	pack := New(Config{MaxFiles: 1, MaxFileBytes: 10, MaxTotalBytes: 10}).Build(context.Background(), []domain.RepositoryFile{{Path: "large.go", Language: "go"}}, reader)
	if pack.TotalBytes != 10 || !pack.Snippets[0].Truncated {
		t.Fatalf("pack=%+v", pack)
	}
}

func TestBuilderReportsReaderErrorsAsSkipped(t *testing.T) {
	reader := fakeContextReader{contents: map[string]string{}}
	pack := New(Config{}).Build(context.Background(), []domain.RepositoryFile{{Path: "missing.go", Language: "go"}}, reader)
	if len(pack.Snippets) != 0 || len(pack.Skipped) != 1 {
		t.Fatalf("pack=%+v", pack)
	}
	if !strings.Contains(pack.Skipped[0].Reason, "file not found") {
		t.Fatalf("reason=%q", pack.Skipped[0].Reason)
	}
}

func TestDefaultContextLimits(t *testing.T) {
	builder := New(Config{})
	if builder.config.MaxFiles != 5 || builder.config.MaxTotalBytes != 32*1024 {
		t.Fatalf("unexpected defaults: %+v", builder.config)
	}
}
