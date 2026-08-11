import { useEffect, useState } from 'react'
import { createRoot } from 'react-dom/client'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import './styles.css'

type Task = { id: string; repository_id: string; title: string; description: string; status: string; error?: string; updated_at: string }
type Repository = { id: string; name: string; path: string; test_command?: string; file_count: number; indexed_at?: string }
type Skill = { name: string; description?: string; keywords?: string[]; path_patterns?: string[]; workflow?: string; prompts?: Record<string, { system?: string; user?: string }>; allowed_tools?: string[] }
type Overview = { repositories: number; tasks: number; completed: number; failed: number; human_review: number; active: number; average_run_latency_ms: number; status_counts: Record<string, number> }
type TimelineEvent = { id: string; type: string; label: string; status?: string; error?: string; started_at: string; ended_at?: string; latency_ms?: number; payload?: unknown }
type MemoryLink = { id: string; target_type: string; target_id: string; label?: string }
type Memory = { id: string; kind: string; content: string; title?: string; summary?: string; symptom?: string; root_cause?: string; condition?: string; duplicate_of?: string; conflict_group_id?: string; changed_files?: string[]; symbols?: string[]; source?: string; score?: number; access_count?: number; links?: MemoryLink[]; created_at: string }
type Evaluation = { cases: { id: string; name: string; title: string; description: string }[]; runs: { id: string; case_id: string; batch_id?: string; mode: string; status: string; passed: boolean; duration_ms: number; memory_hits?: number; repair_attempts?: number; created_at: string; notes?: string }[]; batches: { id: string; name: string; mode: string; status: string; total: number; completed: number; passed: number; created_at: string }[]; history: { id: string; batch_id: string; mode: string; total: number; passed: number; pass_rate: number; avg_duration_ms: number; created_at: string }[]; metrics: { total: number; passed: number; human_review?: number; failed?: number; auto_human?: number; pass_rate: number; by_mode: Record<string, { total: number; passed: number; human_review?: number; failed?: number; auto_human?: number }>; by_case: Record<string, Record<string, { total: number; passed: number; human_review?: number; failed?: number; auto_human?: number }>>; by_memory?: Record<string, { total: number; passed: number; human_review?: number; failed?: number; auto_human?: number; avg_duration_ms: number; memory_hits: number; repair_attempts: number }>; by_category?: Record<string, { total: number; passed: number; human_review?: number; failed?: number; auto_human?: number }> } }
type EvalAgentUsage = { calls: number; steps: number; tool_calls: number; tool_errors: number; prompt_tokens: number; completion_tokens: number; total_tokens: number; estimated_cost_usd: number; latency_ms: number; avg_latency_ms: number }
type EvalToolUsage = { calls: number; errors: number; latency_ms: number; avg_latency_ms: number }
type EvalDimension = { score: number; max: number; label: string; details?: Record<string, unknown> }
type EvalTraceEvent = { id: string; type: string; agent?: string; phase?: string; attempt?: number; status?: string; label?: string; latency_ms?: number; total_tokens?: number; estimated_cost_usd?: number; summary?: string }
type EvalRunReport = { run_id: string; task_id: string; case_id: string; case_name: string; category: string; status: string; passed: boolean; created_at: string; duration_ms: number; quality_score: number; token_usage: { prompt_tokens: number; completion_tokens: number; total_tokens: number; estimated_cost_usd: number }; agents: Record<string, EvalAgentUsage>; tool_usage: Record<string, EvalToolUsage>; dimensions: Record<string, EvalDimension>; trace: { phases: Record<string, { calls: number; tokens: number; tool_calls: number; tool_errors: number; latency_ms: number; llm_calls: number }>; events: EvalTraceEvent[] }; repair_attempts: number }
type EvalReport = { summary: { total_runs: number; passed: number; failed: number; avg_quality: number; avg_tokens: number; avg_cost_usd: number; avg_duration_ms: number }; agent_stats: Record<string, EvalAgentUsage>; tool_stats: Record<string, EvalToolUsage>; runs: EvalRunReport[] }

const statusTone: Record<string, string> = { COMPLETED: 'success', FAILED: 'danger', HUMAN_REVIEW_REQUIRED: 'warning', CANCELLED: 'muted', CREATED: 'neutral' }

async function get<T>(path: string): Promise<T> {
  const response = await fetch(path)
  if (!response.ok) throw new Error(`${response.status} ${response.statusText}`)
  return response.json()
}

