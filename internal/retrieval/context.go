package retrieval

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"codecodriver/internal/domain"
)

const (
	DefaultMaxFiles      = 8
	DefaultMaxFileBytes  = 12 * 1024
	DefaultMaxTotalBytes = 48 * 1024
)

type Config struct {
	MaxFiles      int
	MaxFileBytes  int
	MaxTotalBytes int
}

type SourceSnippet struct {
	Path      string `json:"path"`
	Language  string `json:"language,omitempty"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
}

type ContextPack struct {
	Snippets    []SourceSnippet `json:"snippets"`
	Skipped     []SkippedFile   `json:"skipped,omitempty"`
	TotalBytes  int             `json:"total_bytes"`
	BudgetBytes int             `json:"budget_bytes"`
}

type SkippedFile struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type Builder struct{ config Config }

func New(config Config) *Builder {
	if config.MaxFiles <= 0 {
		config.MaxFiles = DefaultMaxFiles
	}
	if config.MaxFileBytes <= 0 {
		config.MaxFileBytes = DefaultMaxFileBytes
	}
	if config.MaxTotalBytes <= 0 {
		config.MaxTotalBytes = DefaultMaxTotalBytes
	}
	return &Builder{config: config}
}

func (b *Builder) Build(repo domain.Repository, files []domain.RepositoryFile) ContextPack {
	pack := ContextPack{Snippets: []SourceSnippet{}, Skipped: []SkippedFile{}, BudgetBytes: b.config.MaxTotalBytes}
	for _, file := range files {
		if len(pack.Snippets) >= b.config.MaxFiles || pack.TotalBytes >= b.config.MaxTotalBytes {
			break
		}
		if reason := sensitiveReason(file.Path); reason != "" {
			pack.Skipped = append(pack.Skipped, SkippedFile{Path: file.Path, Reason: reason})
			continue
		}
		remaining := b.config.MaxTotalBytes - pack.TotalBytes
		limit := min(b.config.MaxFileBytes, remaining)
		content, truncated, err := readRepositoryFile(repo.Path, file.Path, limit)
		if err != nil {
			pack.Skipped = append(pack.Skipped, SkippedFile{Path: file.Path, Reason: err.Error()})
			continue
		}
		numbered := addLineNumbers(content)
		pack.Snippets = append(pack.Snippets, SourceSnippet{Path: file.Path, Language: file.Language, Content: numbered, Truncated: truncated})
		pack.TotalBytes += len(content)
	}
	return pack
}

func Render(pack ContextPack) string {
	var out strings.Builder
	for _, snippet := range pack.Snippets {
		fmt.Fprintf(&out, "===== FILE: %s (%s) =====\n", snippet.Path, snippet.Language)
		out.WriteString(snippet.Content)
		if snippet.Truncated {
			out.WriteString("\n[TRUNCATED BY CONTEXT BUDGET]")
		}
		out.WriteString("\n\n")
	}
	if len(pack.Skipped) > 0 {
		out.WriteString("===== SKIPPED =====\n")
		for _, skipped := range pack.Skipped {
			fmt.Fprintf(&out, "%s: %s\n", skipped.Path, skipped.Reason)
		}
	}
	return strings.TrimSpace(out.String())
}

func readRepositoryFile(root, relative string, limit int) ([]byte, bool, error) {
	if limit <= 0 {
		return nil, false, fmt.Errorf("context budget exhausted")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, false, fmt.Errorf("resolve repository root")
	}
	rootResolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return nil, false, fmt.Errorf("resolve repository root")
	}
	candidate := filepath.Join(rootResolved, filepath.FromSlash(relative))
	candidateResolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return nil, false, fmt.Errorf("resolve file path")
	}
	rel, err := filepath.Rel(rootResolved, candidateResolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return nil, false, fmt.Errorf("path escapes repository root")
	}
	info, err := os.Stat(candidateResolved)
	if err != nil {
		return nil, false, fmt.Errorf("stat file")
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("not a regular file")
	}
	f, err := os.Open(candidateResolved)
	if err != nil {
		return nil, false, fmt.Errorf("open file")
	}
	defer f.Close()
	content, err := io.ReadAll(io.LimitReader(f, int64(limit+1)))
	if err != nil {
		return nil, false, fmt.Errorf("read file")
	}
	truncated := len(content) > limit
	if truncated {
		content = content[:limit]
	}
	return content, truncated, nil
}

func sensitiveReason(path string) string {
	base := strings.ToLower(filepath.Base(path))
	ext := strings.ToLower(filepath.Ext(base))
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return "sensitive environment file"
	}
	switch ext {
	case ".pem", ".key", ".p12", ".pfx", ".jks":
		return "private key or certificate file"
	}
	for _, marker := range []string{"credential", "credentials", "secret", "secrets"} {
		if strings.Contains(base, marker) {
			return "sensitive filename"
		}
	}
	return ""
}

func addLineNumbers(content []byte) string {
	var out strings.Builder
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	line := 1
	for scanner.Scan() {
		fmt.Fprintf(&out, "%4d | %s\n", line, scanner.Text())
		line++
	}
	return strings.TrimSuffix(out.String(), "\n")
}
