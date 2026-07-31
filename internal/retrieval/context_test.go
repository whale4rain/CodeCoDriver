package retrieval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codecodriver/internal/domain"
)

func TestBuilderReadsNumberedSourceAndFiltersSecrets(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("TOKEN=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := domain.Repository{ID: "repo-1", Path: root, CreatedAt: time.Now()}
	files := []domain.RepositoryFile{{Path: "main.go", Language: "go"}, {Path: ".env"}}
	pack := New(Config{}).Build(repo, files)
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
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "large.go"), []byte(strings.Repeat("x", 100)), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := domain.Repository{ID: "repo-1", Path: root}
	pack := New(Config{MaxFiles: 1, MaxFileBytes: 10, MaxTotalBytes: 10}).Build(repo, []domain.RepositoryFile{{Path: "large.go", Language: "go"}})
	if pack.TotalBytes != 10 || !pack.Snippets[0].Truncated {
		t.Fatalf("pack=%+v", pack)
	}
}

func TestReadRepositoryFileRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if _, _, err := readRepositoryFile(root, "../outside.txt", 100); err == nil {
		t.Fatal("expected traversal error")
	}
}