async function post<T>(path: string, body: unknown): Promise<T> {
  const response = await fetch(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
  if (!response.ok) throw new Error(`${response.status} ${response.statusText}`)
  return response.json()
}

function formatDuration(ms: number) {
  if (!ms) return '—'
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(1)}s`
}

function formatDate(value: string) { return new Date(value).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) }

function App() {
  const [view, setView] = useState('overview')
  const [overview, setOverview] = useState<Overview | null>(null)
  const [tasks, setTasks] = useState<Task[]>([])
  const [repositories, setRepositories] = useState<Repository[]>([])
  const [skills, setSkills] = useState<Skill[]>([])
  const [taskSkill, setTaskSkill] = useState('')
  const [skillImportUrl, setSkillImportUrl] = useState('')
  const [selected, setSelected] = useState<Task | null>(null)
  const [timeline, setTimeline] = useState<TimelineEvent[]>([])
  const [memories, setMemories] = useState<Memory[]>([])
  const [evaluation, setEvaluation] = useState<Evaluation | null>(null)
  const [evalReport, setEvalReport] = useState<EvalReport | null>(null)
  const [evaluationMode, setEvaluationMode] = useState('agent')
  const [memoryQuery, setMemoryQuery] = useState('')
  const [memoryRepo, setMemoryRepo] = useState('')
  const [repoName, setRepoName] = useState('')
  const [repoPath, setRepoPath] = useState('')
  const [taskRepo, setTaskRepo] = useState('')
  const [taskTitle, setTaskTitle] = useState('')
  const [taskDescription, setTaskDescription] = useState('')
  const [reviewReason, setReviewReason] = useState('')
  const [applyResult, setApplyResult] = useState<{ taskId: string; status: string; warnings: string[] } | null>(null)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  const refresh = async () => {
    try {
      const [summary, taskData, repoData] = await Promise.all([
        get<Overview>('/dashboard/overview'),
        get<Task[]>('/tasks'),
        get<Repository[]>('/repositories')
      ])
      const skillData = await get<Skill[]>('/skills').catch(() => [] as Skill[])
      setOverview(summary)
      setTasks(taskData.sort((a, b) => b.updated_at.localeCompare(a.updated_at)))
      setRepositories(repoData)
      setSkills(skillData)
      if (taskSkill && !skillData.some(skill => skill.name === taskSkill)) setTaskSkill('')
      if (!taskRepo && repoData.length) setTaskRepo(repoData[0].id)
      if (repoData.length && !repoData.some(repo => repo.id === memoryRepo)) setMemoryRepo(repoData[0].id)
      setError('')
    } catch (err) { setError(err instanceof Error ? err.message : 'Unable to load dashboard') }
  }

  useEffect(() => { void refresh(); const timer = window.setInterval(() => void refresh(), 10000); return () => window.clearInterval(timer) }, [])
  useEffect(() => { if (view === 'evaluation') void loadEvaluation() }, [view])

  const loadEvaluation = async () => {
    try {
      const data = await get<Evaluation>('/evaluations')
      setEvaluation(data)
      const latest = data.batches[data.batches.length - 1]
      setEvalReport(latest ? await get<EvalReport>(`/evaluations/report?batch_id=${latest.id}`) : null)
      setError('')
    } catch (err) { setError(err instanceof Error ? err.message : 'Unable to load evaluations') }
  }

  const openTask = async (task: Task) => {
    setSelected(task); setView('tasks'); setApplyResult(null)
    try {
      const result = await get<{ events: TimelineEvent[]; applied?: Record<string, unknown> }>(`/tasks/${task.id}/timeline`)
      setTimeline([...result.events].sort((a, b) => b.started_at.localeCompare(a.started_at)))
      const applied = result.applied
      if (applied && typeof applied.status === 'string') {
        setApplyResult({
          taskId: task.id,
          status: applied.status,
          warnings: Array.isArray(applied.warnings) ? applied.warnings.map(String) : []
        })
      }
    } catch (err) { setError(err instanceof Error ? err.message : 'Unable to load timeline') }
  }

  const searchMemory = async () => {
    if (!memoryRepo) return
    try { const result = await get<Memory[]>(`/memory/search?repository_id=${encodeURIComponent(memoryRepo)}&query=${encodeURIComponent(memoryQuery)}`); setMemories(result) } catch (err) { setError(err instanceof Error ? err.message : 'Unable to search memory') }
  }

  const runSuite = async () => {
    if (!evaluation?.cases.length) return
    try {
      await post('/evaluations/suites', { name: `${evaluationMode} suite`, mode: evaluationMode, case_ids: evaluation.cases.map(item => item.id) })
      await loadEvaluation()
    } catch (err) { setError(err instanceof Error ? err.message : 'Unable to start evaluation suite') }
  }

  const registerRepository = async () => {
    try {
      await post('/repositories', { name: repoName, path: repoPath })
      setRepoName(''); setRepoPath('')
      await refresh()
    } catch (err) { setError(err instanceof Error ? err.message : 'Unable to register repository') }
  }

  const createTask = async () => {
    if (!taskRepo || !taskDescription.trim()) return
    try {
      const task = await post<Task>('/tasks', { repository_id: taskRepo, title: taskTitle, description: taskDescription, skill_name: taskSkill })
      setTaskTitle(''); setTaskDescription('')
      await refresh()
      await openTask(task)
    } catch (err) { setError(err instanceof Error ? err.message : 'Unable to create task') }
  }

  const importSkillFromGitHub = async () => {
    if (!skillImportUrl.trim()) return
    try {
      const result = await post<{ imported: { name: string }[] }>('/skills/import', { url: skillImportUrl })
      setSkillImportUrl('')
      setError('')
      setNotice(`Imported: ${result.imported.map(skill => skill.name).join(', ')}`)
      await refresh()
    } catch (err) { setError(err instanceof Error ? err.message : 'Unable to import skill'); setNotice('') }
  }

  const reloadSkills = async () => {
    try {
      await post('/skills/reload', {})
      setNotice('Skills reloaded from folder')
      await refresh()
    } catch (err) { setError(err instanceof Error ? err.message : 'Unable to reload skills'); setNotice('') }
  }

  const applyPatch = async (task: Task) => {
    try {
      const result = await post<{ status: string; warnings?: string[] }>(`/tasks/${task.id}/apply`, {})
      await refresh()
      await openTask(task)
      setApplyResult({ taskId: task.id, status: result.status, warnings: result.warnings ?? [] })
      setError('')
    } catch (err) { setError(err instanceof Error ? err.message : 'Unable to apply patch') }
  }

  const rerunTask = async (task: Task) => {
    try {
      const rerun = await post<Task>(`/tasks/${task.id}/rerun`, {})
      await refresh()
      await openTask(rerun)
    } catch (err) { setError(err instanceof Error ? err.message : 'Unable to rerun task') }
  }

  const resolveReview = async (task: Task, approve: boolean) => {
    try {
      const updated = await post<Task>(`/human-reviews/${task.id}/${approve ? 'approve' : 'reject'}`, { reason: reviewReason })
      setReviewReason('')
      await refresh()
      setSelected(updated)
    } catch (err) { setError(err instanceof Error ? err.message : 'Unable to resolve human review') }
  }

  const sendFeedback = async (task: Task) => {
    if (!reviewReason.trim()) return
    try {
      const updated = await post<Task>(`/human-reviews/${task.id}/feedback`, { feedback: reviewReason })
      setReviewReason('')
      setNotice('Feedback sent; task will continue in the Agent loop')
      await refresh()
      await openTask(updated)
    } catch (err) { setError(err instanceof Error ? err.message : 'Unable to send feedback'); setNotice('') }
  }

  return <div className="app-shell">
    <aside className="sidebar">
      <div className="brand"><span className="brand-mark">CC</span><div><strong>CodeCoDriver</strong><small>control room</small></div></div>
      <nav>{[['overview', 'Overview'], ['tasks', 'Task trace'], ['memory', 'Memory'], ['skills', 'Skills'], ['evaluation', 'Evaluation']].map(([key, label]) => <button className={view === key ? 'nav-item active' : 'nav-item'} onClick={() => setView(key)} key={key}><span className={`nav-dot ${key}`} />{label}</button>)}</nav>
      <div className="sidebar-foot"><span className="online-dot" /> Runtime online<div className="version">v0.1 · local execution</div></div>
    </aside>
    <main className="main-content">
      <header className="topbar"><div><p className="eyebrow">ENGINEERING AGENT RUNTIME</p><h1>{view === 'overview' ? 'Control room' : view === 'tasks' ? 'Task trace' : view === 'evaluation' ? 'Evaluation' : view === 'skills' ? 'Skill registry' : 'Memory inspector'}</h1></div><button className="refresh" onClick={() => void refresh()} aria-label="Refresh dashboard">↻ <span>Refresh</span></button></header>
      {error && <div className="error-banner">{error}</div>}
      {notice && <div className="notice-banner">{notice}</div>}
      {view === 'overview' && <OverviewView overview={overview} tasks={tasks} repositories={repositories} skills={skills} taskRepo={taskRepo} taskSkill={taskSkill} repoName={repoName} repoPath={repoPath} taskTitle={taskTitle} taskDescription={taskDescription} setTaskRepo={setTaskRepo} setTaskSkill={setTaskSkill} setRepoName={setRepoName} setRepoPath={setRepoPath} setTaskTitle={setTaskTitle} setTaskDescription={setTaskDescription} onTask={openTask} onCreateTask={() => void createTask()} onRegisterRepository={() => void registerRepository()} />}
      {view === 'tasks' && <TasksView tasks={tasks} selected={selected} timeline={timeline} reviewReason={reviewReason} setReviewReason={setReviewReason} onTask={openTask} onReview={(task, approve) => void resolveReview(task, approve)} onFeedback={sendFeedback} onApply={applyPatch} applyResult={applyResult} onRerun={rerunTask} />}
      {view === 'memory' && <MemoryView query={memoryQuery} repo={memoryRepo} setQuery={setMemoryQuery} setRepo={setMemoryRepo} onSearch={() => void searchMemory()} memories={memories} repositories={repositories} />}
      {view === 'skills' && <SkillsView skills={skills} importUrl={skillImportUrl} setImportUrl={setSkillImportUrl} onImport={() => void importSkillFromGitHub()} onReload={() => void reloadSkills()} />}
      {view === 'evaluation' && <><EvaluationView data={evaluation} report={evalReport} mode={evaluationMode} setMode={setEvaluationMode} onRun={() => void runSuite()} /><TracePanel report={evalReport} /></>}
    </main>
  </div>
}

function OverviewView({ overview, tasks, repositories, skills, taskRepo, taskSkill, repoName, repoPath, taskTitle, taskDescription, setTaskRepo, setTaskSkill, setRepoName, setRepoPath, setTaskTitle, setTaskDescription, onTask, onCreateTask, onRegisterRepository }: {
  overview: Overview | null; tasks: Task[]; repositories: Repository[]; skills: Skill[]; taskRepo: string; taskSkill: string; repoName: string; repoPath: string; taskTitle: string; taskDescription: string
  setTaskRepo: (value: string) => void; setTaskSkill: (value: string) => void; setRepoName: (value: string) => void; setRepoPath: (value: string) => void; setTaskTitle: (value: string) => void; setTaskDescription: (value: string) => void
  onTask: (task: Task) => void; onCreateTask: () => void; onRegisterRepository: () => void
}) {
  const cards = [['Active runs', overview?.active ?? 0, 'neutral'], ['Completed', overview?.completed ?? 0, 'success'], ['Human review', overview?.human_review ?? 0, 'warning'], ['Avg. runtime', formatDuration(overview?.average_run_latency_ms ?? 0), 'neutral']]
  return <><section className="panel create-panel"><div className="panel-head"><div><p className="eyebrow">TASK LAUNCH</p><h2>Create an engineering task</h2></div><span className="count-label">{repositories.length} repositories · {skills.length} skills</span></div><div className="form-grid"><label>Repository<select value={taskRepo} onChange={event => setTaskRepo(event.target.value)}>{repositories.map(repo => <option value={repo.id} key={repo.id}>{repo.name || repo.id}</option>)}{!repositories.length && <option value="">No repositories</option>}</select></label><label>Skill<select value={taskSkill} onChange={event => setTaskSkill(event.target.value)}><option value="">Auto route</option>{skills.map(skill => <option value={skill.name} key={skill.name}>{skill.name}</option>)}</select></label><label>Title<input value={taskTitle} onChange={event => setTaskTitle(event.target.value)} placeholder="Fix retry timeout" /></label><label>Description<input value={taskDescription} onChange={event => setTaskDescription(event.target.value)} placeholder="Describe the repository change" /></label><button className="primary-button" onClick={onCreateTask} disabled={!taskRepo || !taskDescription.trim()}>Create task</button></div><div className="form-grid"><label>Repository name<input value={repoName} onChange={event => setRepoName(event.target.value)} placeholder="sample-repo" /></label><label>Repository path<input value={repoPath} onChange={event => setRepoPath(event.target.value)} placeholder="D:\\repos\\sample" /></label><button className="primary-button" onClick={onRegisterRepository} disabled={!repoPath.trim()}>Register repo</button></div></section><section className="stat-grid">{cards.map(([label, value, tone]) => <div className="stat-card" key={label as string}><span className={`stat-icon ${tone}`}>●</span><div><small>{label}</small><strong>{value}</strong></div></div>)}</section><section className="content-grid"><div className="panel wide"><div className="panel-head"><div><p className="eyebrow">LIVE QUEUE</p><h2>Recent tasks</h2></div><span className="count-label">{tasks.length} total</span></div><TaskTable tasks={tasks.slice(0, 8)} onTask={onTask} /></div><div className="panel signal"><div className="panel-head"><div><p className="eyebrow">SYSTEM SIGNAL</p><h2>Run health</h2></div></div><div className="health-ring"><strong>{overview?.tasks ? Math.round(((overview.completed ?? 0) / overview.tasks) * 100) : 0}%</strong><span>completion</span></div><div className="health-row"><span>Repositories</span><b>{overview?.repositories ?? 0}</b></div><div className="health-row"><span>Skills</span><b>{skills.length}</b></div><div className="health-row"><span>Failed</span><b className="danger-text">{overview?.failed ?? 0}</b></div></div></section></>
}

function renderEventDetail(event: TimelineEvent) {
  const payload = event.payload as Record<string, unknown> | undefined
  if (!payload) return null
  if (event.type === 'artifact' && (payload['type'] === 'explanation' || String(payload['name'] || '').endsWith('.md'))) {
    const content = typeof payload['content'] === 'string' ? payload['content'] : ''
    return <details className="event-detail markdown-detail" open><summary>{String(payload['type'] || 'markdown')}</summary><MarkdownView value={content} /></details>
  }
  if (event.type === 'step' && event.label === 'explainer') {
    return null
  }
  if (event.type === 'step' && event.label === 'codebase' && payload['context_pack']) {
    return renderCodebaseDetail(payload)
  }
  if (event.type === 'artifact' && typeof payload['content'] === 'string') {
    const content = payload['content'] as string
    const parsed = parseJsonContent(content)
    if (parsed !== undefined) {
      return <details className="event-detail" open><summary>{String(payload['type'] || 'artifact')}</summary>{renderJsonTree(parsed)}</details>
    }
    return <details className="event-detail" open><summary>{String(payload['type'] || 'artifact')}</summary><LongText value={content} /></details>
  }
  return <details className="event-detail" open><summary>Output</summary>{renderJsonTree(payload)}</details>
}

function MarkdownView({ value }: { value: string }) {
  return <div className="markdown-body"><ReactMarkdown remarkPlugins={[remarkGfm]}>{value}</ReactMarkdown></div>
}

function renderCodebaseDetail(payload: Record<string, unknown>) {
  const pack = payload['context_pack'] as Record<string, unknown> | undefined
  if (!pack) return null
  const snippets = Array.isArray(pack['snippets']) ? pack['snippets'] as Record<string, unknown>[] : []
  return <div className="codebase-detail">
    <div className="codebase-meta">
      <span>files {String(payload['indexed_files'] ?? '')}</span>
      <span>symbols {String(payload['indexed_symbols'] ?? '')}</span>
      <span>memory hits {String(payload['memory_hits'] ?? 0)}</span>
      <span>selected {snippets.length}</span>
    </div>
    {snippets.length > 0 && <div className="codebase-snippets">{snippets.map((snippet, index) => {
      const path = typeof snippet['path'] === 'string' ? snippet['path'] : `file-${index}`
      const language = typeof snippet['language'] === 'string' ? snippet['language'] : ''
      const content = typeof snippet['content'] === 'string' ? snippet['content'] : ''
      return <details className="codebase-snippet" key={path}>
        <summary><span>{path}</span>{language && <i>{language}</i>}</summary>
        <pre>{previewCode(content)}</pre>
      </details>
    })}</div>}
  </div>
}

function previewCode(value: string, maxLines = 40): string {
  const lines = value.split('\n')
  if (lines.length <= maxLines) return value
  return `${lines.slice(0, maxLines).join('\n')}\n... [${lines.length - maxLines} more lines in full trace]`
}

function parseJsonContent(value: string): unknown {
  const trimmed = value.trim()
  const candidate = trimmed.startsWith('```') ? trimmed.replace(/^```(?:json)?\s*/i, '').replace(/```\s*$/, '').trim() : trimmed
  const objectStart = candidate.indexOf('{')
  const objectEnd = candidate.lastIndexOf('}')
  const arrayStart = candidate.indexOf('[')
  const arrayEnd = candidate.lastIndexOf(']')
  let json = candidate
  if (objectStart >= 0 && objectEnd > objectStart) json = candidate.slice(objectStart, objectEnd + 1)
  else if (arrayStart >= 0 && arrayEnd > arrayStart) json = candidate.slice(arrayStart, arrayEnd + 1)
  try {
    return JSON.parse(json)
  } catch {
    return undefined
  }
}

