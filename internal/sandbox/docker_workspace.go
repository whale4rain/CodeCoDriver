package sandbox

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	workspaceRoot           = "/workspace"
	workspaceVolumePrefix   = "codecodriver-workspace-"
	DefaultDockerImage      = "codecodriver-sandbox:local"
	maxSearchResultRows     = 50
	maxWorkspaceOutputBytes = 64 * 1024
)

// DockerWorkspace keeps one repository volume per task and executes every file
// operation inside an isolated container. The original host repository remains
// read-only for the agent.
type DockerWorkspace struct {
	config    Config
	image     string
	dockerBin string
	volume    string
	memory    string
	cpus      string
	network   string
	pidsLimit int
}

func NewDockerWorkspace(ctx context.Context, source string, config Config) (Workspace, error) {
	config = normalizeConfig(config)
	if config.Image == "" {
		config.Image = DefaultDockerImage
	}
	if config.DockerBin == "" {
		config.DockerBin = "docker"
	}
	if config.MemoryLimit == "" {
		config.MemoryLimit = "2g"
	}
	if config.CPULimit == "" {
		config.CPULimit = "2"
	}
	if config.Network == "" {
		config.Network = "none"
	}
	if config.GoProxy == "" {
		config.GoProxy = "https://goproxy.cn,direct"
	}
	if config.PidsLimit <= 0 {
		config.PidsLimit = 256
	}
	id := fmt.Sprintf("%d", time.Now().UnixNano())
	workspace := &DockerWorkspace{
		config:    config,
		image:     config.Image,
		dockerBin: config.DockerBin,
		volume:    workspaceVolumePrefix + id,
		memory:    config.MemoryLimit,
		cpus:      config.CPULimit,
		network:   config.Network,
		pidsLimit: config.PidsLimit,
	}
	ok := false
	defer func() {
		if !ok {
			_ = workspace.Close(context.Background())
		}
	}()
	if err := workspace.importRepository(ctx, source); err != nil {
		return nil, err
	}
	if err := workspace.initializeGit(ctx); err != nil {
		return nil, err
	}
	ok = true
	return workspace, nil
}

func (w *DockerWorkspace) ReadFile(ctx context.Context, path string, start, end int) (map[string]any, error) {
	relative, err := workspaceRelativePath(path)
	if err != nil {
		return nil, err
	}
	script := `import json, os, sys
p = sys.argv[1]
try:
    start = max(1, int(sys.argv[2] or 1))
except ValueError:
    start = 1
try:
    end = int(sys.argv[3] or 0)
except ValueError:
    end = 0
try:
    with open(os.path.join("/workspace", p), "rb") as handle:
        raw = handle.read()
except OSError as exc:
    print(json.dumps({"error": str(exc)}))
    sys.exit(1)
text = raw.decode("utf-8", "replace").replace("\r\n", "\n")
lines = text.split("\n")
if start <= 0:
    start = 1
if end <= 0 or end > len(lines):
    end = len(lines)
if start > len(lines):
    start = len(lines)
if start > end:
    start = end
body = "\n".join(lines[start-1:end])
print(json.dumps({"path": p, "start": start, "end": end, "lines": end - start + 1, "content": body}))`
	output, runErr := w.run(ctx, "python3", "-c", script, relative, strconv.Itoa(start), strconv.Itoa(end))
	if runErr != nil {
		return nil, dockerCommandError("read file", output, runErr)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return nil, fmt.Errorf("parse read_file result: %w", err)
	}
	if message, ok := result["error"].(string); ok {
		return nil, fmt.Errorf("%s", message)
	}
	return result, nil
}

