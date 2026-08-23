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
	Image           string
	DockerBin       string
	MemoryLimit     string
	CPULimit        string
	PidsLimit       int
	Network         string
	GoProxy         string
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

// Validator validates a patch without mutating the original repository.
type Validator interface {
	ValidateAndTest(context.Context, string, string) Report
}

type Runner struct{ config Config }

func New(config Config) *Runner {
	config = normalizeConfig(config)
	return &Runner{config: config}
}

// WithTestCommand returns a runner with the same driver configuration but a
// different repository test command.
func (r *Runner) WithTestCommand(command string) Validator {
	config := r.config
	config.TestCommand = command
	return New(config)
}

func normalizeConfig(config Config) Config {
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
	return config
}

func (r *Runner) ValidateAndTest(ctx context.Context, repositoryPath, proposal string) Report {
	diff, files, report := prepareValidation(r.config, repositoryPath, proposal)
	if report != nil {
		return *report
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
	if output, err := run(commandCtx, workdir, "git", "apply", "--check", "--recount", "--ignore-space-change", "--whitespace=error-all", patchPath); err != nil {
		return Report{Status: "apply_failed", PatchExtracted: true, ChangedFiles: files, Output: limitOutput(output, r.config.MaxOutputBytes), Error: commandError(commandCtx, err)}
	}
	if output, err := run(commandCtx, workdir, "git", "apply", "--recount", "--ignore-space-change", "--whitespace=error-all", patchPath); err != nil {
		return Report{Status: "apply_failed", PatchExtracted: true, ChangedFiles: files, Output: limitOutput(output, r.config.MaxOutputBytes), Error: commandError(commandCtx, err)}
	}
	report = &Report{Status: "applied", PatchExtracted: true, Applied: true, ChangedFiles: files}
	if _, err := os.Stat(filepath.Join(workdir, "go.mod")); err != nil {
		report.Status, report.Output = "tests_skipped", "no supported test runner detected"
		return *report
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
	return *report
}

func prepareValidation(config Config, repositoryPath, proposal string) (string, []string, *Report) {
	if _, err := PreflightDiff(proposal); err != nil {
		return "", nil, &Report{Status: "invalid_patch", Error: err.Error()}
	}
	diff, err := ExtractDiff(proposal)
	if err != nil {
		return "", nil, &Report{Status: "invalid_patch", Error: err.Error()}
	}
	diff = normalizeDiff(diff)
	diff = trimTrailingAddedBlanks(diff)
	diff = repairHunkContext(diff, repositoryPath)
	diff = repairHunkPositions(diff, repositoryPath)
	diff = normalizePatchLineEndings(diff)
	if len(diff) > config.MaxPatchBytes {
		return "", nil, &Report{Status: "invalid_patch", PatchExtracted: true, Error: "patch exceeds size limit"}
	}
	files, err := validatePaths(diff, config.MaxChangedFiles)
	if err != nil {
		return "", nil, &Report{Status: "invalid_patch", PatchExtracted: true, Error: err.Error()}
	}
	if err := validateFileStates(diff, repositoryPath); err != nil {
		return "", nil, &Report{Status: "invalid_patch", PatchExtracted: true, ChangedFiles: files, Error: err.Error()}
	}
	return diff, files, nil
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
	if len(diff) > r.config.MaxPatchBytes {
		return Report{Status: "invalid_patch", PatchExtracted: true, Error: "patch exceeds size limit"}, nil
	}
	commandCtx, cancel := context.WithTimeout(ctx, r.config.CommandTimeout)
	defer cancel()
	pendingFiles := []string{}
	pendingPatches := []string{}
	pendingNew := []bool{}
	pendingHunks := [][]string{}
	pendingHeaders := []string{}
	changedFiles := []string{}
	for _, chunk := range splitDiffFiles(diff) {
		files, err := validatePaths(chunk, r.config.MaxChangedFiles)
		if err != nil {
			return Report{Status: "invalid_patch", PatchExtracted: true, Error: err.Error()}, nil
		}
		path, newFile, _ := diffFileState(chunk)
		if newFile {
			if fileExists(filepath.Join(repositoryPath, filepath.FromSlash(path))) {
				if newFileAlreadyApplied(repositoryPath, chunk) {
					continue
				}
				return Report{Status: "invalid_patch", PatchExtracted: true, ChangedFiles: files, Error: fmt.Sprintf("patch creates already-existing file %q with different content", path)}, nil
			}
			pendingFiles = append(pendingFiles, files...)
			pendingPatches = append(pendingPatches, chunk)
			pendingNew = append(pendingNew, true)
			pendingHunks = append(pendingHunks, nil)
			pendingHeaders = append(pendingHeaders, "")
			continue
		}
		headers, hunks := splitDiffHunks(chunk)
		applyHunks := []string{}
		for _, hunk := range hunks {
			subpatch := headers + hunk
			if existingFileAlreadyApplied(commandCtx, repositoryPath, subpatch) {
				continue
			}
			applyHunks = append(applyHunks, hunk)
		}
		if len(applyHunks) > 0 {
			pendingFiles = append(pendingFiles, files...)
			pendingPatches = append(pendingPatches, headers+strings.Join(applyHunks, "\n"))
			pendingNew = append(pendingNew, false)
			pendingHunks = append(pendingHunks, applyHunks)
			pendingHeaders = append(pendingHeaders, headers)
		}
	}
	if len(pendingFiles) == 0 {
		return Report{Status: "already_applied", PatchExtracted: true, Applied: true, ChangedFiles: changedFiles}, nil
	}
	warnings := []string{}
	for i := range pendingPatches {
		output, applyErr := applyPatchToRepo(commandCtx, repositoryPath, pendingPatches[i], r.config.MaxOutputBytes)
		if applyErr == nil {
			changedFiles = append(changedFiles, pendingFiles[i])
			continue
		}
		if pendingNew[i] {
			return Report{Status: "apply_failed", PatchExtracted: true, ChangedFiles: changedFiles, Output: limitOutput(output, r.config.MaxOutputBytes), Error: commandError(commandCtx, applyErr)}, nil
		}
		appliedAny := false
		for _, hunk := range pendingHunks[i] {
			if _, hunkErr := applyPatchToRepo(commandCtx, repositoryPath, pendingHeaders[i]+hunk, r.config.MaxOutputBytes); hunkErr == nil {
				appliedAny = true
			} else {
				warnings = append(warnings, fmt.Sprintf("%s hunk skipped: %s", pendingFiles[i], firstLine(output)))
			}
		}
		if appliedAny {
			changedFiles = append(changedFiles, pendingFiles[i])
		}
	}
	if len(changedFiles) == 0 {
		return Report{Status: "already_applied", PatchExtracted: true, Applied: true, ChangedFiles: changedFiles, Output: strings.Join(warnings, "\n")}, nil
	}
	if _, err := run(commandCtx, repositoryPath, "git", "rev-parse", "--is-inside-work-tree"); err == nil {
		addArgs := append([]string{"add", "--"}, changedFiles...)
		if _, addErr := run(commandCtx, repositoryPath, "git", addArgs...); addErr == nil {
			if _, commitErr := run(commandCtx, repositoryPath, "git", "commit", "-m", commitMessage); commitErr != nil {
				return Report{Status: "applied_with_warnings", PatchExtracted: true, Applied: true, ChangedFiles: changedFiles, Output: "patch applied; commit skipped because no changes were staged\n" + strings.Join(warnings, "\n")}, nil
			}
		}
	}
	status := "applied"
	output := ""
	if len(warnings) > 0 {
		status = "applied_with_warnings"
		output = strings.Join(warnings, "\n")
	}
	return Report{Status: status, PatchExtracted: true, Applied: true, ChangedFiles: changedFiles, Output: output}, nil
}

func applyPatchToRepo(ctx context.Context, repositoryPath, patch string, maxOutput int) (string, error) {
	patchFile, err := os.CreateTemp("", "codecodriver-apply-*.diff")
	if err != nil {
		return "", err
	}
	patchPath := patchFile.Name()
	defer os.Remove(patchPath)
	if _, err := patchFile.WriteString(strings.TrimRight(patch, "\n") + "\n"); err != nil {
		patchFile.Close()
		return "", err
	}
	if err := patchFile.Close(); err != nil {
		return "", err
	}
	if output, err := run(ctx, repositoryPath, "git", "apply", "--check", "--recount", "--ignore-space-change", "--whitespace=error-all", patchPath); err != nil {
		return limitOutput(output, maxOutput), err
	}
	if output, err := run(ctx, repositoryPath, "git", "apply", "--recount", "--ignore-space-change", "--whitespace=error-all", patchPath); err != nil {
		return limitOutput(output, maxOutput), err
	}
	return "", nil
}

func firstLine(value string) string {
	for _, line := range strings.Split(value, "\n") {
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return value
}

func splitDiffFiles(diff string) []string {
	diff = strings.ReplaceAll(diff, "\r\n", "\n")
	parts := strings.Split(diff, "\ndiff --git ")
	chunks := []string{}
	for i, part := range parts {
		if i > 0 {
			part = "diff --git " + part
		}
		if strings.TrimSpace(part) == "" {
			continue
		}
		chunks = append(chunks, strings.TrimSpace(part)+"\n")
	}
	if len(chunks) == 0 && strings.TrimSpace(diff) != "" {
		return []string{diff}
	}
	return chunks
}

func splitDiffHunks(chunk string) (string, []string) {
	lines := strings.Split(strings.ReplaceAll(chunk, "\r\n", "\n"), "\n")
	headerLines := []string{}
	hunks := []string{}
	var current strings.Builder
	inHunk := false
	for _, line := range lines {
		if strings.HasPrefix(line, "@@ ") {
			if inHunk {
				hunks = append(hunks, strings.TrimSuffix(current.String(), "\n"))
			}
			current.Reset()
			current.WriteString(line)
			current.WriteString("\n")
			inHunk = true
			continue
		}
		if !inHunk {
			if strings.TrimSpace(line) != "" {
				headerLines = append(headerLines, line)
			}
			continue
		}
		current.WriteString(line)
		current.WriteString("\n")
	}
	if inHunk {
		hunks = append(hunks, strings.TrimSuffix(current.String(), "\n"))
	}
	return strings.Join(headerLines, "\n") + "\n", hunks
}

func diffFileState(chunk string) (path string, newFile bool, addedLines []string) {
	for _, line := range strings.Split(chunk, "\n") {
		if strings.HasPrefix(line, "--- ") {
			from := strings.TrimSpace(strings.TrimPrefix(line, "--- "))
			newFile = from == "/dev/null"
			continue
		}
		if strings.HasPrefix(line, "+++ ") {
			to := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			to = strings.TrimPrefix(to, "b/")
			if to != "/dev/null" {
				path = to
			}
			continue
		}
		if newFile && strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++ ") {
			addedLines = append(addedLines, strings.TrimPrefix(line, "+"))
		}
	}
	return path, newFile, addedLines
}

func newFileAlreadyApplied(repositoryPath, chunk string) bool {
	path, _, addedLines := diffFileState(chunk)
	if path == "" || len(addedLines) == 0 {
		return false
	}
	content, err := os.ReadFile(filepath.Join(repositoryPath, filepath.FromSlash(path)))
	if err != nil {
		return false
	}
	normalized := strings.TrimRight(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n") + "\n"
	expected := strings.TrimRight(strings.Join(addedLines, "\n"), "\n") + "\n"
	return normalized == expected
}

func existingFileAlreadyApplied(ctx context.Context, repositoryPath, chunk string) bool {
	patchFile, err := os.CreateTemp("", "codecodriver-reverse-*.diff")
	if err != nil {
		return false
	}
	patchPath := patchFile.Name()
	defer os.Remove(patchPath)
	if _, err := patchFile.WriteString(chunk + "\n"); err != nil {
		patchFile.Close()
		return false
	}
	if err := patchFile.Close(); err != nil {
		return false
	}
	_, err = run(ctx, repositoryPath, "git", "apply", "--reverse", "--check", "--recount", "--whitespace=error-all", patchPath)
	if err == nil {
		return true
	}
	return hunkAddedLinesPresent(repositoryPath, chunk)
}

func hunkAddedLinesPresent(repositoryPath, chunk string) bool {
	path, _, _ := diffFileState(chunk)
	if path == "" {
		return false
	}
	content, err := os.ReadFile(filepath.Join(repositoryPath, filepath.FromSlash(path)))
	if err != nil {
		return false
	}
	normalized := strings.ReplaceAll(string(content), "\r\n", "\n")
	added := []string{}
	for _, line := range strings.Split(chunk, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++ ") {
			added = append(added, strings.TrimPrefix(line, "+"))
		}
	}
	if len(added) == 0 {
		return false
	}
	for _, line := range added {
		if strings.TrimSpace(line) != "" && !strings.Contains(normalized, line) {
			return false
		}
	}
	return true
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
		if end := strings.LastIndex(body, fence); end >= 0 {
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

// PreflightDiff performs cheap structural validation before expensive sandbox work.
// It catches missing file headers, missing hunks, and repeated hunk-header loops.
func PreflightDiff(proposal string) (string, error) {
	diff, err := ExtractDiff(proposal)
	if err != nil {
		return "", err
	}
	diff = normalizeDiff(diff)
	chunks := splitDiffFiles(diff)
	if len(chunks) == 0 {
		return "", fmt.Errorf("patch has no file sections")
	}
	seenHunks := map[string]int{}
	for _, chunk := range chunks {
		headers, hunks := splitDiffHunks(chunk)
		path, newFile, _ := diffFileState(chunk)
		if path == "" {
			return "", fmt.Errorf("patch file section is missing +++ target path")
		}
		if !newFile && !strings.Contains(headers, "--- a/") {
			return "", fmt.Errorf("patch for %s is missing --- a/ header", path)
		}
		if len(hunks) == 0 {
			return "", fmt.Errorf("patch for %s has no hunks", path)
		}
		for _, hunk := range hunks {
			header := strings.TrimSpace(strings.SplitN(hunk, "\n", 2)[0])
			key := path + "|" + header
			seenHunks[key]++
			if seenHunks[key] > 5 {
				return "", fmt.Errorf("patch for %s repeats hunk header %s too many times", path, header)
			}
		}
	}
	return diff, nil
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

// normalizePatchLineEndings converts CRLF to LF so git apply can validate
// patches against CRLF repositories without treating CR as trailing whitespace.
func normalizePatchLineEndings(diff string) string {
	diff = strings.ReplaceAll(diff, "\r\n", "\n")
	return strings.ReplaceAll(diff, "\r", "")
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

func repairHunkPositions(diff, repositoryPath string) string {
	lines := strings.Split(diff, "\n")
	out := make([]string, 0, len(lines))
	currentPath := ""
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "diff --git "):
			currentPath = patchPathFromLine(line, "diff --git ")
			out = append(out, line)
		case strings.HasPrefix(line, "--- "):
			currentPath = patchPathFromLine(line, "--- ")
			out = append(out, line)
		case strings.HasPrefix(line, "+++ "):
			if currentPath == "" {
				currentPath = patchPathFromLine(line, "+++ ")
			}
			out = append(out, line)
		case strings.HasPrefix(line, "@@ "):
			hunk := []string{line}
			for i+1 < len(lines) {
				next := lines[i+1]
				if strings.HasPrefix(next, "diff --git ") || strings.HasPrefix(next, "--- ") || strings.HasPrefix(next, "+++ ") || strings.HasPrefix(next, "@@ ") {
					break
				}
				i++
				hunk = append(hunk, next)
			}
			if currentPath != "" {
				if found, sourceLines, ok := findHunkSourceMatch(repositoryPath, currentPath, hunk[1:]); ok {
					hunk[0] = setHunkStart(hunk[0], found)
					hunk = rewriteHunkSource(hunk, sourceLines)
				}
			}
			out = append(out, hunk...)
		default:
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func findHunkSourceMatch(root, path string, hunkLines []string) (int, []string, bool) {
	content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		return 0, nil, false
	}
	source := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	sequence := make([]string, 0, len(hunkLines))
	for _, line := range hunkLines {
		if strings.HasPrefix(line, " ") {
			sequence = append(sequence, strings.TrimSpace(strings.TrimPrefix(line, " ")))
		} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "--- ") {
			sequence = append(sequence, strings.TrimSpace(strings.TrimPrefix(line, "-")))
		}
	}
	if len(sequence) == 0 {
		return 0, nil, false
	}
	for start := 0; start+len(sequence) <= len(source); start++ {
		matches := true
		for offset, expected := range sequence {
			if strings.TrimSpace(source[start+offset]) != expected {
				matches = false
				break
			}
		}
		if matches {
			return start + 1, source[start : start+len(sequence)], true
		}
	}
	return 0, nil, false
}

func rewriteHunkSource(hunk []string, sourceLines []string) []string {
	sourceIndex := 0
	for i := 1; i < len(hunk) && sourceIndex < len(sourceLines); i++ {
		line := hunk[i]
		switch {
		case strings.HasPrefix(line, " "):
			hunk[i] = " " + sourceLines[sourceIndex]
			sourceIndex++
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "--- "):
			hunk[i] = "-" + sourceLines[sourceIndex]
			sourceIndex++
		}
	}
	return hunk
}

func setHunkStart(header string, line int) string {
	parts := strings.SplitN(header, "@@ ", 2)
	if len(parts) != 2 {
		return header
	}
	body := strings.SplitN(parts[1], " @@", 2)
	if len(body) != 2 {
		return header
	}
	ranges := strings.Split(body[0], " ")
	if len(ranges) != 2 {
		return header
	}
	oldRange := ranges[0]
	newRange := ranges[1]
	oldParts := strings.SplitN(oldRange, ",", 2)
	newParts := strings.SplitN(newRange, ",", 2)
	if len(oldParts) == 0 || len(newParts) == 0 {
		return header
	}
	oldParts[0] = strconv.Itoa(line)
	newParts[0] = strconv.Itoa(line)
	return "@@ -" + strings.Join(oldParts, ",") + " +" + strings.Join(newParts, ",") + " @@" + body[1]
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

// CopyRepository copies a repository into a fresh sandbox worktree.
func CopyRepository(source, destination string, maxBytes int64) error {
	return copyRepository(source, destination, maxBytes)
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
