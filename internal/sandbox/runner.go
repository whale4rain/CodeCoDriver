package sandbox

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	TestCommand     string
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
	diff = normalizeDiff(diff)
	diff = trimTrailingAddedBlanks(diff)
	diff = repairHunkContext(diff, repositoryPath)
	if len(diff) > r.config.MaxPatchBytes {
		return Report{Status: "invalid_patch", PatchExtracted: true, Error: "patch exceeds size limit"}
	}
	files, err := validatePaths(diff, r.config.MaxChangedFiles)
	if err != nil {
		return Report{Status: "invalid_patch", PatchExtracted: true, Error: err.Error()}
	}
	if err := validateFileStates(diff, repositoryPath); err != nil {
		return Report{Status: "invalid_patch", PatchExtracted: true, ChangedFiles: files, Error: err.Error()}
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
	if output, err := run(commandCtx, workdir, "git", "apply", "--check", "--recount", "--whitespace=error-all", patchPath); err != nil {
		return Report{Status: "apply_failed", PatchExtracted: true, ChangedFiles: files, Output: limitOutput(output, r.config.MaxOutputBytes), Error: commandError(commandCtx, err)}
	}
	if output, err := run(commandCtx, workdir, "git", "apply", "--recount", "--whitespace=error-all", patchPath); err != nil {
		return Report{Status: "apply_failed", PatchExtracted: true, ChangedFiles: files, Output: limitOutput(output, r.config.MaxOutputBytes), Error: commandError(commandCtx, err)}
	}
	report := Report{Status: "applied", PatchExtracted: true, Applied: true, ChangedFiles: files}
	if _, err := os.Stat(filepath.Join(workdir, "go.mod")); err != nil {
		report.Status, report.Output = "tests_skipped", "no supported test runner detected"
		return report
	}
	testCommand := r.config.TestCommand
	if testCommand == "" {
		testCommand = "go test ./..."
	}
	report.TestCommand = testCommand
	command, args := splitCommand(testCommand)
	output, testErr := runWithEnv(commandCtx, workdir, []string{"GOTELEMETRY=off"}, command, args...)
	report.Output, report.Passed = limitOutput(output, r.config.MaxOutputBytes), testErr == nil
	if testErr != nil {
		report.Status, report.Error = "tests_failed", commandError(commandCtx, testErr)
	} else {
		report.Status = "passed"
	}
	return report
}

// ApplyToRepository validates and applies a proposal to the original repository.
// It only touches the changed files and records a commit when the repository is a git work tree.
func (r *Runner) ApplyToRepository(ctx context.Context, repositoryPath, proposal, commitMessage string) (Report, error) {
	diff, err := ExtractDiff(proposal)
	if err != nil {
		return Report{Status: "invalid_patch", Error: err.Error()}, nil
	}
	diff = normalizeDiff(diff)
	diff = trimTrailingAddedBlanks(diff)
	diff = repairHunkContext(diff, repositoryPath)
	if len(diff) > r.config.MaxPatchBytes {
		return Report{Status: "invalid_patch", PatchExtracted: true, Error: "patch exceeds size limit"}, nil
	}
	files, err := validatePaths(diff, r.config.MaxChangedFiles)
	if err != nil {
		return Report{Status: "invalid_patch", PatchExtracted: true, Error: err.Error()}, nil
	}
	if err := validateFileStates(diff, repositoryPath); err != nil {
		return Report{Status: "invalid_patch", PatchExtracted: true, ChangedFiles: files, Error: err.Error()}, nil
	}
	commandCtx, cancel := context.WithTimeout(ctx, r.config.CommandTimeout)
	defer cancel()
	patchFile, err := os.CreateTemp("", "codecodriver-apply-*.diff")
	if err != nil {
		return Report{Status: "sandbox_error", PatchExtracted: true, ChangedFiles: files, Error: err.Error()}, nil
	}
	patchPath := patchFile.Name()
	defer os.Remove(patchPath)
	if _, err := patchFile.WriteString(diff); err != nil {
		patchFile.Close()
		return Report{Status: "sandbox_error", PatchExtracted: true, ChangedFiles: files, Error: err.Error()}, nil
	}
	if err := patchFile.Close(); err != nil {
		return Report{Status: "sandbox_error", PatchExtracted: true, ChangedFiles: files, Error: err.Error()}, nil
	}
	if output, err := run(commandCtx, repositoryPath, "git", "apply", "--check", "--recount", "--whitespace=error-all", patchPath); err != nil {
		return Report{Status: "apply_failed", PatchExtracted: true, ChangedFiles: files, Output: limitOutput(output, r.config.MaxOutputBytes), Error: commandError(commandCtx, err)}, nil
	}
	if output, err := run(commandCtx, repositoryPath, "git", "apply", "--recount", "--whitespace=error-all", patchPath); err != nil {
		return Report{Status: "apply_failed", PatchExtracted: true, ChangedFiles: files, Output: limitOutput(output, r.config.MaxOutputBytes), Error: commandError(commandCtx, err)}, nil
	}
	if _, err := run(commandCtx, repositoryPath, "git", "rev-parse", "--is-inside-work-tree"); err == nil {
		addArgs := append([]string{"add", "--"}, files...)
		if _, addErr := run(commandCtx, repositoryPath, "git", addArgs...); addErr == nil {
			if _, commitErr := run(commandCtx, repositoryPath, "git", "commit", "-m", commitMessage); commitErr != nil {
				return Report{Status: "applied", PatchExtracted: true, Applied: true, ChangedFiles: files, Output: "patch applied; commit skipped because no changes were staged"}, nil
			}
		}
	}
	return Report{Status: "applied", PatchExtracted: true, Applied: true, ChangedFiles: files}, nil
}

