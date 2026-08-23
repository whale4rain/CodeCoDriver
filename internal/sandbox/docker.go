package sandbox

import (
	"archive/tar"
	"context"
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
	DefaultDockerImage = "codecodriver-sandbox:local"
	applyFailedMarker  = "__CODECODRIVER_APPLY_FAILED__"
)

// DockerRunner executes patch validation inside an ephemeral Docker container.
// It never bind-mounts the original repository and never exposes the Docker
// socket to the untrusted test command.
type DockerRunner struct {
	config    Config
	image     string
	dockerBin string
	memory    string
	cpus      string
	network   string
	pidsLimit int
}

func NewDocker(config Config) *DockerRunner {
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
	return &DockerRunner{
		config:    config,
		image:     config.Image,
		dockerBin: config.DockerBin,
		memory:    config.MemoryLimit,
		cpus:      config.CPULimit,
		network:   config.Network,
		pidsLimit: config.PidsLimit,
	}
}

// WithTestCommand returns a Docker runner with the same isolation limits but a
// different repository test command.
func (r *DockerRunner) WithTestCommand(command string) Validator {
	config := r.config
	config.TestCommand = command
	return NewDocker(config)
}

func (r *DockerRunner) ValidateAndTest(ctx context.Context, repositoryPath, proposal string) Report {
	diff, files, report := prepareValidation(r.config, repositoryPath, proposal)
	if report != nil {
		return *report
	}
	diff = normalizePatchLineEndings(diff)
	workdir, err := os.MkdirTemp("", "codecodriver-docker-sandbox-*")
	if err != nil {
		return Report{Status: "sandbox_error", PatchExtracted: true, ChangedFiles: files, Error: err.Error()}
	}
	defer os.RemoveAll(workdir)
	if err := copyRepository(repositoryPath, workdir, r.config.MaxCopyBytes); err != nil {
		return Report{Status: "sandbox_error", PatchExtracted: true, ChangedFiles: files, Error: err.Error()}
	}
	patchPath := filepath.Join(workdir, ".codecodriver.patch")
	if err := os.WriteFile(patchPath, []byte(diff), 0o600); err != nil {
		return Report{Status: "sandbox_error", PatchExtracted: true, ChangedFiles: files, Error: err.Error()}
	}
	tarPath := filepath.Join(workdir, ".codecodriver.repo.tar")
	if err := writeTar(workdir, tarPath); err != nil {
		return Report{Status: "sandbox_error", PatchExtracted: true, ChangedFiles: files, Error: err.Error()}
	}
	testCommand := r.config.TestCommand
	hasGoMod := fileExists(filepath.Join(workdir, "go.mod"))
	if hasGoMod && testCommand == "" {
		testCommand = "go test ./..."
	}
	commandCtx, cancel := context.WithTimeout(ctx, r.config.CommandTimeout)
	defer cancel()
	output, runErr := r.runDocker(commandCtx, workdir, testCommand)
	report = &Report{
		Status:         "applied",
		PatchExtracted: true,
		Applied:        true,
		ChangedFiles:   files,
		Output:         limitOutput(output, r.config.MaxOutputBytes),
	}
	if runErr != nil {
		if strings.Contains(output, applyFailedMarker) {
			report.Status = "apply_failed"
			report.Applied = false
			report.Error = commandError(ctx, runErr)
			return *report
		}
		if hasGoMod {
			report.Status = "tests_failed"
			report.Error = commandError(ctx, runErr)
		} else {
			report.Status = "sandbox_error"
			report.Error = commandError(ctx, runErr)
		}
		return *report
	}
	if !hasGoMod {
		report.Status = "tests_skipped"
		report.Output = "no supported test runner detected"
		return *report
	}
	report.TestCommand = testCommand
	report.Status = "passed"
	report.Passed = true
	return *report
}

