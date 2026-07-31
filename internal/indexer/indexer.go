package indexer

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"codecodriver/internal/domain"
)

type Indexer struct{}

func New() *Indexer { return &Indexer{} }

type pattern struct {
	kind string
	re   *regexp.Regexp
}

var symbolPatterns = map[string][]pattern{
	"go": {
		{"type", regexp.MustCompile(`^type\s+([A-Za-z_]\w*)`)},
		{"function", regexp.MustCompile(`^func\s+(?:\([^)]*\)\s*)?([A-Za-z_]\w*)`)},
	},
	"python": {
		{"class", regexp.MustCompile(`^class\s+([A-Za-z_]\w*)`)},
		{"function", regexp.MustCompile(`^def\s+([A-Za-z_]\w*)`)},
	},
	"typescript": {
		{"class", regexp.MustCompile(`^(?:export\s+)?class\s+([A-Za-z_$][\w$]*)`)},
		{"function", regexp.MustCompile(`^(?:export\s+)?(?:async\s+)?function\s+([A-Za-z_$][\w$]*)`)},
	},
	"javascript": {
		{"class", regexp.MustCompile(`^(?:export\s+)?class\s+([A-Za-z_$][\w$]*)`)},
		{"function", regexp.MustCompile(`^(?:export\s+)?(?:async\s+)?function\s+([A-Za-z_$][\w$]*)`)},
	},
}

func (i *Indexer) Index(repo domain.Repository) (domain.Repository, []domain.RepositoryFile, []domain.Symbol, error) {
	files, symbols, counts := []domain.RepositoryFile{}, []domain.Symbol{}, map[string]int{}
	err := filepath.WalkDir(repo.Path, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if path != repo.Path && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > 2*1024*1024 {
			return nil
		}
		lang := language(path)
		if lang == "" && !isTextName(d.Name()) {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		h := sha256.New()
		content, readErr := io.ReadAll(io.TeeReader(io.LimitReader(f, 2*1024*1024), h))
		closeErr := f.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		rel, _ := filepath.Rel(repo.Path, path)
		rel = filepath.ToSlash(rel)
		files = append(files, domain.RepositoryFile{RepositoryID: repo.ID, Path: rel, Language: lang, Size: info.Size(), Hash: hex.EncodeToString(h.Sum(nil)), Summary: firstNonEmptyLine(content)})
		counts[lang]++
		symbols = append(symbols, extractSymbols(repo.ID, rel, lang, content)...)
		return nil
	})
	if err != nil {
		return repo, nil, nil, err
	}
	repo.FileCount, repo.IndexedAt, repo.PrimaryLanguage = len(files), time.Now().UTC(), dominant(counts)
	sort.Slice(files, func(a, b int) bool { return files[a].Path < files[b].Path })
	return repo, files, symbols, nil
}

func skipDir(n string) bool {
	switch n {
	case ".git", ".cache", "node_modules", "vendor", "dist", "build", ".idea", ".vscode":
		return true
	}
	return false
}
func language(p string) string {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx":
		return "javascript"
	case ".java":
		return "java"
	case ".rs":
		return "rust"
	case ".md":
		return "markdown"
	case ".yaml", ".yml":
		return "yaml"
	case ".json":
		return "json"
	}
	return ""
}
func isTextName(n string) bool {
	switch strings.ToLower(n) {
	case "dockerfile", "makefile", "go.mod", "go.sum", ".gitignore":
		return true
	}
	return false
}
func firstNonEmptyLine(b []byte) string {
	s := bufio.NewScanner(strings.NewReader(string(b)))
	for s.Scan() {
		v := strings.TrimSpace(s.Text())
		if v != "" {
			if len(v) > 160 {
				v = v[:160]
			}
			return v
		}
	}
	return ""
}
func extractSymbols(repo, path, lang string, b []byte) []domain.Symbol {
	patterns := symbolPatterns[lang]
	if len(patterns) == 0 {
		return nil
	}
	out := []domain.Symbol{}
	s := bufio.NewScanner(strings.NewReader(string(b)))
	line := 0
	for s.Scan() {
		line++
		text := strings.TrimSpace(s.Text())
		for _, p := range patterns {
			if m := p.re.FindStringSubmatch(text); len(m) > 1 {
				out = append(out, domain.Symbol{RepositoryID: repo, FilePath: path, Name: m[1], Kind: p.kind, Line: line})
				break
			}
		}
	}
	return out
}
func dominant(m map[string]int) string {
	best, n := "", 0
	for k, v := range m {
		if k != "" && v > n {
			best, n = k, v
		}
	}
	return best
}
