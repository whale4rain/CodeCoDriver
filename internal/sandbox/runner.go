package sandbox

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	DefaultMaxPatchBytes   = 256 * 1024
	DefaultMaxChangedFiles = 10
	DefaultMaxCopyBytes    = 100 * 1024 * 1024
	DefaultMaxOutputBytes  = 64 * 1024
	DefaultCommandTimeout  = 2 * time.Minute
)

type Config struct {
	MaxPatchBytes   int
	MaxChangedFiles int
	MaxCopyBytes    int64
	MaxOutputBytes  int
	CommandTimeout  time.Duration
}

type Report struct {
	Status         string   `json:"status"`
	PatchExtracted bool     `json:"patch_extracted"`
	Applied        bool     `json:"applied"`
	TestCommand    string   `json:"test_command,omitempty"`
	Passed         bool     `json:"passed"`
	ChangedFiles   []string `json:"changed_files,omitempty"`
	Output         string   `json:"output,omitempty"`
	Error          string   `json:"error,omitempty"`
}

type Runner struct{ config Config }

func New(config Config) *Runner {
	if config.MaxPatchBytes <= 0 {
		config.MaxPatchBytes = DefaultMaxPatchBytes
	}
	if config.MaxChangedFiles <= 0 {
		config.MaxChangedFiles = DefaultMaxChangedFiles
	}
	if config.MaxCopyBytes <= 0 {
		config.MaxCopyBytes = DefaultMaxCopyBytes
	}
	if config.MaxOutputBytes <= 0 {
		config.MaxOutputBytes = DefaultMaxOutputBytes
	}
	if config.CommandTimeout <= 0 {
		config.CommandTimeout = DefaultCommandTimeout
	}
	return &Runner{config: config}
}

func (r *Runner) ValidateAndTest(ctx context.Context, repositoryPath, proposal string) Report {
	diff, err := ExtractDiff(proposal)
	if err != nil {
		return Report{Status: "invalid_patch", Error: err.Error()}
	}
	if len(diff) > r.config.MaxPatchBytes {
		return Report{Status: "invalid_patch", PatchExtracted: true, Error: "patch exceeds size limit"}
	}
	files, err := validatePaths(diff, r.config.MaxChangedFiles)
	if err != nil {
		return Report{Status: "invalid_patch", PatchExtracted: true, Error: err.Error()}
	}
	workdir, err := os.MkdirTemp("", "codecodriver-sandbox-*")
	if err != nil {
		return Report{Status: "sandbox_error", PatchExtracted: true, ChangedFiles: files, Error: err.Error()}
	}
	defer os.RemoveAll(workdir)
	if err := copyRepository(repositoryPath, workdir, r.config.MaxCopyBytes); err != nil {
		return Report{Status: "sandbox_error", PatchExtracted: true, ChangedFiles: files, Error: err.Error()}
	}
	commandCtx, cancel := context.WithTimeout(ctx, r.config.CommandTimeout)
	defer cancel()
	patchFile, err := os.CreateTemp("", "codecodriver-*.diff")
	if err != nil {
		return Report{Status: "sandbox_error", PatchExtracted: true, ChangedFiles: files, Error: err.Error()}
	}
	patchPath := patchFile.Name()
	defer os.Remove(patchPath)
	if _, err := patchFile.WriteString(diff); err != nil {
		patchFile.Close()
		return Report{Status: "sandbox_error", PatchExtracted: true, ChangedFiles: files, Error: err.Error()}
	}
	if err := patchFile.Close(); err != nil {
		return Report{Status: "sandbox_error", PatchExtracted: true, ChangedFiles: files, Error: err.Error()}
	}
	if output, err := run(commandCtx, workdir, "git", "apply", "--check", "--whitespace=error-all", patchPath); err != nil {
		return Report{Status: "apply_failed", PatchExtracted: true, ChangedFiles: files, Output: limitOutput(output, r.config.MaxOutputBytes), Error: commandError(commandCtx, err)}
	}
	if output, err := run(commandCtx, workdir, "git", "apply", "--whitespace=error-all", patchPath); err != nil {
		return Report{Status: "apply_failed", PatchExtracted: true, ChangedFiles: files, Output: limitOutput(output, r.config.MaxOutputBytes), Error: commandError(commandCtx, err)}
	}
	report := Report{Status: "applied", PatchExtracted: true, Applied: true, ChangedFiles: files}
	if _, err := os.Stat(filepath.Join(workdir, "go.mod")); err != nil {
		report.Status, report.Output = "tests_skipped", "no supported test runner detected"
		return report
	}
	report.TestCommand = "go test ./..."
	output, testErr := runWithEnv(commandCtx, workdir, []string{"GOTELEMETRY=off"}, "go", "test", "./...")
	report.Output, report.Passed = limitOutput(output, r.config.MaxOutputBytes), testErr == nil
	if testErr != nil {
		report.Status, report.Error = "tests_failed", commandError(commandCtx, testErr)
	} else {
		report.Status = "passed"
	}
	return report
}