func splitCommand(command string) (string, []string) {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return "go", []string{"test", "./..."}
	}
	return parts[0], parts[1:]
}

func ExtractDiff(proposal string) (string, error) {
	fence := strings.Repeat(string(rune(96)), 3)
	if start := strings.Index(proposal, fence+"diff"); start >= 0 {
		body := proposal[start+len(fence+"diff"):]
		if end := strings.Index(body, fence); end >= 0 {
			diff := strings.TrimSpace(body[:end])
			if strings.HasPrefix(diff, "--- ") || strings.HasPrefix(diff, "diff --git ") {
				return diff + "\n", nil
			}
		}
	}
	start := firstDiffHeader(proposal)
	if start < 0 {
		return "", fmt.Errorf("proposal does not contain a unified diff")
	}
	diff := strings.TrimSpace(proposal[start:])
	if end := strings.Index(diff, "\n```"); end >= 0 {
		diff = strings.TrimSpace(diff[:end])
	}
	if diff == "" {
		return "", fmt.Errorf("proposal contains an empty diff")
	}
	return diff + "\n", nil
}

func firstDiffHeader(proposal string) int {
	start := -1
	for _, marker := range []string{"--- a/", "--- /dev/null"} {
		if index := strings.Index(proposal, marker); index >= 0 && (start < 0 || index < start) {
			start = index
		}
	}
	return start
}

func normalizeDiff(diff string) string {
	lines := strings.Split(diff, "\n")
	out := make([]string, 0, len(lines)+4)
	hasGitHeader := false
	hasNewFileMode := false
	for i, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			hasGitHeader = true
			hasNewFileMode = false
			out = append(out, line)
			continue
		}
		if strings.HasPrefix(line, "--- ") {
			if !hasGitHeader {
				out = append(out, "diff --git "+diffHeaderTarget(lines, i))
			}
			if strings.HasPrefix(line, "--- /dev/null") && !hasNewFileMode {
				out = append(out, "new file mode 100644")
				hasNewFileMode = true
			}
			hasGitHeader = false
			out = append(out, line)
			continue
		}
		if strings.HasPrefix(line, "new file mode ") {
			hasNewFileMode = true
			out = append(out, line)
			continue
		}
		if strings.HasPrefix(line, "+++ ") {
			hasGitHeader = false
			out = append(out, line)
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func trimTrailingAddedBlanks(diff string) string {
	lines := strings.Split(diff, "\n")
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	for end > 0 && strings.TrimSpace(lines[end-1]) == "+" {
		end--
	}
	if end == len(lines) {
		return diff
	}
	return strings.Join(lines[:end], "\n") + "\n"
}

func diffHeaderTarget(lines []string, index int) string {
	parts := strings.Fields(strings.TrimPrefix(lines[index], "--- "))
	if len(parts) == 0 {
		return "a/file b/file"
	}
	from := strings.TrimPrefix(parts[0], "a/")
	if from != "/dev/null" {
		return "a/" + from + " b/" + from
	}
	for i := index + 1; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "+++ ") {
			continue
		}
		toParts := strings.Fields(strings.TrimPrefix(lines[i], "+++ "))
		if len(toParts) == 0 {
			continue
		}
		to := strings.TrimPrefix(toParts[0], "b/")
		if to != "/dev/null" {
			return "a/" + to + " b/" + to
		}
	}
	return "a/file b/file"
}

