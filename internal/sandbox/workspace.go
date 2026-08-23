package sandbox

import "context"

// Workspace is an isolated mutable copy of a repository. All file-related agent
// tools operate on this workspace so the original repository is never touched.
type Workspace interface {
	ReadFile(context.Context, string, int, int) (map[string]any, error)
	SearchFiles(context.Context, string, int) ([]map[string]any, error)
	ReadSymbols(context.Context, string, int) ([]map[string]any, error)
	EditFile(context.Context, string, string, string, string, int, int) (map[string]any, error)
	WriteFile(context.Context, string, string) (map[string]any, error)
	GeneratePatch(context.Context) (string, error)
	Reset(context.Context) error
	RunTest(context.Context, string) Report
	Close(context.Context) error
}