func (w *DockerWorkspace) SearchFiles(ctx context.Context, query string, maxRows int) ([]map[string]any, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("query is required")
	}
	if maxRows <= 0 {
		maxRows = maxSearchResultRows
	}
	script := `import json, os, re, subprocess, sys
query = sys.argv[1]
limit = int(sys.argv[2])
language_by_extension = {
    ".go": "go",
    ".py": "python",
    ".js": "javascript",
    ".ts": "typescript",
    ".tsx": "typescript",
    ".jsx": "javascript",
    ".md": "markdown",
    ".json": "json",
}

def language_for(path):
    _, extension = os.path.splitext(path.lower())
    return language_by_extension.get(extension, "")

command = ["rg", "--no-heading", "--line-number", "-i", "-F",
           "--glob", "!.git/**", "--glob", "!node_modules/**",
           "--glob", "!vendor/**", "--", query, "/workspace"]
proc = subprocess.run(command, capture_output=True, text=True)
matches = []
for raw in proc.stdout.splitlines():
    if len(matches) >= limit:
        break
    parts = raw.split(":", 2)
    if len(parts) < 3:
        continue
    path = parts[0]
    try:
        relative = os.path.relpath(path, "/workspace")
    except ValueError:
        relative = path
    line = parts[1]
    content = parts[2][:500]
    matches.append({"path": relative, "line": line, "content": content, "language": language_for(relative)})
print(json.dumps(matches))`
	output, runErr := w.run(ctx, "python3", "-c", script, query, strconv.Itoa(maxRows))
	if runErr != nil {
		return nil, dockerCommandError("search files", output, runErr)
	}
	var matches []map[string]any
	if err := json.Unmarshal([]byte(output), &matches); err != nil {
		return nil, fmt.Errorf("parse search_files result: %w", err)
	}
	return matches, nil
}

func (w *DockerWorkspace) ReadSymbols(ctx context.Context, query string, maxRows int) ([]map[string]any, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("query is required")
	}
	if maxRows <= 0 {
		maxRows = maxSearchResultRows
	}
	script := `import json, os, re, subprocess, sys
query = sys.argv[1]
limit = int(sys.argv[2])
command = ["rg", "--no-heading", "--line-number", "-i",
           "--glob", "!.git/**", "--glob", "!node_modules/**",
           "--glob", "!vendor/**", "--", query, "/workspace"]
proc = subprocess.run(command, capture_output=True, text=True)
symbol_patterns = [
    re.compile(r"^\s*(?:func|def|function|class|interface|enum|struct|type|const|var)\s+([A-Za-z_][A-Za-z0-9_]*)"),
    re.compile(r"^\s*public\s+(?:static\s+)?(?:class|interface|enum)\s+([A-Za-z_][A-Za-z0-9_]*)"),
]
matches = []
for raw in proc.stdout.splitlines():
    if len(matches) >= limit:
        break
    parts = raw.split(":", 2)
    if len(parts) < 3:
        continue
    path = parts[0]
    try:
        relative = os.path.relpath(path, "/workspace")
    except ValueError:
        relative = path
    content = parts[2][:500]
    name = ""
    kind = "match"
    for pattern in symbol_patterns:
        found = pattern.search(content)
        if found:
            name = found.group(1)
            kind = "symbol"
            break
    if not name:
        first = re.sub(r"[\W_]+", " ", content).strip().split()
        if first:
            name = first[0]
    matches.append({"name": name, "file": relative, "kind": kind, "line": parts[1], "content": content})
print(json.dumps(matches))`
	output, runErr := w.run(ctx, "python3", "-c", script, query, strconv.Itoa(maxRows))
	if runErr != nil {
		return nil, dockerCommandError("read symbols", output, runErr)
	}
	var matches []map[string]any
	if err := json.Unmarshal([]byte(output), &matches); err != nil {
		return nil, fmt.Errorf("parse read_symbols result: %w", err)
	}
	return matches, nil
}