function renderJsonTree(value: unknown, depth = 0) {
  if (value === null) return <code className="json-null">null</code>
  if (value === undefined) return null
  if (Array.isArray(value)) {
    if (value.length === 0) return <code className="json-empty">[]</code>
    if (depth >= 3) return <details className="long-value"><summary>Array ({value.length} items)</summary><pre>{JSON.stringify(value, null, 2)}</pre></details>
    return <div className="json-list">{value.map((item, index) => <div className="json-item" key={index}><span className="json-index">{index}</span>{renderJsonTree(item, depth + 1)}</div>)}</div>
  }
  if (typeof value === 'object') {
    const record = value as Record<string, unknown>
    if (typeof record['content'] === 'string') {
      const path = typeof record['path'] === 'string' ? record['path'] : ''
      const language = typeof record['language'] === 'string' ? record['language'] : ''
      return <div className="json-snippet"><span className="json-key">{path || 'content'}</span>{language && <i>{language}</i>}<LongText value={record['content']} /></div>
    }
    const entries = Object.entries(record)
    if (entries.length === 0) return <code className="json-empty">{'{}'}</code>
    if (depth >= 3) return <details className="long-value"><summary>Object ({entries.length} keys)</summary><pre>{JSON.stringify(value, null, 2)}</pre></details>
    return <div className="json-object">{entries.map(([key, item]) => <div className="json-row" key={key}><span className="json-key">{key}</span>{renderJsonTree(item, depth + 1)}</div>)}</div>
  }
  if (typeof value === 'string') return <LongText value={value} json />
  return <code className="json-number">{String(value)}</code>
}