func (r *DockerRunner) runDocker(ctx context.Context, workdir, testCommand string) (string, error) {
	id := fmt.Sprintf("%d", time.Now().UnixNano())
	container := "codecodriver-sandbox-" + id
	volume := "codecodriver-sandbox-volume-" + id
	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = run(cleanupCtx, "", r.dockerBin, "rm", "-f", container)
		_, _ = run(cleanupCtx, "", r.dockerBin, "volume", "rm", "-f", volume)
	}
	defer cleanup()

	script := `set +e
mkdir -p /workspace
tar -xf /workspace/.codecodriver.repo.tar -C /workspace
git apply --check --recount --ignore-space-change --whitespace=error-all /workspace/.codecodriver.patch >/tmp/apply-check.log 2>&1
check_status=$?
if [ $check_status -ne 0 ]; then
  cat /tmp/apply-check.log
  echo "` + applyFailedMarker + `"
  exit 10
fi
git apply --recount --ignore-space-change --whitespace=error-all /workspace/.codecodriver.patch >/tmp/apply.log 2>&1
apply_status=$?
if [ $apply_status -ne 0 ]; then
  cat /tmp/apply.log
  echo "` + applyFailedMarker + `"
  exit 11
fi
if [ $# -gt 0 ]; then
  eval "$1"
fi`

	createArgs := []string{
		"create",
		"--name", container,
		"--network", r.network,
		"--read-only",
		"--tmpfs", "/tmp:rw,exec,nosuid,nodev,size=1g",
		"-v", volume + ":/workspace",
		"-w", "/workspace",
		"--user", "nobody",
		"--memory", r.memory,
		"--cpus", r.cpus,
		"--pids-limit", strconv.Itoa(r.pidsLimit),
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"-e", "GOCACHE=/tmp/gocache",
		"-e", "GOPATH=/tmp/gopath",
		"-e", "HOME=/tmp",
		"-e", "GOTOOLCHAIN=local",
		"-e", "GOTELEMETRY=off",
		"-e", "GOPROXY=" + r.config.GoProxy,
		r.image,
		"sh", "-c", script, "codecodriver",
	}
	if testCommand != "" {
		createArgs = append(createArgs, testCommand)
	}
	if output, err := run(ctx, "", r.dockerBin, createArgs...); err != nil {
		return output, fmt.Errorf("docker create: %w", err)
	}
	tarSource := filepath.Join(workdir, ".codecodriver.repo.tar")
	if output, err := run(ctx, "", r.dockerBin, "cp", tarSource, container+":/workspace/.codecodriver.repo.tar"); err != nil {
		return output, fmt.Errorf("docker cp: %w", err)
	}
	patchSource := filepath.Join(workdir, ".codecodriver.patch")
	if output, err := run(ctx, "", r.dockerBin, "cp", patchSource, container+":/workspace/.codecodriver.patch"); err != nil {
		return output, fmt.Errorf("docker cp patch: %w", err)
	}
	if output, err := r.prepareVolumePermissions(ctx, volume); err != nil {
		return output, err
	}
	output, err := run(ctx, "", r.dockerBin, "start", "-a", container)
	if err != nil {
		return output, fmt.Errorf("docker start: %w", err)
	}
	return output, nil
}

// prepareVolumePermissions makes files copied into a named volume readable by
// the unprivileged execution user. docker cp is performed by the daemon and
// may create root-owned files with restrictive modes, so we run a tiny helper
// container as root that only changes permissions. The execution container
// still runs every untrusted command as nobody.
func (r *DockerRunner) prepareVolumePermissions(ctx context.Context, volume string) (string, error) {
	args := []string{
		"run", "--rm",
		"--network", r.network,
		"-v", volume + ":/workspace",
		"--user", "root:root",
		"--entrypoint", "chmod",
		r.image,
		"0644",
		"/workspace/.codecodriver.patch",
		"/workspace/.codecodriver.repo.tar",
	}
	output, err := run(ctx, "", r.dockerBin, args...)
	if err != nil {
		return output, fmt.Errorf("docker chmod sandbox files: %w", err)
	}
	return output, nil
}

func writeTar(source, target string) error {
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	output, err := os.Create(target)
	if err != nil {
		return err
	}
	writer := tar.NewWriter(output)
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
		if relative == ".codecodriver.repo.tar" || relative == ".codecodriver.patch" {
			if pathAbs, absErr := filepath.Abs(path); absErr == nil && pathAbs == targetAbs {
				return nil
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
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
	if closeErr := output.Close(); closeErr != nil {
		return closeErr
	}
	return walkErr
}

// FromEnv returns the sandbox driver selected by CODECODRIVER_SANDBOX_DRIVER.
// Supported values are "local" (default) and "docker".
func FromEnv() Validator {
	driver := strings.ToLower(strings.TrimSpace(os.Getenv("CODECODRIVER_SANDBOX_DRIVER")))
	config := Config{
		TestCommand:    os.Getenv("CODECODRIVER_SANDBOX_TEST_COMMAND"),
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
	if driver == "docker" {
		return NewDocker(config)
	}
	return New(config)
}

func timeoutFromEnv(name string) time.Duration {
	if raw := os.Getenv(name); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
	}
	return 0
}
