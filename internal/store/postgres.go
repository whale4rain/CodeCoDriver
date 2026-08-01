package store

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"codecodriver/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Postgres struct{ pool *pgxpool.Pool }

var _ Store = (*Postgres)(nil)

func OpenPostgres(ctx context.Context, databaseURL string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	p := &Postgres{pool: pool}
	sql, err := migrations.ReadFile("migrations/001_initial.sql")
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("read migration: %w", err)
	}
	if _, err := pool.Exec(ctx, string(sql)); err != nil {
		pool.Close()
		return nil, fmt.Errorf("apply migration: %w", err)
	}
	return p, nil
}

func (p *Postgres) Close() error { p.pool.Close(); return nil }
func (p *Postgres) ID(prefix string) (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(b[:]), nil
}

func (p *Postgres) AddRepository(r domain.Repository) error {
	_, err := p.pool.Exec(context.Background(), "INSERT INTO repositories(id,name,path,primary_language,file_count,indexed_at,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)", r.ID, r.Name, r.Path, r.PrimaryLanguage, r.FileCount, nullTime(r.IndexedAt), r.CreatedAt)
	return err
}
func (p *Postgres) Repository(id string) (domain.Repository, error) {
	var r domain.Repository
	var indexed *time.Time
	err := p.pool.QueryRow(context.Background(), "SELECT id,name,path,primary_language,file_count,indexed_at,created_at FROM repositories WHERE id=$1", id).Scan(&r.ID, &r.Name, &r.Path, &r.PrimaryLanguage, &r.FileCount, &indexed, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	if indexed != nil {
		r.IndexedAt = *indexed
	}
	return r, err
}
func (p *Postgres) Repositories() ([]domain.Repository, error) {
	rows, err := p.pool.Query(context.Background(), "SELECT id,name,path,primary_language,file_count,indexed_at,created_at FROM repositories ORDER BY created_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Repository{}
	for rows.Next() {
		var r domain.Repository
		var indexed *time.Time
		if err := rows.Scan(&r.ID, &r.Name, &r.Path, &r.PrimaryLanguage, &r.FileCount, &indexed, &r.CreatedAt); err != nil {
			return nil, err
		}
		if indexed != nil {
			r.IndexedAt = *indexed
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (p *Postgres) SetIndex(r domain.Repository, files []domain.RepositoryFile, symbols []domain.Symbol) error {
	ctx := context.Background()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "UPDATE repositories SET primary_language=$2,file_count=$3,indexed_at=$4 WHERE id=$1", r.ID, r.PrimaryLanguage, r.FileCount, r.IndexedAt); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM repository_files WHERE repository_id=$1", r.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM symbols WHERE repository_id=$1", r.ID); err != nil {
		return err
	}
	for _, f := range files {
		if _, err = tx.Exec(ctx, "INSERT INTO repository_files(repository_id,path,language,size,hash,summary) VALUES($1,$2,$3,$4,$5,$6)", f.RepositoryID, f.Path, f.Language, f.Size, f.Hash, f.Summary); err != nil {
			return err
		}
	}
	for _, s := range symbols {
		if _, err = tx.Exec(ctx, "INSERT INTO symbols(repository_id,file_path,name,kind,line) VALUES($1,$2,$3,$4,$5)", s.RepositoryID, s.FilePath, s.Name, s.Kind, s.Line); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func (p *Postgres) Files(id string) ([]domain.RepositoryFile, error) {
	rows, err := p.pool.Query(context.Background(), "SELECT repository_id,path,language,size,hash,summary FROM repository_files WHERE repository_id=$1 ORDER BY path", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.RepositoryFile{}
	for rows.Next() {
		var f domain.RepositoryFile
		if err := rows.Scan(&f.RepositoryID, &f.Path, &f.Language, &f.Size, &f.Hash, &f.Summary); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
func (p *Postgres) Symbols(id string) ([]domain.Symbol, error) {
	rows, err := p.pool.Query(context.Background(), "SELECT repository_id,file_path,name,kind,line FROM symbols WHERE repository_id=$1 ORDER BY file_path,line", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Symbol{}
	for rows.Next() {
		var s domain.Symbol
		if err := rows.Scan(&s.RepositoryID, &s.FilePath, &s.Name, &s.Kind, &s.Line); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (p *Postgres) AddTask(t domain.Task) error {
	_, err := p.pool.Exec(context.Background(), "INSERT INTO tasks(id,repository_id,title,description,status,error,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", t.ID, t.RepositoryID, t.Title, t.Description, t.Status, t.Error, t.CreatedAt, t.UpdatedAt)
	return err
}
func (p *Postgres) Task(id string) (domain.Task, error) {
	var t domain.Task
	err := p.pool.QueryRow(context.Background(), "SELECT id,repository_id,title,description,status,error,created_at,updated_at FROM tasks WHERE id=$1", id).Scan(&t.ID, &t.RepositoryID, &t.Title, &t.Description, &t.Status, &t.Error, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}
func (p *Postgres) Tasks() ([]domain.Task, error) {
	rows, err := p.pool.Query(context.Background(), "SELECT id,repository_id,title,description,status,error,created_at,updated_at FROM tasks ORDER BY created_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Task{}
	for rows.Next() {
		var t domain.Task
		if err := rows.Scan(&t.ID, &t.RepositoryID, &t.Title, &t.Description, &t.Status, &t.Error, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
func (p *Postgres) UpdateTask(id string, status domain.TaskStatus, errorText string) error {
	result, err := p.pool.Exec(context.Background(), "UPDATE tasks SET status=$2,error=$3,updated_at=NOW() WHERE id=$1", id, status, errorText)
	if err == nil && result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (p *Postgres) AddRun(r domain.TaskRun) error {
	_, err := p.pool.Exec(context.Background(), "INSERT INTO task_runs(id,task_id,status,started_at,ended_at) VALUES($1,$2,$3,$4,$5)", r.ID, r.TaskID, r.Status, r.StartedAt, nullTime(r.EndedAt))
	return err
}
func (p *Postgres) FinishRun(taskID, runID string, status domain.TaskStatus) error {
	_, err := p.pool.Exec(context.Background(), "UPDATE task_runs SET status=$3,ended_at=NOW() WHERE task_id=$1 AND id=$2", taskID, runID, status)
	return err
}
func (p *Postgres) Runs(taskID string) ([]domain.TaskRun, error) {
	rows, err := p.pool.Query(context.Background(), "SELECT id,task_id,status,started_at,ended_at FROM task_runs WHERE task_id=$1 ORDER BY started_at", taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.TaskRun{}
	for rows.Next() {
		var r domain.TaskRun
		var ended *time.Time
		if err := rows.Scan(&r.ID, &r.TaskID, &r.Status, &r.StartedAt, &ended); err != nil {
			return nil, err
		}
		if ended != nil {
			r.EndedAt = *ended
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (p *Postgres) AddStep(s domain.TaskStep) error {
	input, err := json.Marshal(s.Input)
	if err != nil {
		return err
	}
	output, err := json.Marshal(s.Output)
	if err != nil {
		return err
	}
	_, err = p.pool.Exec(context.Background(), "INSERT INTO task_steps(id,task_id,run_id,agent_name,step_type,status,input,output,error,started_at,ended_at,latency_ms) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)", s.ID, s.TaskID, s.RunID, s.AgentName, s.StepType, s.Status, input, output, s.Error, s.StartedAt, nullTime(s.EndedAt), s.LatencyMS)
	return err
}
func (p *Postgres) Steps(taskID string) ([]domain.TaskStep, error) {
	rows, err := p.pool.Query(context.Background(), "SELECT id,task_id,run_id,agent_name,step_type,status,input,output,error,started_at,ended_at,latency_ms FROM task_steps WHERE task_id=$1 ORDER BY started_at,id", taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.TaskStep{}
	for rows.Next() {
		var s domain.TaskStep
		var input, output []byte
		var ended *time.Time
		if err := rows.Scan(&s.ID, &s.TaskID, &s.RunID, &s.AgentName, &s.StepType, &s.Status, &input, &output, &s.Error, &s.StartedAt, &ended, &s.LatencyMS); err != nil {
			return nil, err
		}
		if len(input) > 0 {
			if err := json.Unmarshal(input, &s.Input); err != nil {
				return nil, err
			}
		}
		if len(output) > 0 {
			if err := json.Unmarshal(output, &s.Output); err != nil {
				return nil, err
			}
		}
		if ended != nil {
			s.EndedAt = *ended
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (p *Postgres) AddArtifact(a domain.Artifact) error {
	_, err := p.pool.Exec(context.Background(), "INSERT INTO artifacts(id,task_id,run_id,type,name,content,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)", a.ID, a.TaskID, a.RunID, a.Type, a.Name, a.Content, a.CreatedAt)
	return err
}
func (p *Postgres) Artifacts(taskID string) ([]domain.Artifact, error) {
	rows, err := p.pool.Query(context.Background(), "SELECT id,task_id,run_id,type,name,content,created_at FROM artifacts WHERE task_id=$1 ORDER BY created_at,id", taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Artifact{}
	for rows.Next() {
		var a domain.Artifact
		if err := rows.Scan(&a.ID, &a.TaskID, &a.RunID, &a.Type, &a.Name, &a.Content, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (p *Postgres) AddMemory(m domain.MemoryEntry) error {
	metadata, err := json.Marshal(m.Metadata)
	if err != nil {
		return err
	}
	if len(m.Embedding) == 0 {
		m.Embedding = textEmbedding(m.Content)
	}
	embedding, err := json.Marshal(m.Embedding)
	if err != nil {
		return err
	}
	_, err = p.pool.Exec(context.Background(), "INSERT INTO memory_entries(id,repository_id,task_id,kind,content,source,score,metadata,embedding,created_at) VALUES($1,$2,NULLIF($3,''),$4,$5,$6,$7,$8,$9,$10)", m.ID, m.RepositoryID, m.TaskID, m.Kind, m.Content, m.Source, m.Score, metadata, embedding, m.CreatedAt)
	return err
}
func (p *Postgres) SearchMemory(repoID, query string) ([]domain.MemoryEntry, error) {
	return p.SearchMemoryLimit(repoID, query, 20)
}
func (p *Postgres) SearchMemoryLimit(repoID, query string, limit int) ([]domain.MemoryEntry, error) {
	rows, err := p.pool.Query(context.Background(), "SELECT id,repository_id,COALESCE(task_id,''),kind,content,source,score,metadata,embedding,created_at FROM memory_entries WHERE repository_id=$1 ORDER BY created_at DESC", repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.MemoryEntry{}
	for rows.Next() {
		var m domain.MemoryEntry
		var metadata, embedding []byte
		if err := rows.Scan(&m.ID, &m.RepositoryID, &m.TaskID, &m.Kind, &m.Content, &m.Source, &m.Score, &metadata, &embedding, &m.CreatedAt); err != nil {
			return nil, err
		}
		if len(metadata) > 0 {
			if err := json.Unmarshal(metadata, &m.Metadata); err != nil {
				return nil, err
			}
		}
		if len(embedding) > 0 {
			if err := json.Unmarshal(embedding, &m.Embedding); err != nil {
				return nil, err
			}
		}
		if query != "" {
			m.Score = memorySearchScore(m.Content, query, m.Embedding)
			if m.Score == 0 {
				continue
			}
		}
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, rows.Err()
}
func nullTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}