function LongText({ value, json = false }: { value: string; json?: boolean }) {
  if (value.length <= 2000) return json ? <code className="json-string">{value}</code> : <pre>{value}</pre>
  return <details className="long-value"><summary>{value.slice(0, 220)}...</summary><pre>{value}</pre></details>
}

type ChatMessage = { role: 'user' | 'assistant'; content: string }

function isChatTask(timeline: TimelineEvent[]) {
  return timeline.some(event => event.type === 'step' && event.label === 'explainer') ||
    timeline.some(event => event.type === 'artifact' && (event.payload as Record<string, unknown> | undefined)?.['type'] === 'explanation')
}

function buildChatMessages(task: Task, timeline: TimelineEvent[]) {
  const messages: ChatMessage[] = [{ role: 'user', content: task.description || task.title }]
  const seenAssistant = new Set<string>()
  const ordered = [...timeline].sort((a, b) => a.started_at.localeCompare(b.started_at))
  for (const event of ordered) {
    const payload = event.payload as Record<string, unknown> | undefined
    if (event.type === 'artifact' && payload) {
      const content = typeof payload['content'] === 'string' ? payload['content'] : ''
      if (payload['type'] === 'human_feedback') {
        const feedback = parseFeedback(content)
        if (feedback) messages.push({ role: 'user', content: feedback })
      }
      if (payload['type'] === 'explanation' && content) {
        if (!seenAssistant.has(content)) {
          seenAssistant.add(content)
          messages.push({ role: 'assistant', content })
        }
      }
    }
    if (event.type === 'step' && event.label === 'explainer' && typeof payload?.['explanation'] === 'string') {
      const content = payload['explanation'] as string
      if (!seenAssistant.has(content)) {
        seenAssistant.add(content)
        messages.push({ role: 'assistant', content })
      }
    }
  }
  return messages
}

