package store

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"codecodriver/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Postgres struct {
	pool       *pgxpool.Pool
	embeddings EmbeddingProvider
}

var _ Store = (*Postgres)(nil)

func OpenPostgres(ctx context.Context, databaseURL string) (*Postgres, error) {
	return OpenPostgresWithEmbedding(ctx, databaseURL, localEmbeddingProvider{})
}

func OpenPostgresWithEmbedding(ctx context.Context, databaseURL string, provider EmbeddingProvider) (*Postgres, error) {
	if provider == nil {
		provider = localEmbeddingProvider{}
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	p := &Postgres{pool: pool, embeddings: provider}
	for _, name := range []string{"001_initial.sql", "002_fencing_token.sql", "003_embedding_vector.sql", "004_memory_rich_fields.sql"} {
		sql, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			pool.Close()
			return nil, fmt.Errorf("read migration %s: %w", name, err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			pool.Close()
			return nil, fmt.Errorf("apply migration %s: %w", name, err)
		}
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
	_, err := p.pool.Exec(context.Background(), "INSERT INTO repositories(id,name,path,test_command,primary_language,file_count,indexed_at,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", r.ID, r.Name, r.Path, r.TestCommand, r.PrimaryLanguage, r.FileCount, nullTime(r.IndexedAt), r.CreatedAt)
	return err
}
func (p *Postgres) Repository(id string) (domain.Repository, error) {
	var r domain.Repository
	var indexed *time.Time
	err := p.pool.QueryRow(context.Background(), "SELECT id,name,path,test_command,primary_language,file_count,indexed_at,created_at FROM repositories WHERE id=$1", id).Scan(&r.ID, &r.Name, &r.Path, &r.TestCommand, &r.PrimaryLanguage, &r.FileCount, &indexed, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	if indexed != nil {
		r.IndexedAt = *indexed
	}
	return r, err
}
func (p *Postgres) Repositories() ([]domain.Repository, error) {
	rows, err := p.pool.Query(context.Background(), "SELECT id,name,path,test_command,primary_language,file_count,indexed_at,created_at FROM repositories ORDER BY created_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Repository{}
	for rows.Next() {
		var r domain.Repository
		var indexed *time.Time
		if err := rows.Scan(&r.ID, &r.Name, &r.Path, &r.TestCommand, &r.PrimaryLanguage, &r.FileCount, &indexed, &r.CreatedAt); err != nil {
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
func (p *Postgres) UpdateTaskForRun(id, runID string, token int64, status domain.TaskStatus, errorText string) error {
	result, err := p.pool.Exec(context.Background(), "UPDATE tasks SET status=$2,error=$3,updated_at=NOW() WHERE id=$1 AND EXISTS (SELECT 1 FROM task_runs WHERE task_id=$1 AND id=$4 AND fencing_token=$5)", id, status, errorText, runID, token)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrStaleRun
	}
	return nil
}

func (p *Postgres) AddRun(r domain.TaskRun) error {
	_, err := p.pool.Exec(context.Background(), "INSERT INTO task_runs(id,task_id,status,fencing_token,started_at,ended_at) VALUES($1,$2,$3,$4,$5,$6)", r.ID, r.TaskID, r.Status, r.FencingToken, r.StartedAt, nullTime(r.EndedAt))
	return err
}
func (p *Postgres) FinishRun(taskID, runID string, status domain.TaskStatus) error {
	_, err := p.pool.Exec(context.Background(), "UPDATE task_runs SET status=$3,ended_at=NOW() WHERE task_id=$1 AND id=$2", taskID, runID, status)
	return err
}
func (p *Postgres) FinishRunWithToken(taskID, runID string, status domain.TaskStatus, token int64) error {
	result, err := p.pool.Exec(context.Background(), "UPDATE task_runs SET status=$3,ended_at=NOW() WHERE task_id=$1 AND id=$2 AND fencing_token=$4", taskID, runID, status, token)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrStaleRun
	}
	return nil
}
func (p *Postgres) Runs(taskID string) ([]domain.TaskRun, error) {
	rows, err := p.pool.Query(context.Background(), "SELECT id,task_id,status,fencing_token,started_at,ended_at FROM task_runs WHERE task_id=$1 ORDER BY started_at", taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.TaskRun{}
	for rows.Next() {
		var r domain.TaskRun
		var ended *time.Time
		if err := rows.Scan(&r.ID, &r.TaskID, &r.Status, &r.FencingToken, &r.StartedAt, &ended); err != nil {
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

func (p *Postgres) AddToolCall(call domain.ToolCall) error {
	request, err := json.Marshal(call.RequestPayload)
	if err != nil {
		return err
	}
	response, err := json.Marshal(call.ResponsePayload)
	if err != nil {
		return err
	}
	_, err = p.pool.Exec(context.Background(), "INSERT INTO tool_calls(id,task_id,run_id,step_id,tool_name,provider_type,request_payload,response_payload,status,error,started_at,ended_at,latency_ms) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)", call.ID, call.TaskID, call.RunID, call.StepID, call.ToolName, call.ProviderType, request, response, call.Status, call.Error, call.StartedAt, nullTime(call.EndedAt), call.LatencyMS)
	return err
}
func (p *Postgres) ToolCalls(taskID string) ([]domain.ToolCall, error) {
	rows, err := p.pool.Query(context.Background(), "SELECT id,task_id,run_id,step_id,tool_name,provider_type,request_payload,response_payload,status,error,started_at,ended_at,latency_ms FROM tool_calls WHERE task_id=$1 ORDER BY started_at,id", taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ToolCall{}
	for rows.Next() {
		var call domain.ToolCall
		var request, response []byte
		var ended *time.Time
		if err := rows.Scan(&call.ID, &call.TaskID, &call.RunID, &call.StepID, &call.ToolName, &call.ProviderType, &request, &response, &call.Status, &call.Error, &call.StartedAt, &ended, &call.LatencyMS); err != nil {
			return nil, err
		}
		if ended != nil {
			call.EndedAt = *ended
		}
		if len(request) > 0 {
			if err := json.Unmarshal(request, &call.RequestPayload); err != nil {
				return nil, err
			}
		}
		if len(response) > 0 {
			if err := json.Unmarshal(response, &call.ResponsePayload); err != nil {
				return nil, err
			}
		}
		out = append(out, call)
	}
	return out, rows.Err()
}

func (p *Postgres) AddLLMUsage(usage domain.LLMUsage) error {
	_, err := p.pool.Exec(context.Background(), "INSERT INTO llm_usages(id,task_id,run_id,step_id,agent_name,model,prompt_tokens,completion_tokens,total_tokens,estimated_cost_usd,latency_ms,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)", usage.ID, usage.TaskID, usage.RunID, usage.StepID, usage.AgentName, usage.Model, usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, usage.EstimatedCostUSD, usage.LatencyMS, usage.CreatedAt)
	return err
}
func (p *Postgres) LLMUsages(taskID string) ([]domain.LLMUsage, error) {
	rows, err := p.pool.Query(context.Background(), "SELECT id,task_id,run_id,step_id,agent_name,model,prompt_tokens,completion_tokens,total_tokens,estimated_cost_usd,latency_ms,created_at FROM llm_usages WHERE task_id=$1 ORDER BY created_at,id", taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.LLMUsage{}
	for rows.Next() {
		var usage domain.LLMUsage
		if err := rows.Scan(&usage.ID, &usage.TaskID, &usage.RunID, &usage.StepID, &usage.AgentName, &usage.Model, &usage.PromptTokens, &usage.CompletionTokens, &usage.TotalTokens, &usage.EstimatedCostUSD, &usage.LatencyMS, &usage.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, usage)
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
		vectors, err := p.embeddings.Embed(context.Background(), []string{m.Content})
		if err != nil {
			log.Printf("memory embedding provider %s failed, using local fallback: %v", p.embeddings.Name(), err)
			m.Embedding = textEmbedding(m.Content)
		} else if len(vectors) > 0 {
			m.Embedding = vectors[0]
		} else {
			m.Embedding = textEmbedding(m.Content)
		}
	}
	embedding, err := json.Marshal(m.Embedding)
	if err != nil {
		return err
	}
	if m.ChangedFiles == nil {
		m.ChangedFiles = []string{}
	}
	if m.Symbols == nil {
		m.Symbols = []string{}
	}
	var halfvec any
	if len(m.Embedding) == doubaoEmbeddingDimensions {
		halfvec = vectorLiteral(m.Embedding)
	}
	_, err = p.pool.Exec(context.Background(), "INSERT INTO memory_entries(id,repository_id,task_id,kind,content,title,summary,symptom,root_cause,changed_files,symbols,test_command,verification_evidence,success_score,source_run_id,source,score,metadata,embedding,embedding_halfvec,last_accessed_at,access_count,created_at) VALUES($1,$2,NULLIF($3,''),$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)", m.ID, m.RepositoryID, m.TaskID, m.Kind, m.Content, m.Title, m.Summary, m.Symptom, m.RootCause, m.ChangedFiles, m.Symbols, m.TestCommand, m.VerificationEvidence, m.SuccessScore, m.SourceRunID, m.Source, m.Score, metadata, embedding, halfvec, nullTime(m.LastAccessedAt), m.AccessCount, m.CreatedAt)
	return err
}
func (p *Postgres) SearchMemory(repoID, query string) ([]domain.MemoryEntry, error) {
	return p.SearchMemoryLimit(repoID, query, 20)
}

func (p *Postgres) SearchMemoryLimit(repoID, query string, limit int) ([]domain.MemoryEntry, error) {
	if limit <= 0 {
		limit = 20
	}
	q := strings.ToLower(strings.TrimSpace(query))
	queryEmbedding := textEmbedding(q)
	if p.embeddings != nil && q != "" {
		vectors, err := p.embeddings.Embed(context.Background(), []string{q})
		if err != nil {
			log.Printf("memory embedding provider %s failed during search, using local fallback: %v", p.embeddings.Name(), err)
		} else if len(vectors) > 0 {
			queryEmbedding = vectors[0]
		}
	}
	if q != "" && len(queryEmbedding) == doubaoEmbeddingDimensions {
		out, err := p.searchMemoryHybrid(repoID, q, queryEmbedding, limit)
		if err == nil {
			if len(out) > 0 {
				return p.finalizeMemorySearch(out, limit)
			}
		} else {
			log.Printf("pgvector memory search failed, falling back to full scan: %v", err)
		}
	}
	out, err := p.searchMemoryScan(repoID, q, queryEmbedding, limit)
	if err != nil {
		return nil, err
	}
	return p.finalizeMemorySearch(out, limit)
}

const memorySelectColumns = "id,repository_id,COALESCE(task_id,''),kind,content,title,summary,symptom,root_cause,changed_files,symbols,test_command,verification_evidence,success_score,source_run_id,source,score,metadata,embedding,last_accessed_at,access_count,created_at"

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMemoryEntry(row rowScanner) (domain.MemoryEntry, error) {
	var m domain.MemoryEntry
	var metadata, embedding []byte
	var lastAccessed *time.Time
	if err := row.Scan(&m.ID, &m.RepositoryID, &m.TaskID, &m.Kind, &m.Content, &m.Title, &m.Summary, &m.Symptom, &m.RootCause, &m.ChangedFiles, &m.Symbols, &m.TestCommand, &m.VerificationEvidence, &m.SuccessScore, &m.SourceRunID, &m.Source, &m.Score, &metadata, &embedding, &lastAccessed, &m.AccessCount, &m.CreatedAt); err != nil {
		return m, err
	}
	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &m.Metadata); err != nil {
			return m, err
		}
	}
	if len(embedding) > 0 {
		if err := json.Unmarshal(embedding, &m.Embedding); err != nil {
			return m, err
		}
	}
	if lastAccessed != nil {
		m.LastAccessedAt = *lastAccessed
	}
	return m, nil
}

func (p *Postgres) searchMemoryScan(repoID, query string, queryEmbedding []float64, limit int) ([]domain.MemoryEntry, error) {
	rows, err := p.pool.Query(context.Background(), "SELECT "+memorySelectColumns+" FROM memory_entries WHERE repository_id=$1 ORDER BY created_at DESC", repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.MemoryEntry{}
	for rows.Next() {
		m, err := scanMemoryEntry(rows)
		if err != nil {
			return nil, err
		}
		if query != "" {
			m.Score = memoryRerankScore(m, query, queryEmbedding, time.Now().UTC())
			if m.Score == 0 {
				continue
			}
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (p *Postgres) searchMemoryHybrid(repoID, query string, queryEmbedding []float64, limit int) ([]domain.MemoryEntry, error) {
	candidates := map[string]domain.MemoryEntry{}
	now := time.Now().UTC()
	vectorLimit := limit * 2
	rows, err := p.pool.Query(context.Background(), "SELECT "+memorySelectColumns+" FROM memory_entries WHERE repository_id=$1 AND embedding_halfvec IS NOT NULL ORDER BY embedding_halfvec <=> $2::halfvec LIMIT $3", repoID, vectorLiteral(queryEmbedding), vectorLimit)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		m, err := scanMemoryEntry(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		m.Score = memoryRerankScore(m, query, queryEmbedding, now)
		if m.Score > 0 {
			candidates[m.ID] = m
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	patterns := keywordPatterns(query)
	rows, err = p.pool.Query(context.Background(), "SELECT "+memorySelectColumns+" FROM memory_entries WHERE repository_id=$1 AND content ILIKE ANY($2) ORDER BY score DESC, created_at DESC LIMIT $3", repoID, patterns, vectorLimit)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		m, err := scanMemoryEntry(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		m.Score = memoryRerankScore(m, query, queryEmbedding, now)
		if m.Score == 0 {
			continue
		}
		if existing, ok := candidates[m.ID]; !ok || m.Score > existing.Score {
			candidates[m.ID] = m
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	out := make([]domain.MemoryEntry, 0, len(candidates))
	for _, item := range candidates {
		out = append(out, item)
	}
	return out, nil
}

func (p *Postgres) finalizeMemorySearch(out []domain.MemoryEntry, limit int) ([]domain.MemoryEntry, error) {
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > limit {
		out = out[:limit]
	}
	ids := make([]string, 0, len(out))
	for _, item := range out {
		ids = append(ids, item.ID)
	}
	if err := p.RecordMemoryAccess(ids); err != nil {
		return nil, err
	}
	return out, nil
}

func keywordPatterns(query string) []string {
	terms := strings.Fields(query)
	patterns := make([]string, 0, len(terms))
	for _, term := range terms {
		if len(term) >= 3 {
			patterns = append(patterns, "%"+term+"%")
		}
	}
	return patterns
}

func (p *Postgres) RecordMemoryAccess(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := p.pool.Exec(context.Background(), "UPDATE memory_entries SET access_count=access_count+1,last_accessed_at=NOW() WHERE id = ANY($1)", ids)
	return err
}

func (p *Postgres) AddBenchmarkCase(item domain.BenchmarkCase) error {
	expected, err := json.Marshal(item.Expected)
	if err != nil {
		return err
	}
	_, err = p.pool.Exec(context.Background(), "INSERT INTO benchmark_cases(id,name,repository_id,title,description,expected,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)", item.ID, item.Name, item.RepositoryID, item.Title, item.Description, expected, item.CreatedAt)
	return err
}
func (p *Postgres) UpdateBenchmarkCase(item domain.BenchmarkCase) error {
	expected, err := json.Marshal(item.Expected)
	if err != nil {
		return err
	}
	result, err := p.pool.Exec(context.Background(), "UPDATE benchmark_cases SET name=$2,repository_id=$3,title=$4,description=$5,expected=$6 WHERE id=$1", item.ID, item.Name, item.RepositoryID, item.Title, item.Description, expected)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
func (p *Postgres) BenchmarkCases() ([]domain.BenchmarkCase, error) {
	rows, err := p.pool.Query(context.Background(), "SELECT id,name,repository_id,title,description,expected,created_at FROM benchmark_cases ORDER BY created_at,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.BenchmarkCase{}
	for rows.Next() {
		var item domain.BenchmarkCase
		var expected []byte
		if err := rows.Scan(&item.ID, &item.Name, &item.RepositoryID, &item.Title, &item.Description, &expected, &item.CreatedAt); err != nil {
			return nil, err
		}
		if len(expected) > 0 {
			if err := json.Unmarshal(expected, &item.Expected); err != nil {
				return nil, err
			}
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
func (p *Postgres) BenchmarkCase(id string) (domain.BenchmarkCase, error) {
	var item domain.BenchmarkCase
	var expected []byte
	err := p.pool.QueryRow(context.Background(), "SELECT id,name,repository_id,title,description,expected,created_at FROM benchmark_cases WHERE id=$1", id).Scan(&item.ID, &item.Name, &item.RepositoryID, &item.Title, &item.Description, &expected, &item.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return item, ErrNotFound
	}
	if err != nil {
		return item, err
	}
	if len(expected) > 0 {
		if err := json.Unmarshal(expected, &item.Expected); err != nil {
			return item, err
		}
	}
	return item, nil
}
func (p *Postgres) AddEvaluationRun(run domain.EvaluationRun) error {
	_, err := p.pool.Exec(context.Background(), "INSERT INTO evaluation_runs(id,case_id,batch_id,task_id,mode,status,passed,duration_ms,notes,started_at,ended_at,created_at) VALUES($1,$2,NULLIF($3,''),NULLIF($4,''),$5,$6,$7,$8,$9,$10,$11,$12)", run.ID, run.CaseID, run.BatchID, run.TaskID, run.Mode, run.Status, run.Passed, run.DurationMS, run.Notes, run.StartedAt, nullTime(run.EndedAt), run.CreatedAt)
	return err
}
func (p *Postgres) UpdateEvaluationRun(run domain.EvaluationRun) error {
	result, err := p.pool.Exec(context.Background(), "UPDATE evaluation_runs SET batch_id=NULLIF($2,''),task_id=NULLIF($3,''),mode=$4,status=$5,passed=$6,duration_ms=$7,notes=$8,started_at=$9,ended_at=$10 WHERE id=$1", run.ID, run.BatchID, run.TaskID, run.Mode, run.Status, run.Passed, run.DurationMS, run.Notes, run.StartedAt, nullTime(run.EndedAt))
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
func (p *Postgres) EvaluationRuns(caseID string) ([]domain.EvaluationRun, error) {
	rows, err := p.pool.Query(context.Background(), "SELECT id,case_id,COALESCE(batch_id,''),COALESCE(task_id,''),mode,status,passed,duration_ms,notes,started_at,ended_at,created_at FROM evaluation_runs WHERE case_id=$1 ORDER BY created_at,id", caseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.EvaluationRun{}
	for rows.Next() {
		var run domain.EvaluationRun
		var ended *time.Time
		if err := rows.Scan(&run.ID, &run.CaseID, &run.BatchID, &run.TaskID, &run.Mode, &run.Status, &run.Passed, &run.DurationMS, &run.Notes, &run.StartedAt, &ended, &run.CreatedAt); err != nil {
			return nil, err
		}
		if ended != nil {
			run.EndedAt = *ended
		}
		out = append(out, run)
	}
	return out, rows.Err()
}
func (p *Postgres) AllEvaluationRuns() ([]domain.EvaluationRun, error) {
	rows, err := p.pool.Query(context.Background(), "SELECT id,case_id,COALESCE(batch_id,''),COALESCE(task_id,''),mode,status,passed,duration_ms,notes,started_at,ended_at,created_at FROM evaluation_runs ORDER BY created_at,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.EvaluationRun{}
	for rows.Next() {
		var run domain.EvaluationRun
		var ended *time.Time
		if err := rows.Scan(&run.ID, &run.CaseID, &run.BatchID, &run.TaskID, &run.Mode, &run.Status, &run.Passed, &run.DurationMS, &run.Notes, &run.StartedAt, &ended, &run.CreatedAt); err != nil {
			return nil, err
		}
		if ended != nil {
			run.EndedAt = *ended
		}
		out = append(out, run)
	}
	return out, rows.Err()
}
func (p *Postgres) AddEvaluationBatch(batch domain.EvaluationBatch) error {
	_, err := p.pool.Exec(context.Background(), "INSERT INTO evaluation_batches(id,name,mode,status,total,completed,passed,started_at,ended_at,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)", batch.ID, batch.Name, batch.Mode, batch.Status, batch.Total, batch.Completed, batch.Passed, batch.StartedAt, nullTime(batch.EndedAt), batch.CreatedAt)
	return err
}
func (p *Postgres) UpdateEvaluationBatch(batch domain.EvaluationBatch) error {
	result, err := p.pool.Exec(context.Background(), "UPDATE evaluation_batches SET name=$2,mode=$3,status=$4,total=$5,completed=$6,passed=$7,started_at=$8,ended_at=$9 WHERE id=$1", batch.ID, batch.Name, batch.Mode, batch.Status, batch.Total, batch.Completed, batch.Passed, batch.StartedAt, nullTime(batch.EndedAt))
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
func (p *Postgres) EvaluationBatches() ([]domain.EvaluationBatch, error) {
	rows, err := p.pool.Query(context.Background(), "SELECT id,name,mode,status,total,completed,passed,started_at,ended_at,created_at FROM evaluation_batches ORDER BY created_at,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.EvaluationBatch{}
	for rows.Next() {
		var batch domain.EvaluationBatch
		var ended *time.Time
		if err := rows.Scan(&batch.ID, &batch.Name, &batch.Mode, &batch.Status, &batch.Total, &batch.Completed, &batch.Passed, &batch.StartedAt, &ended, &batch.CreatedAt); err != nil {
			return nil, err
		}
		if ended != nil {
			batch.EndedAt = *ended
		}
		out = append(out, batch)
	}
	return out, rows.Err()
}
func (p *Postgres) AddEvaluationMetricSnapshot(snapshot domain.EvaluationMetricSnapshot) error {
	_, err := p.pool.Exec(context.Background(), "INSERT INTO evaluation_metric_snapshots(id,batch_id,mode,total,passed,pass_rate,avg_duration_ms,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(batch_id) DO UPDATE SET mode=EXCLUDED.mode,total=EXCLUDED.total,passed=EXCLUDED.passed,pass_rate=EXCLUDED.pass_rate,avg_duration_ms=EXCLUDED.avg_duration_ms,created_at=EXCLUDED.created_at", snapshot.ID, snapshot.BatchID, snapshot.Mode, snapshot.Total, snapshot.Passed, snapshot.PassRate, snapshot.AvgDurationMS, snapshot.CreatedAt)
	return err
}
func (p *Postgres) EvaluationMetricSnapshots() ([]domain.EvaluationMetricSnapshot, error) {
	rows, err := p.pool.Query(context.Background(), "SELECT id,batch_id,mode,total,passed,pass_rate,avg_duration_ms,created_at FROM evaluation_metric_snapshots ORDER BY created_at,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.EvaluationMetricSnapshot{}
	for rows.Next() {
		var snapshot domain.EvaluationMetricSnapshot
		if err := rows.Scan(&snapshot.ID, &snapshot.BatchID, &snapshot.Mode, &snapshot.Total, &snapshot.Passed, &snapshot.PassRate, &snapshot.AvgDurationMS, &snapshot.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, snapshot)
	}
	return out, rows.Err()
}
func nullTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}
