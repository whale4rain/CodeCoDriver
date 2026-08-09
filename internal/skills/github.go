package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const maxSkillFileBytes = 2 << 20

func ImportFromGitHub(ctx context.Context, rawURL, destDir string) ([]Skill, error) {
	rawURL = strings.TrimSpace(rawURL)
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid GitHub URL: %w", err)
	}
	host := strings.ToLower(parsed.Host)
	if host != "github.com" && host != "www.github.com" {
		return nil, fmt.Errorf("only github.com URLs are supported")
	}
	if strings.HasSuffix(strings.ToLower(parsed.Path), ".json") {
		return importSkillJSON(ctx, rawURL, destDir)
	}
	return importSkillRepository(ctx, rawURL, destDir)
}

func importSkillJSON(ctx context.Context, rawURL, destDir string) ([]Skill, error) {
	data, err := fetchURL(ctx, rawGitHubFileURL(rawURL))
	if err != nil {
		return nil, err
	}
	registry := New()
	if err := registry.Load(data); err != nil {
		return nil, fmt.Errorf("invalid skill JSON: %w", err)
	}
	skills := registry.List()
	if len(skills) == 0 {
		return nil, fmt.Errorf("skill JSON did not contain a skill")
	}
	for _, skill := range skills {
		if !likelySkill(skill) {
			continue
		}
		if _, err := writeSkillFile(destDir, skill); err != nil {
			return nil, err
		}
	}
	imported := []Skill{}
	for _, skill := range skills {
		if likelySkill(skill) {
			imported = append(imported, skill)
		}
	}
	if len(imported) == 0 {
		return nil, fmt.Errorf("skill JSON did not contain a valid skill")
	}
	return imported, nil
}

func importSkillRepository(ctx context.Context, rawURL, destDir string) ([]Skill, error) {
	tmp, err := os.MkdirTemp("", "codecodriver-skills-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", rawURL, tmp)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("clone GitHub skill repo: %v: %s", err, strings.TrimSpace(string(output)))
	}
	var files []string
	if err := filepath.WalkDir(tmp, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.EqualFold(filepath.Ext(path), ".json") {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("walk imported skill repo: %w", err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("GitHub skill repo did not contain any .json skill files")
	}
	imported := []Skill{}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		registry := New()
		if err := registry.Load(data); err != nil {
			continue
		}
		for _, skill := range registry.List() {
			if !likelySkill(skill) {
				continue
			}
			if _, err := writeSkillFile(destDir, skill); err != nil {
				return nil, err
			}
			imported = append(imported, skill)
		}
	}
	if len(imported) == 0 {
		return nil, fmt.Errorf("GitHub skill repo did not contain valid skills")
	}
	return imported, nil
}

func fetchURL(ctx context.Context, rawURL string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github.raw+json")
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download skill file: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("download skill file returned status %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxSkillFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSkillFileBytes {
		return nil, fmt.Errorf("skill file exceeds %d bytes", maxSkillFileBytes)
	}
	return data, nil
}

func rawGitHubFileURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	parts := strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/")
	if len(parts) >= 5 && (parts[2] == "blob" || parts[2] == "raw") {
		return "https://raw.githubusercontent.com/" + parts[0] + "/" + parts[1] + "/" + strings.Join(parts[3:], "/")
	}
	return rawURL
}

func writeSkillFile(dir string, skill Skill) (string, error) {
	if err := skill.Validate(); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(skill, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, skillFileName(skill.Name)+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}