func (w *DockerWorkspace) EditFile(ctx context.Context, path, oldText, newText, content string, start, end int) (map[string]any, error) {
	relative, err := workspaceRelativePath(path)
	if err != nil {
		return nil, err
	}
	if oldText == "" && content == "" {
		return nil, fmt.Errorf("old_string or content is required")
	}
	payload, err := json.Marshal(map[string]any{
		"path":      relative,
		"old_text":  oldText,
		"new_text":  newText,
		"content":   content,
		"start":     start,
		"end":       end,
		"workspace": workspaceRoot,
	})
	if err != nil {
		return nil, err
	}
	script := `import json, os, sys
try:
    data = json.load(sys.stdin)
except Exception as exc:
    print(json.dumps({"error": "invalid edit payload: " + str(exc)}))
    sys.exit(1)
path = os.path.join(data["workspace"], data["path"])
try:
    with open(path, "rb") as handle:
        raw = handle.read()
except OSError as exc:
    print(json.dumps({"error": str(exc)}))
    sys.exit(1)

def normalize(value):
    return value.replace("\r\n", "\n")

text = normalize(raw.decode("utf-8", "replace"))
old_text = data.get("old_text") or ""
if old_text:
    old = normalize(old_text)
    if old not in text:
        print(json.dumps({"error": "old_string not found in " + data["path"]}))
        sys.exit(1)
    updated = text.replace(old, normalize(data.get("new_text") or ""), 1)
else:
    replacement = normalize(data.get("content") or "").split("\n")
    while replacement and replacement[-1] == "":
        replacement.pop()
    lines = text.split("\n")
    start = max(1, int(data.get("start") or 1))
    end = int(data.get("end") or 0)
    if end <= 0 or end > len(lines):
        end = len(lines)
    if start > len(lines):
        start = len(lines)
    if start > end:
        start = end
    existing = lines[start-1:end]
    already_present = existing == replacement
    if not already_present and start-1+len(replacement) <= len(lines):
        already_present = lines[start-1:start-1+len(replacement)] == replacement
    if not already_present and replacement:
        already_present = all(not line.strip() or line in lines for line in replacement)
    if already_present:
        print(json.dumps({"path": data["path"], "changed": False,
                          "file_lines": len(lines),
                          "reason": "requested content already present at range"}))
        sys.exit(0)
    updated = "\n".join(lines[:start-1] + replacement + lines[end:])
if updated == text:
    print(json.dumps({"path": data["path"], "changed": False,
                      "file_lines": len(text.split("\n")),
                      "reason": "edit result is identical to current content"}))
    sys.exit(0)
if b"\r\n" in raw:
    updated = updated.replace("\n", "\r\n")
with open(path, "wb") as handle:
    handle.write(updated.encode("utf-8"))
print(json.dumps({"path": data["path"], "changed": True,
                  "file_lines": len(updated.split("\n"))}))`
	output, runErr := w.runWithInput(ctx, payload, "python3", "-c", script)
	if runErr != nil {
		return nil, dockerCommandError("edit file", output, runErr)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return nil, fmt.Errorf("parse edit_file result: %w", err)
	}
	if message, ok := result["error"].(string); ok {
		return nil, fmt.Errorf("%s", message)
	}
	return result, nil
}

func (w *DockerWorkspace) WriteFile(ctx context.Context, path, content string) (map[string]any, error) {
	relative, err := workspaceRelativePath(path)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(map[string]any{"path": relative, "content": content, "workspace": workspaceRoot})
	if err != nil {
		return nil, err
	}
	script := `import json, os, sys
try:
    data = json.load(sys.stdin)
except Exception as exc:
    print(json.dumps({"error": "invalid write payload: " + str(exc)}))
    sys.exit(1)
path = os.path.join(data["workspace"], data["path"])
try:
    with open(path, "rb") as handle:
        raw = handle.read()
    line_ending = b"\r\n" if b"\r\n" in raw else b"\n"
except OSError:
    line_ending = b"\n"
os.makedirs(os.path.dirname(path), exist_ok=True)
content = data.get("content") or ""
if line_ending == b"\r\n":
    content = content.replace("\n", "\r\n")
with open(path, "wb") as handle:
    handle.write(content.encode("utf-8"))
print(json.dumps({"path": data["path"], "changed": True}))`
	output, runErr := w.runWithInput(ctx, payload, "python3", "-c", script)
	if runErr != nil {
		return nil, dockerCommandError("write file", output, runErr)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return nil, fmt.Errorf("parse write_file result: %w", err)
	}
	if message, ok := result["error"].(string); ok {
		return nil, fmt.Errorf("%s", message)
	}
	return result, nil
}

func (w *DockerWorkspace) GeneratePatch(ctx context.Context) (string, error) {
	output, runErr := w.run(ctx, "sh", "-c", "git add -A && git diff --cached --binary")
	if runErr != nil {
		return "", fmt.Errorf("generate workspace patch: %s: %w", limitOutput(output, maxWorkspaceOutputBytes), runErr)
	}
	if strings.TrimSpace(output) == "" {
		return "", fmt.Errorf("workspace has no changes")
	}
	return output, nil
}

func (w *DockerWorkspace) Reset(ctx context.Context) error {
	output, runErr := w.run(ctx, "sh", "-c", "git reset --hard -q HEAD && git clean -fdq")
	if runErr != nil {
		return fmt.Errorf("reset workspace: %s: %w", limitOutput(output, maxWorkspaceOutputBytes), runErr)
	}
	return nil
}