function parseFeedback(content: string): string | null {
  try {
    const parsed = JSON.parse(content) as { feedback?: unknown }
    return typeof parsed.feedback === 'string' ? parsed.feedback : null
  } catch {
    return null
  }
}

function ChatThread({ task, timeline, inputValue, setInputValue, onSend }: {
  task: Task; timeline: TimelineEvent[]; inputValue: string; setInputValue: (value: string) => void; onSend: () => void
}) {
  if (!isChatTask(timeline)) return null
  const messages = buildChatMessages(task, timeline)
  return <section className="panel chat-thread">
    <div className="panel-head"><div><p className="eyebrow">EXPLAINER CHAT</p><h2>Conversation</h2></div><span className="count-label">{messages.length} messages</span></div>
    <div className="chat-messages">{messages.map((message, index) => <div className={`chat-message ${message.role}`} key={`${message.role}-${index}`}><div className="chat-avatar">{message.role === 'user' ? 'YOU' : 'AI'}</div><div className="chat-bubble"><MarkdownView value={message.content} /></div></div>)}</div>
    <div className="chat-input"><input value={inputValue} onChange={event => setInputValue(event.target.value)} onKeyDown={event => { if (event.key === 'Enter' && inputValue.trim()) onSend() }} placeholder="Ask a follow-up question..." /><button className="primary-button" onClick={onSend} disabled={!inputValue.trim()}>Ask follow-up</button></div>
  </section>
}

function truncateText(value: string): string {
  return value.length > 40000 ? `${value.slice(0, 40000)}\n...TRUNCATED` : value
}

function TaskTable({ tasks, onTask }: { tasks: Task[]; onTask: (task: Task) => void }) { return <div className="task-table"><div className="table-row table-header"><span>Task</span><span>Status</span><span>Updated</span></div>{tasks.map(task => <button className="table-row task-row" key={task.id} onClick={() => onTask(task)}><span><strong>{task.title || 'Untitled task'}</strong><small>{task.description}</small></span><span><i className={`status-pill ${statusTone[task.status] || 'neutral'}`}>{task.status.replace(/_/g, ' ')}</i></span><span className="date">{formatDate(task.updated_at)}</span></button>)}{tasks.length === 0 && <div className="empty">No tasks yet. Create one from the overview to see its execution trace.</div>}</div> }

