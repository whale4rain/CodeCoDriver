package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codecodriver/internal/sandbox"
)

func main() {
	root, err := os.MkdirTemp("", "codecodriver-sandbox-smoke-*")
	if err != nil {
		fatal(err)
	}
	defer os.RemoveAll(root)

	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module smoke\n\ngo 1.24\n"), 0o600); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "smoke.go"), []byte("package smoke\n\nfunc Value() int { return 1 }\n"), 0o600); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "smoke_test.go"), []byte("package smoke\n\nimport \"testing\"\n\nfunc TestValue(t *testing.T) {\n\tif Value() != 2 {\n\t\tt.Fatal(\"expected patched value\")\n\t}\n}\n"), 0o600); err != nil {
		fatal(err)
	}

	config := sandbox.Config{
		TestCommand:    "go test ./...",
		Image:          envOr("CODECODRIVER_SANDBOX_IMAGE", sandbox.DefaultDockerImage),
		DockerBin:      envOr("CODECODRIVER_SANDBOX_DOCKER_BIN", "docker"),
		MemoryLimit:    envOr("CODECODRIVER_SANDBOX_MEMORY", "512m"),
		CPULimit:       envOr("CODECODRIVER_SANDBOX_CPUS", "1"),
		Network:        envOr("CODECODRIVER_SANDBOX_NETWORK", "none"),
		CommandTimeout: 90 * time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	workspace, err := sandbox.NewDockerWorkspace(ctx, root, config)
	if err != nil {
		fatal(err)
	}
	defer workspace.Close(context.Background())
	if _, err := workspace.EditFile(ctx, "smoke.go", "func Value() int { return 1 }", "func Value() int { return 2 }", "", 0, 0); err != nil {
		fatal(err)
	}
	patch, err := workspace.GeneratePatch(ctx)
	if err != nil {
		fatal(err)
	}
	fmt.Println(patch)
	report := workspace.RunTest(ctx, "go test ./...")
	data, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(data))
	if report.Status != "passed" || !report.Applied || !report.Passed {
		os.Exit(1)
	}
	edited, err := workspace.ReadFile(ctx, "smoke.go", 1, 10)
	if err != nil {
		fatal(err)
	}
	if content, ok := edited["content"].(string); !ok || !strings.Contains(content, "func Value() int { return 2 }") {
		fatal(fmt.Errorf("workspace was not edited as expected"))
	}
	content, err := os.ReadFile(filepath.Join(root, "smoke.go"))
	if err != nil {
		fatal(err)
	}
	if string(content) != "package smoke\n\nfunc Value() int { return 1 }\n" {
		fatal(fmt.Errorf("original repository was modified by Docker sandbox"))
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