func (w *DockerWorkspace) RunTest(ctx context.Context, command string) Report {
	changedFiles, err := w.stagedChangedFiles(ctx)
	if err != nil {
		return Report{Status: "sandbox_error", PatchExtracted: true, Error: err.Error()}
	}
	if command == "" {
		_, probeErr := w.run(ctx, "sh", "-c", "test -f go.mod")
		if probeErr != nil {
			return Report{Status: "tests_skipped", PatchExtracted: true, Applied: true, ChangedFiles: changedFiles, Output: "no supported test runner detected"}
		}
		command = "go test ./..."
	}
	commandCtx, cancel := context.WithTimeout(ctx, w.config.CommandTimeout)
	defer cancel()
	output, runErr := w.run(commandCtx, "sh", "-c", `eval "$1"`, "codecodriver", command)
	report := Report{Status: "applied", PatchExtracted: true, Applied: true, TestCommand: command, ChangedFiles: changedFiles, Output: limitOutput(output, w.config.MaxOutputBytes)}
	if runErr != nil {
		report.Status = "tests_failed"
		report.Error = commandError(commandCtx, runErr)
		return report
	}
	report.Status = "passed"
	report.Passed = true
	return report
}

func (w *DockerWorkspace) Close(ctx context.Context) error {
	if w == nil || w.volume == "" || w.dockerBin == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cleanupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, _ = run(cleanupCtx, "", w.dockerBin, "volume", "rm", "-f", w.volume)
	return nil
}

// NewWorkspaceFromEnv creates an isolated Docker workspace using the same
// environment overrides as the sandbox runner. The repository is imported into
// a per-task volume and never bind-mounted from the host.
func NewWorkspaceFromEnv(ctx context.Context, source string) (Workspace, error) {
	config := Config{
		Image:          os.Getenv("CODECODRIVER_SANDBOX_IMAGE"),
		DockerBin:      os.Getenv("CODECODRIVER_SANDBOX_DOCKER_BIN"),
		MemoryLimit:    os.Getenv("CODECODRIVER_SANDBOX_MEMORY"),
		CPULimit:       os.Getenv("CODECODRIVER_SANDBOX_CPUS"),
		Network:        os.Getenv("CODECODRIVER_SANDBOX_NETWORK"),
		GoProxy:        os.Getenv("CODECODRIVER_SANDBOX_GOPROXY"),
		CommandTimeout: timeoutFromEnv("CODECODRIVER_SANDBOX_TIMEOUT_SECONDS"),
	}
	if raw := os.Getenv("CODECODRIVER_SANDBOX_PIDS_LIMIT"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil {
			config.PidsLimit = value
		}
	}
	return NewDockerWorkspace(ctx, source, config)
}

func timeoutFromEnv(name string) time.Duration {
	if raw := os.Getenv(name); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
	}
	return 0
}

func (w *DockerWorkspace) importRepository(ctx context.Context, source string) error {
	reader, writer := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		err := writeRepositoryTar(source, writer, w.config.MaxCopyBytes)
		_ = writer.CloseWithError(err)
		errCh <- err
	}()
	output, runErr := w.runWithInputAsUser(ctx, reader, "nobody:nogroup", "tar", "-xf", "-", "-C", workspaceRoot)
	_ = reader.Close()
	streamErr := <-errCh
	if runErr != nil {
		return fmt.Errorf("import repository into docker workspace: %s: %w", limitOutput(output, maxWorkspaceOutputBytes), runErr)
	}
	if streamErr != nil {
		return fmt.Errorf("import repository into docker workspace: %w", streamErr)
	}
	return nil
}

func (w *DockerWorkspace) initializeGit(ctx context.Context) error {
	script := "git init -q && git config user.email codecodriver@example.com && git config user.name CodeCoDriver && git add -A && git commit -q -m baseline"
	output, runErr := w.runAsUser(ctx, "nobody:nogroup", "sh", "-c", script)
	if runErr != nil {
		return fmt.Errorf("initialize workspace git baseline: %s: %w", limitOutput(output, maxWorkspaceOutputBytes), runErr)
	}
	return nil
}

func (w *DockerWorkspace) stagedChangedFiles(ctx context.Context) ([]string, error) {
	output, runErr := w.run(ctx, "git", "diff", "--name-only", "--cached", "-z")
	if runErr != nil {
		return nil, fmt.Errorf("list workspace changes: %s: %w", limitOutput(output, maxWorkspaceOutputBytes), runErr)
	}
	output = strings.TrimRight(output, "\x00")
	if output == "" {
		return []string{}, nil
	}
	return strings.Split(output, "\x00"), nil
}

