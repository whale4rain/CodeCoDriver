package indexer

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"codecodriver/internal/domain"
)

func TestIndexGoRepository(t *testing.T) {
	dir := t.TempDir()
	source := []byte("package sample\n\ntype Worker struct{}\nfunc Run() {}\n")
	if err := os.WriteFile(filepath.Join(dir, "worker.go"), source, 0o600); err != nil {
		t.Fatal(err)
	}
	repo := domain.Repository{ID: "repo-1", Name: "sample", Path: dir, CreatedAt: time.Now()}
	got, files, symbols, err := New().Index(repo)
	if err != nil {
		t.Fatal(err)
	}
	if got.PrimaryLanguage != "go" || got.FileCount != 1 {
		t.Fatalf("unexpected repository: %+v", got)
	}
	if len(files) != 1 || len(symbols) != 2 {
		t.Fatalf("files=%d symbols=%d", len(files), len(symbols))
	}
}