function TasksView({ tasks, selected, timeline, reviewReason, setReviewReason, onTask, onReview, onFeedback, onApply, onRerun, applyResult }: {
  tasks: Task[]; selected: Task | null; timeline: TimelineEvent[]; reviewReason: string; setReviewReason: (value: string) => void
  onTask: (task: Task) => void; onReview: (task: Task, approve: boolean) => void; onFeedback: (task: Task) => void; onApply: (task: Task) => void; onRerun: (task: Task) => void; applyResult: { taskId: string; status: string; warnings: string[] } | null
}) {
  const isSkipProposal = selected?.status === 'HUMAN_REVIEW_REQUIRED' && selected.error?.toLowerCase().startsWith('planner suggested skip')
  const chatTask = isChatTask(timeline)
  return <section className="task-layout">
    <div className="panel task-list">
      <div className="panel-head"><div><p className="eyebrow">EXECUTION HISTORY</p><h2>All tasks</h2></div></div>
      <TaskTable tasks={tasks} onTask={onTask} />
    </div>
    <div className="panel trace-panel">
      <div className="panel-head"><div><p className="eyebrow">AUDIT TRAIL</p><h2>{selected?.title || 'Select a task'}</h2></div>{selected && <i className={`status-pill ${statusTone[selected.status] || 'neutral'}`}>{selected.status}</i>}</div>
      {selected ? <>
        {chatTask ? <ChatThread task={selected} timeline={timeline} inputValue={reviewReason} setInputValue={setReviewReason} onSend={() => onFeedback(selected)} /> : <div className="review-actions">{selected.status === 'HUMAN_REVIEW_REQUIRED' && <>{isSkipProposal && <p className="event-error">{selected.error}</p>}<input value={reviewReason} onChange={event => setReviewReason(event.target.value)} placeholder="Your feedback for the next Agent loop" /><button className="primary-button" onClick={() => onFeedback(selected)} disabled={!reviewReason.trim()}>Send feedback & continue</button><button className="primary-button" onClick={() => onReview(selected, true)}>{isSkipProposal ? 'Accept skip' : 'Approve'}</button><button className="danger-button" onClick={() => onReview(selected, false)}>{isSkipProposal ? 'Continue anyway' : 'Reject'}</button></>}{selected.status === 'COMPLETED' && (applyResult?.taskId === selected.id ? <><span className="apply-success">Apply success: {applyResult.status.replace(/_/g, ' ')}</span>{applyResult.warnings.length > 0 && <span className="apply-warnings">{applyResult.warnings.join(' | ')}</span>}<button className="primary-button" onClick={() => onApply(selected)}>Apply again if wrong</button></> : <button className="primary-button" onClick={() => onApply(selected)}>Apply to repo</button>)}{selected.status === 'FAILED' && <button className="danger-button" onClick={() => onRerun(selected)}>Re-run task</button>}</div>}
        <div className="timeline">{timeline.map(event => <div className="timeline-event" key={`${event.type}-${event.id}`}><span className={`timeline-marker ${event.type}`} /><div className="event-copy"><div className="event-top"><strong>{event.label}</strong><span>{formatDate(event.started_at)}</span></div><div className="event-meta"><i className={`status-pill ${statusTone[event.status || ''] || 'neutral'}`}>{event.type.replace('_', ' ')}</i>{event.latency_ms ? <span>{formatDuration(event.latency_ms)}</span> : null}</div>{event.error && <p className="event-error">{event.error}</p>}{renderEventDetail(event)}</div></div>)}{timeline.length === 0 && <div className="empty">No execution events recorded.</div>}</div>
      </> : <div className="empty centered">Choose a task to inspect its Agent trace.</div>}
    </div>
  </section>
}

function SkillsView({ skills, importUrl, setImportUrl, onImport, onReload }: { skills: Skill[]; importUrl: string; setImportUrl: (value: string) => void; onImport: () => void; onReload: () => void }) {
  return <section className="skills-layout">
    <div className="panel">
      <div className="panel-head"><div><p className="eyebrow">CONFIGURABLE AGENTS</p><h2>Skill registry</h2></div><span className="count-label">{skills.length} skills</span></div>
      <p className="panel-note">TaskRouter scores each skill by task keywords, repository paths, and memory hits. Explicit task skill_name overrides auto routing. Prompt templates can be iterated without changing runtime code.</p>
    </div>
    <div className="panel skill-import">
      <div className="panel-head"><div><p className="eyebrow">GITHUB IMPORT</p><h2>Add skill from GitHub</h2></div></div>
      <div className="form-grid">
        <label>GitHub URL<input value={importUrl} onChange={event => setImportUrl(event.target.value)} placeholder="https://github.com/owner/skill-repo" /></label>
        <button className="primary-button" onClick={onImport} disabled={!importUrl.trim()}>Import skill</button>
        <button className="primary-button" onClick={onReload}>Reload folder</button>
      </div>
    </div>
    <div className="skills-grid">
      {skills.map(skill => <article className="skill-card" key={skill.name}>
        <div className="skill-top"><strong>{skill.name}</strong><span className="status-pill neutral">{skill.workflow || 'standard_agent_loop'}</span></div>
        {skill.description && <p>{skill.description}</p>}
        <div className="skill-meta">
          {skill.keywords?.length ? <div className="memory-chips">{skill.keywords.slice(0, 8).map(keyword => <span key={keyword}>{keyword}</span>)}</div> : null}
          {skill.path_patterns?.length ? <div className="memory-chips">{skill.path_patterns.map(pattern => <span key={pattern}>{pattern}</span>)}</div> : null}
        </div>
        {Object.entries(skill.prompts ?? {}).length > 0 && <details className="long-value"><summary>Prompts ({Object.keys(skill.prompts ?? {}).length} agents)</summary><pre>{JSON.stringify(skill.prompts, null, 2)}</pre></details>}
      </article>)}
      {skills.length === 0 && <div className="empty">No skills registered. POST /skills to register a custom template.</div>}
    </div>
  </section>
}