func (w *DockerWorkspace) run(ctx context.Context, args ...string) (string, error) {
	return w.runAsUser(ctx, "nobody:nogroup", args...)
}

func (w *DockerWorkspace) runAsUser(ctx context.Context, user string, args ...string) (string, error) {
	return w.runWithInputAsUser(ctx, nil, user, args...)
}

func (w *DockerWorkspace) runWithInput(ctx context.Context, input []byte, args ...string) (string, error) {
	return w.runWithInputAsUser(ctx, readerFromBytes(input), "nobody:nogroup", args...)
}

func (w *DockerWorkspace) runWithInputAsUser(ctx context.Context, input io.Reader, user string, args ...string) (string, error) {
	dockerArgs := []string{
		"run", "--rm",
		"--network", w.network,
		"--read-only",
		"--tmpfs", "/tmp:rw,exec,nosuid,nodev,size=256m",
		"-v", w.volume + ":" + workspaceRoot,
		"-w", workspaceRoot,
	}
	if input != nil {
		dockerArgs = append(dockerArgs, "--interactive")
	}
	if user != "" {
		dockerArgs = append(dockerArgs, "--user", user)
	}
	dockerArgs = append(dockerArgs,
		"--memory", w.memory,
		"--cpus", w.cpus,
		"--pids-limit", strconv.Itoa(w.pidsLimit),
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"-e", "HOME=/tmp",
		"-e", "GOCACHE=/tmp/gocache",
		"-e", "GOPATH=/tmp/gopath",
		"-e", "GOTOOLCHAIN=local",
		"-e", "GOTELEMETRY=off",
		"-e", "GOPROXY="+w.config.GoProxy,
		w.image,
	)
	dockerArgs = append(dockerArgs, args...)
	return runWithInput(ctx, "", nil, input, w.dockerBin, dockerArgs...)
}

func workspaceRelativePath(path string) (string, error) {
	path = strings.TrimSpace(strings.ReplaceAll(path, "\\", "/"))
	path = strings.TrimPrefix(path, "./")
	if path == "" || strings.HasPrefix(path, "/") || filepath.IsAbs(path) {
		return "", fmt.Errorf("path escapes workspace: %q", path)
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".." || part == ".git" {
			return "", fmt.Errorf("path escapes workspace: %q", path)
		}
	}
	return path, nil
}

func dockerCommandError(operation, output string, err error) error {
	return fmt.Errorf("%s: %s: %w", operation, strings.TrimSpace(limitOutput(output, maxWorkspaceOutputBytes)), err)
}

func readerFromBytes(data []byte) io.Reader {
	if len(data) == 0 {
		return nil
	}
	return strings.NewReader(string(data))
}

func writeRepositoryTar(source string, output io.Writer, maxBytes int64) error {
	writer := tar.NewWriter(output)
	var copied int64
	walkErr := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".cache", "node_modules":
				return filepath.SkipDir
			}
			header, err := tar.FileInfoHeader(entryInfo(entry), "")
			if err != nil {
				return err
			}
			header.Name = filepath.ToSlash(relative) + "/"
			return writer.WriteHeader(header)
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
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(writer, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if closeErr := writer.Close(); closeErr != nil {
		return closeErr
	}
	return walkErr
}

func entryInfo(entry fs.DirEntry) fs.FileInfo {
	info, err := entry.Info()
	if err != nil {
		return fakeDirInfo{name: entry.Name()}
	}
	return info
}

type fakeDirInfo struct {
	name string
}

func (f fakeDirInfo) Name() string       { return f.name }
func (f fakeDirInfo) Size() int64        { return 0 }
func (f fakeDirInfo) Mode() fs.FileMode  { return fs.ModeDir | 0o755 }
func (f fakeDirInfo) ModTime() time.Time { return time.Time{} }
func (f fakeDirInfo) IsDir() bool        { return true }
func (f fakeDirInfo) Sys() any           { return nil }

func language_for(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".js", ".ts", ".tsx", ".jsx":
		return "javascript"
	case ".md":
		return "markdown"
	case ".json":
		return "json"
	default:
		return ""
	}
}