func ExtractDiff(proposal string) (string, error) {
	fence := strings.Repeat(string(rune(96)), 3)
	if start := strings.Index(proposal, fence+"diff"); start >= 0 {
		body := proposal[start+len(fence+"diff"):]
		if end := strings.Index(body, fence); end >= 0 {
			diff := strings.TrimSpace(body[:end])
			if strings.HasPrefix(diff, "--- ") {
				return diff + "\n", nil
			}
		}
	}
	start := strings.Index(proposal, "--- a/")
	if start < 0 {
		return "", fmt.Errorf("proposal does not contain a unified diff")
	}
	diff := strings.TrimSpace(proposal[start:])
	if diff == "" {
		return "", fmt.Errorf("proposal contains an empty diff")
	}
	return diff + "\n", nil
}

func validatePaths(diff string, maxFiles int) ([]string, error) {
	seen, files := map[string]bool{}, []string{}
	for _, line := range strings.Split(diff, "\n") {
		if !strings.HasPrefix(line, "+++ ") {
			continue
		}
		parts := strings.Fields(strings.TrimPrefix(line, "+++ "))
		if len(parts) == 0 || parts[0] == "/dev/null" {
			continue
		}
		clean, err := safePatchPath(parts[0])
		if err != nil {
			return nil, err
		}
		if sensitivePath(clean) {
			return nil, fmt.Errorf("patch targets sensitive file %q", clean)
		}
		if !seen[clean] {
			seen[clean] = true
			files = append(files, clean)
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("patch contains no target files")
	}
	if len(files) > maxFiles {
		return nil, fmt.Errorf("patch changes %d files; limit is %d", len(files), maxFiles)
	}
	return files, nil
}

func safePatchPath(path string) (string, error) {
	path = strings.TrimPrefix(strings.TrimPrefix(path, "a/"), "b/")
	path = filepath.ToSlash(path)
	if path == "" || filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, "../") || strings.Contains(path, "/../") {
		return "", fmt.Errorf("unsafe patch path %q", path)
	}
	return path, nil
}

func sensitivePath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return true
	}
	switch strings.ToLower(filepath.Ext(base)) {
	case ".pem", ".key", ".p12", ".pfx", ".jks":
		return true
	}
	for _, marker := range []string{"credential", "credentials", "secret", "secrets"} {
		if strings.Contains(base, marker) {
			return true
		}
	}
	return false
}

func copyRepository(source, destination string, maxBytes int64) error {
	var copied int64
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".cache", "node_modules":
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(destination, rel), 0o755)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		copied += info.Size()
		if copied > maxBytes {
			return fmt.Errorf("repository copy exceeds %d bytes", maxBytes)
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		dst, err := os.OpenFile(filepath.Join(destination, rel), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			src.Close()
			return err
		}
		_, copyErr := io.Copy(dst, src)
		srcCloseErr := src.Close()
		closeErr := dst.Close()
		if copyErr != nil {
			return copyErr
		}
		if srcCloseErr != nil {
			return srcCloseErr
		}
		return closeErr
	})
}

func run(ctx context.Context, dir, name string, args ...string) (string, error) {
	return runWithEnv(ctx, dir, nil, name, args...)
}

func runWithEnv(ctx context.Context, dir string, extraEnv []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Env = dir, append(os.Environ(), extraEnv...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func limitOutput(output string, maxBytes int) string {
	if len(output) <= maxBytes {
		return output
	}
	return output[:maxBytes] + "\n[OUTPUT TRUNCATED]"
}

func commandError(ctx context.Context, err error) string {
	if ctx.Err() != nil {
		return ctx.Err().Error()
	}
	return err.Error()
}