function MemoryView({ query, repo, setQuery, setRepo, onSearch, memories, repositories }: { query: string; repo: string; setQuery: (value: string) => void; setRepo: (value: string) => void; onSearch: () => void; memories: Memory[]; repositories: Repository[] }) {
  return <section className="memory-layout">
    <div className="panel search-panel">
      <div className="panel-head"><div><p className="eyebrow">LONG-TERM CONTEXT</p><h2>Memory inspector</h2></div></div>
      <p className="panel-note">Search structured execution memories by repository. Results combine keyword relevance, embedding similarity, freshness, and access frequency.</p>
      <div className="form-grid"><label>Repository<select value={repo} onChange={event => setRepo(event.target.value)}>{repositories.map(item => <option value={item.id} key={item.id}>{item.name} ({item.id})</option>)}{!repositories.length && <option value="">No repositories</option>}</select></label><label>Query<input value={query} onChange={event => setQuery(event.target.value)} placeholder="retry timeout" onKeyDown={event => event.key === 'Enter' && onSearch()} /></label><button className="primary-button" onClick={onSearch}>Search memory</button></div>
    </div>
    <div className="memory-list">
      {memories.map(memory => <article className="memory-item" key={memory.id}>
        <div className="memory-top"><i className="memory-kind">{memory.kind}</i><span>score {memory.score?.toFixed(2) ?? '0.00'}</span></div>
        {memory.title && <h3>{memory.title}</h3>}
        <p>{memory.summary || memory.content}</p>
        {(memory.symptom || memory.root_cause) && <div className="memory-details"><span>{memory.symptom && `symptom: ${memory.symptom}`}</span><span>{memory.root_cause && `root cause: ${memory.root_cause}`}</span></div>}
        {(memory.duplicate_of || memory.conflict_group_id) && <div className="memory-badges">{memory.duplicate_of && <span className="memory-badge duplicate">duplicate of {memory.duplicate_of}</span>}{memory.conflict_group_id && <span className="memory-badge conflict">conflict {memory.conflict_group_id}</span>}</div>}
        {memory.condition && <div className="memory-condition">{memory.condition}</div>}
        {memory.changed_files?.length ? <div className="memory-chips">{memory.changed_files.map(path => <span key={path}>{path}</span>)}</div> : null}
        {memory.links?.length ? <div className="memory-links"><strong>links</strong>{memory.links.map(link => <span key={link.id}>{link.target_type}:{link.target_id}</span>)}</div> : null}
        <footer><span>{memory.source || 'runtime'}</span><span>{memory.access_count ?? 0} recalls</span><span>{formatDate(memory.created_at)}</span></footer>
      </article>)}
      {memories.length === 0 && <div className="empty">Enter a repository ID and query to inspect recalled experience.</div>}
    </div>
  </section>
}

