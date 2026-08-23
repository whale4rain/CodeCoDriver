package retrieval

import (
	"bufio"
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"codecodriver/internal/domain"
)

const (
	DefaultMaxFiles      = 5
	DefaultMaxFileBytes  = 12 * 1024
	DefaultMaxTotalBytes = 32 * 1024
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

// FileReader is the narrow file surface used to build context packs. The
// production implementation reads from the isolated Docker workspace so the
// host repository is never accessed by agent context retrieval.
type FileReader interface {
	ReadFile(context.Context, string, int, int) (map[string]any, error)
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

func (b *Builder) Build(ctx context.Context, files []domain.RepositoryFile, reader FileReader) ContextPack {
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
		content, truncated, err := readFileThroughReader(ctx, reader, file.Path, limit)
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

func readFileThroughReader(ctx context.Context, reader FileReader, path string, limit int) ([]byte, bool, error) {
	if limit <= 0 {
		return nil, false, fmt.Errorf("context budget exhausted")
	}
	if reader == nil {
		return nil, false, fmt.Errorf("workspace file reader unavailable")
	}
	result, err := reader.ReadFile(ctx, path, 1, 0)
	if err != nil {
		return nil, false, fmt.Errorf("read file: %w", err)
	}
	raw, ok := result["content"].(string)
	if !ok {
		return nil, false, fmt.Errorf("read file: missing content")
	}
	content := []byte(strings.ReplaceAll(raw, "\r\n", "\n"))
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