func repairHunkContext(diff, repositoryPath string) string {
	lines := strings.Split(diff, "\n")
	out := make([]string, 0, len(lines)+4)
	currentPath := ""
	inHunk := false
	oldPos := 0
	hasContextAfterChange := false
	flushHunk := func() {
		if inHunk && !hasContextAfterChange {
			if ok, line := nextSourceLine(repositoryPath, currentPath, oldPos); ok {
				out = append(out, " "+line)
			}
		}
		inHunk = false
	}
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flushHunk()
			currentPath = patchPathFromLine(line, "diff --git ")
			out = append(out, line)
		case strings.HasPrefix(line, "--- "):
			flushHunk()
			currentPath = patchPathFromLine(line, "--- ")
			out = append(out, line)
		case strings.HasPrefix(line, "+++ "):
			if currentPath == "" {
				currentPath = patchPathFromLine(line, "+++ ")
			}
			out = append(out, line)
		case strings.HasPrefix(line, "@@ "):
			flushHunk()
			inHunk = true
			oldPos = hunkOldStart(line)
			hasContextAfterChange = false
			out = append(out, line)
		default:
			if inHunk {
				if cleaned, ok := stripNumberedDiffLine(line); ok {
					line = cleaned
				}
				switch {
				case strings.HasPrefix(line, " "):
					oldPos++
					hasContextAfterChange = true
				case strings.HasPrefix(line, "-"):
					oldPos++
					hasContextAfterChange = false
				case strings.HasPrefix(line, "+"):
					hasContextAfterChange = false
				default:
					flushHunk()
				}
			}
			out = append(out, line)
		}
	}
	flushHunk()
	return strings.Join(out, "\n")
}

func stripNumberedDiffLine(line string) (string, bool) {
	trimmed := strings.TrimLeft(line, " ")
	index := 0
	for index < len(trimmed) && trimmed[index] >= '0' && trimmed[index] <= '9' {
		index++
	}
	for index < len(trimmed) && trimmed[index] == ' ' {
		index++
	}
	if index == 0 || index >= len(trimmed) || trimmed[index] != '|' {
		return line, false
	}
	rest := strings.TrimLeft(trimmed[index+1:], " ")
	if rest == "" {
		return line, false
	}
	return " " + rest, true
}

func patchPathFromLine(line, prefix string) string {
	parts := strings.Fields(strings.TrimPrefix(line, prefix))
	if len(parts) == 0 {
		return ""
	}
	path := strings.TrimPrefix(strings.TrimPrefix(parts[0], "a/"), "b/")
	if path == "/dev/null" {
		return ""
	}
	return path
}

func hunkOldStart(line string) int {
	start := strings.Index(line, "-")
	if start < 0 {
		return 1
	}
	rest := strings.TrimPrefix(line[start:], "-")
	number := strings.TrimSpace(strings.SplitN(rest, ",", 2)[0])
	value, err := strconv.Atoi(number)
	if err != nil || value <= 0 {
		return 1
	}
	return value
}

func nextSourceLine(root, path string, line int) (bool, string) {
	if path == "" || line <= 0 {
		return false, ""
	}
	content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		return false, ""
	}
	normalized := strings.ReplaceAll(string(content), "\r\n", "\n")
	normalized = strings.TrimSuffix(normalized, "\n")
	lines := strings.Split(normalized, "\n")
	if line > len(lines) {
		return false, ""
	}
	return true, strings.TrimSuffix(lines[line-1], "\r")
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

func validateFileStates(diff, repositoryPath string) error {
	newFile := false
	seen := make(map[string]bool)
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			newFile = false
			continue
		}
		if strings.HasPrefix(line, "--- ") {
			from := strings.TrimSpace(strings.TrimPrefix(line, "--- "))
			from = strings.TrimPrefix(strings.TrimPrefix(from, "a/"), "b/")
			newFile = from == "/dev/null"
			continue
		}
		if !strings.HasPrefix(line, "+++ ") {
			continue
		}
		to := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
		if to == "/dev/null" {
			continue
		}
		clean, err := safePatchPath(to)
		if err != nil {
			return err
		}
		if seen[clean] {
			continue
		}
		seen[clean] = true
		exists := fileExists(filepath.Join(repositoryPath, filepath.FromSlash(clean)))
		if newFile && exists {
			return fmt.Errorf("patch creates already-existing file %q", clean)
		}
		if !newFile && !exists {
			return fmt.Errorf("patch modifies missing file %q", clean)
		}
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
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