function EvaluationView({ data, report, mode, setMode, onRun }: { data: Evaluation | null; report: EvalReport | null; mode: string; setMode: (mode: string) => void; onRun: () => void }) {
  const metrics = data?.metrics
  const dimensions = ['result_usability', 'planning', 'efficiency', 'safety'] as const
  const dimensionLabels: Record<string, string> = { result_usability: 'Result', planning: 'Planning', efficiency: 'Efficiency', safety: 'Safety' }
  const dimensionAvg = (key: string) => {
    const runs = report?.runs ?? []
    if (!runs.length) return 0
    return runs.reduce((sum, run) => sum + (run.dimensions[key]?.score ?? 0), 0) / runs.length
  }
  const totalToolCalls = Object.values(report?.tool_stats ?? {}).reduce((sum, tool) => sum + tool.calls, 0)
  return <section className="evaluation-layout"><div className="panel suite-action"><div><p className="eyebrow">BENCHMARK CONTROL</p><h2>Run the full suite</h2><span>Execute all registered cases as one tracked batch.</span></div><select value={mode} onChange={event => setMode(event.target.value)}><option value="agent">Agent</option><option value="baseline">Baseline</option></select><button className="primary-button" onClick={onRun} disabled={!data?.cases.length}>Run suite</button></div><div className="stat-grid"><div className="stat-card"><span className="stat-icon success">●</span><div><small>Pass rate</small><strong>{metrics ? `${Math.round(metrics.pass_rate * 100)}%` : '—'}</strong></div></div><div className="stat-card"><span className="stat-icon neutral">●</span><div><small>Runs</small><strong>{report?.summary.total_runs ?? metrics?.total ?? '—'}</strong></div></div><div className="stat-card"><span className="stat-icon warning">●</span><div><small>Tool calls</small><strong>{totalToolCalls}</strong></div></div></div><div className="dimension-grid">{dimensions.map(key => <div className="dimension-card" key={key}><small>{dimensionLabels[key]}</small><strong>{Math.round(dimensionAvg(key))}</strong><i style={{ width: `${Math.max(dimensionAvg(key), 3)}%` }} /></div>)}</div><div className="panel history-panel"><div className="panel-head"><div><p className="eyebrow">METRIC HISTORY</p><h2>Batch pass rate</h2></div></div><div className="history-list">{data?.history.map(snapshot => <div className="history-row" key={snapshot.id}><span>{formatDate(snapshot.created_at)} · {snapshot.mode}</span><div className="history-track"><i style={{ width: `${Math.max(snapshot.pass_rate * 100, 3)}%` }} /></div><b>{Math.round(snapshot.pass_rate * 100)}%</b></div>)}{!data?.history.length && <div className="empty">Completed suites will appear here as historical snapshots.</div>}</div></div><div className="content-grid"><div className="panel wide"><div className="panel-head"><div><p className="eyebrow">REPRODUCIBLE CASES</p><h2>Benchmark suite</h2></div></div><div className="evaluation-list">{data?.cases.map(item => <div className="evaluation-case" key={item.id}><strong>{item.name}</strong><span>{item.title}</span><small>{item.description}</small></div>)}{!data?.cases.length && <div className="empty">No benchmark cases have been registered yet.</div>}</div></div><div className="panel"><div className="panel-head"><div><p className="eyebrow">BATCHES</p><h2>Recent suites</h2></div></div><div className="mode-list">{data?.batches.map(batch => <div className="health-row" key={batch.id}><span>{batch.name} <small>{batch.status}</small></span><b>{batch.completed}/{batch.total}</b></div>)}{!data?.batches.length && <div className="empty">No suite runs yet.</div>}</div></div></div><div className="panel comparison-panel"><div className="panel-head"><div><p className="eyebrow">BASELINE DELTA</p><h2>Agent versus baseline</h2></div></div><div className="comparison-list">{Object.entries(metrics?.by_case ?? {}).map(([caseID, modes]) => <div className="comparison-row" key={caseID}><strong>{caseID}</strong>{Object.entries(modes).map(([mode, value]) => <span key={mode}><i>{mode}</i> {value.passed}/{value.total}</span>)}</div>)}{!Object.keys(metrics?.by_case ?? {}).length && <div className="empty">Run the same case in agent and baseline modes to compare outcomes.</div>}</div></div><div className="content-grid"><div className="panel wide agent-panel"><div className="panel-head"><div><p className="eyebrow">AGENT USAGE</p><h2>Latency and token consumption</h2></div></div><div className="task-table"><div className="table-row table-header"><span>Agent</span><span>LLM calls</span><span>Steps</span><span>Tools</span><span>Tokens</span><span>Cost</span><span>Latency</span></div>{Object.entries(report?.agent_stats ?? {}).map(([name, agent]) => <div className="table-row" key={name}><span><strong>{name}</strong></span><span>{agent.calls}</span><span>{agent.steps}</span><span>{agent.tool_calls}<small className={agent.tool_errors ? 'danger-text' : ''}> {agent.tool_errors ? `${agent.tool_errors} errors` : ''}</small></span><span>{agent.total_tokens.toLocaleString()}</span><span>${agent.estimated_cost_usd.toFixed(4)}</span><span>{formatDuration(agent.latency_ms)}</span></div>)}</div></div><div className="panel tool-panel"><div className="panel-head"><div><p className="eyebrow">TOOL USAGE</p><h2>Tool call profile</h2></div></div><div className="task-table"><div className="table-row table-header"><span>Tool</span><span>Calls</span><span>Errors</span><span>Latency</span></div>{Object.entries(report?.tool_stats ?? {}).map(([name, tool]) => <div className="table-row" key={name}><span><strong>{name}</strong></span><span>{tool.calls}</span><span className={tool.errors ? 'danger-text' : ''}>{tool.errors}</span><span>{formatDuration(tool.latency_ms)}</span></div>)}{!Object.keys(report?.tool_stats ?? {}).length && <div className="empty">No tool calls recorded for this batch.</div>}</div></div></div><div className="panel runs-panel"><div className="panel-head"><div><p className="eyebrow">RUN HISTORY</p><h2>Evaluation runs</h2></div></div><div className="task-table"><div className="table-row table-header"><span>Case</span><span>Result</span><span>Quality</span><span>Result/Plan/Efficiency/Safety</span><span>Tokens</span><span>Tools</span><span>Duration</span></div>{report?.runs.map(run => <div className="table-row" key={run.run_id}><span><strong>{run.case_name}</strong><small>{run.category} · {formatDate(run.created_at)}</small></span><span><i className={`status-pill ${run.passed ? 'success' : 'danger'}`}>{run.passed ? 'PASSED' : 'FAILED'}</i></span><span>{Math.round(run.quality_score)}</span><span>{dimensions.map(key => `${Math.round(run.dimensions[key]?.score ?? 0)}`).join(' / ')}</span><span>{run.token_usage.total_tokens.toLocaleString()}</span><span>{Object.values(run.tool_usage ?? {}).reduce((sum, tool) => sum + tool.calls, 0)}</span><span>{formatDuration(run.duration_ms)}</span></div>)}{!report?.runs.length && <div className="empty">No evaluation runs yet.</div>}</div></div></section>
}

function TracePanel({ report }: { report: EvalReport | null }) {
  if (!report?.runs.length) return null
  return <section className="evaluation-layout trace-panel"><div className="panel"><div className="panel-head"><div><p className="eyebrow">TRACE EVALUATION</p><h2>Per-trace events</h2></div><span className="count-label">{report.runs.length} runs</span></div><div className="trace-list">{report.runs.map(run => <details className="trace-run" key={run.run_id}><summary><strong>{run.case_name}</strong><span>{run.trace.events.length} events · {Object.keys(run.trace.phases ?? {}).join(' / ')} · {run.passed ? 'PASSED' : 'FAILED'}</span></summary><div className="task-table"><div className="table-row table-header"><span>Phase</span><span>Agent</span><span>Type</span><span>Attempt</span><span>Status</span><span>Tokens</span><span>Latency</span></div>{run.trace.events.map(event => <div className="table-row trace-event-row" key={`${event.id}-${event.type}`}><span><strong>{event.phase || event.type}</strong><small>{event.summary || event.label || ''}</small></span><span>{event.agent || '—'}</span><span>{event.type}</span><span>{event.attempt || '—'}</span><span className={event.status === 'FAILED' ? 'danger-text' : ''}>{event.status || '—'}</span><span>{event.total_tokens ? event.total_tokens.toLocaleString() : '—'}</span><span>{formatDuration(event.latency_ms ?? 0)}</span></div>)}</div></details>)}</div></div></section>
}

export default App

const rootElement = document.getElementById('root')
if (rootElement) {
  createRoot(rootElement).render(<App />)
}
