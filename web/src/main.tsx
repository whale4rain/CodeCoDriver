import { useEffect, useState } from 'react'
import './styles.css'

type Task = { id: string; repository_id: string; title: string; description: string; status: string; updated_at: string }
type Overview = { repositories: number; tasks: number; completed: number; failed: number; human_review: number; active: number; average_run_latency_ms: number; status_counts: Record<string, number> }
type TimelineEvent = { id: string; type: string; label: string; status?: string; error?: string; started_at: string; ended_at?: string; latency_ms?: number; payload?: unknown }
type Memory = { id: string; kind: string; content: string; source?: string; score?: number; access_count?: number; created_at: string }
type Evaluation = { cases: { id: string; name: string; title: string; description: string }[]; runs: { id: string; case_id: string; batch_id?: string; mode: string; status: string; passed: boolean; duration_ms: number; created_at: string; notes?: string }[]; batches: { id: string; name: string; mode: string; status: string; total: number; completed: number; passed: number; created_at: string }[]; history: { id: string; batch_id: string; mode: string; total: number; passed: number; pass_rate: number; avg_duration_ms: number; created_at: string }[]; metrics: { total: number; passed: number; pass_rate: number; by_mode: Record<string, { total: number; passed: number }>; by_case: Record<string, Record<string, { total: number; passed: number }>> } }

const statusTone: Record<string, string> = { COMPLETED: 'success', FAILED: 'danger', HUMAN_REVIEW_REQUIRED: 'warning', CANCELLED: 'muted', CREATED: 'neutral' }

async function get<T>(path: string): Promise<T> {
  const response = await fetch(path)
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
  const [selected, setSelected] = useState<Task | null>(null)
  const [timeline, setTimeline] = useState<TimelineEvent[]>([])
  const [memories, setMemories] = useState<Memory[]>([])
  const [evaluation, setEvaluation] = useState<Evaluation | null>(null)
  const [evaluationMode, setEvaluationMode] = useState('agent')
  const [memoryQuery, setMemoryQuery] = useState('')
  const [memoryRepo, setMemoryRepo] = useState('')
  const [error, setError] = useState('')

  const refresh = async () => {
    try {
      const [summary, taskData] = await Promise.all([get<Overview>('/dashboard/overview'), get<Task[]>('/tasks')])
      setOverview(summary); setTasks(taskData.sort((a, b) => b.updated_at.localeCompare(a.updated_at))); setError('')
    } catch (err) { setError(err instanceof Error ? err.message : 'Unable to load dashboard') }
  }
  useEffect(() => { void refresh(); const timer = window.setInterval(() => void refresh(), 10000); return () => window.clearInterval(timer) }, [])
  useEffect(() => { if (view === 'evaluation') void get<Evaluation>('/evaluations').then(setEvaluation).catch(err => setError(err instanceof Error ? err.message : 'Unable to load evaluations')) }, [view])

  const openTask = async (task: Task) => {
    setSelected(task); setView('tasks')
    try { const result = await get<{ events: TimelineEvent[] }>(`/tasks/${task.id}/timeline`); setTimeline(result.events) } catch (err) { setError(err instanceof Error ? err.message : 'Unable to load timeline') }
  }
  const searchMemory = async () => {
    if (!memoryRepo) return
    try { const result = await get<Memory[]>(`/memory/search?repository_id=${encodeURIComponent(memoryRepo)}&query=${encodeURIComponent(memoryQuery)}`); setMemories(result) } catch (err) { setError(err instanceof Error ? err.message : 'Unable to search memory') }
  }
  const runSuite = async () => {
    if (!evaluation?.cases.length) return
    try {
      const response = await fetch('/evaluations/suites', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: `${evaluationMode} suite`, mode: evaluationMode, case_ids: evaluation.cases.map(item => item.id) }) })
      if (!response.ok) throw new Error(`${response.status} ${response.statusText}`)
      setEvaluation(await get<Evaluation>('/evaluations'))
    } catch (err) { setError(err instanceof Error ? err.message : 'Unable to start evaluation suite') }
  }

  return <div className="app-shell">
    <aside className="sidebar">
      <div className="brand"><span className="brand-mark">CC</span><div><strong>CodeCoDriver</strong><small>control room</small></div></div>
      <nav>{[['overview', 'Overview'], ['tasks', 'Task trace'], ['memory', 'Memory'], ['evaluation', 'Evaluation']].map(([key, label]) => <button className={view === key ? 'nav-item active' : 'nav-item'} onClick={() => setView(key)} key={key}><span className={`nav-dot ${key}`} />{label}</button>)}</nav>
      <div className="sidebar-foot"><span className="online-dot" /> Runtime online<div className="version">v0.1 · local execution</div></div>
    </aside>
    <main className="main-content">
      <header className="topbar"><div><p className="eyebrow">ENGINEERING AGENT RUNTIME</p><h1>{view === 'overview' ? 'Control room' : view === 'tasks' ? 'Task trace' : view === 'evaluation' ? 'Evaluation' : 'Memory inspector'}</h1></div><button className="refresh" onClick={() => void refresh()} aria-label="Refresh dashboard">↻ <span>Refresh</span></button></header>
      {error && <div className="error-banner">{error}</div>}
      {view === 'overview' && <OverviewView overview={overview} tasks={tasks} onTask={openTask} />}
      {view === 'tasks' && <TasksView tasks={tasks} selected={selected} timeline={timeline} onTask={openTask} />}
      {view === 'memory' && <MemoryView query={memoryQuery} repo={memoryRepo} setQuery={setMemoryQuery} setRepo={setMemoryRepo} onSearch={() => void searchMemory()} memories={memories} />}
      {view === 'evaluation' && <EvaluationView data={evaluation} mode={evaluationMode} setMode={setEvaluationMode} onRun={() => void runSuite()} />}
    </main>
  </div>
}

function OverviewView({ overview, tasks, onTask }: { overview: Overview | null; tasks: Task[]; onTask: (task: Task) => void }) {
  const cards = [['Active runs', overview?.active ?? 0, 'neutral'], ['Completed', overview?.completed ?? 0, 'success'], ['Human review', overview?.human_review ?? 0, 'warning'], ['Avg. runtime', formatDuration(overview?.average_run_latency_ms ?? 0), 'neutral']]
  return <><section className="stat-grid">{cards.map(([label, value, tone]) => <div className="stat-card" key={label as string}><span className={`stat-icon ${tone}`}>●</span><div><small>{label}</small><strong>{value}</strong></div></div>)}</section><section className="content-grid"><div className="panel wide"><div className="panel-head"><div><p className="eyebrow">LIVE QUEUE</p><h2>Recent tasks</h2></div><span className="count-label">{tasks.length} total</span></div><TaskTable tasks={tasks.slice(0, 8)} onTask={onTask} /></div><div className="panel signal"><div className="panel-head"><div><p className="eyebrow">SYSTEM SIGNAL</p><h2>Run health</h2></div></div><div className="health-ring"><strong>{overview?.tasks ? Math.round(((overview.completed ?? 0) / overview.tasks) * 100) : 0}%</strong><span>completion</span></div><div className="health-row"><span>Repositories</span><b>{overview?.repositories ?? 0}</b></div><div className="health-row"><span>Failed</span><b className="danger-text">{overview?.failed ?? 0}</b></div></div></section></>
}

function TaskTable({ tasks, onTask }: { tasks: Task[]; onTask: (task: Task) => void }) { return <div className="task-table"><div className="table-row table-header"><span>Task</span><span>Status</span><span>Updated</span></div>{tasks.map(task => <button className="table-row task-row" key={task.id} onClick={() => onTask(task)}><span><strong>{task.title || 'Untitled task'}</strong><small>{task.description}</small></span><span><i className={`status-pill ${statusTone[task.status] || 'neutral'}`}>{task.status.replace(/_/g, ' ')}</i></span><span className="date">{formatDate(task.updated_at)}</span></button>)}{tasks.length === 0 && <div className="empty">No tasks yet. Create one through the API to see its execution trace.</div>}</div> }

function TasksView({ tasks, selected, timeline, onTask }: { tasks: Task[]; selected: Task | null; timeline: TimelineEvent[]; onTask: (task: Task) => void }) { return <section className="task-layout"><div className="panel task-list"><div className="panel-head"><div><p className="eyebrow">EXECUTION HISTORY</p><h2>All tasks</h2></div></div><TaskTable tasks={tasks} onTask={onTask} /></div><div className="panel trace-panel"><div className="panel-head"><div><p className="eyebrow">AUDIT TRAIL</p><h2>{selected?.title || 'Select a task'}</h2></div>{selected && <i className={`status-pill ${statusTone[selected.status] || 'neutral'}`}>{selected.status}</i>}</div>{selected ? <div className="timeline">{timeline.map(event => <div className="timeline-event" key={`${event.type}-${event.id}`}><span className={`timeline-marker ${event.type}`} /><div className="event-copy"><div className="event-top"><strong>{event.label}</strong><span>{formatDate(event.started_at)}</span></div><div className="event-meta"><i className={`status-pill ${statusTone[event.status || ''] || 'neutral'}`}>{event.type.replace('_', ' ')}</i>{event.latency_ms ? <span>{formatDuration(event.latency_ms)}</span> : null}</div>{event.error && <p className="event-error">{event.error}</p>}</div></div>)}{timeline.length === 0 && <div className="empty">No execution events recorded.</div>}</div> : <div className="empty centered">Choose a task to inspect its Agent trace.</div>}</div></section> }

function MemoryView({ query, repo, setQuery, setRepo, onSearch, memories }: { query: string; repo: string; setQuery: (value: string) => void; setRepo: (value: string) => void; onSearch: () => void; memories: Memory[] }) { return <section className="memory-layout"><div className="panel search-panel"><div className="panel-head"><div><p className="eyebrow">LONG-TERM CONTEXT</p><h2>Memory inspector</h2></div></div><p className="panel-note">Search structured execution memories by repository. Results combine keyword relevance, embedding similarity, freshness, and access frequency.</p><div className="form-grid"><label>Repository ID<input value={repo} onChange={event => setRepo(event.target.value)} placeholder="repo-..." /></label><label>Query<input value={query} onChange={event => setQuery(event.target.value)} placeholder="retry timeout" onKeyDown={event => event.key === 'Enter' && onSearch()} /></label><button className="primary-button" onClick={onSearch}>Search memory</button></div></div><div className="memory-list">{memories.map(memory => <article className="memory-item" key={memory.id}><div className="memory-top"><i className="memory-kind">{memory.kind}</i><span>score {memory.score?.toFixed(2) ?? '0.00'}</span></div><p>{memory.content}</p><footer><span>{memory.source || 'runtime'}</span><span>{memory.access_count ?? 0} recalls</span><span>{formatDate(memory.created_at)}</span></footer></article>)}{memories.length === 0 && <div className="empty">Enter a repository ID and query to inspect recalled experience.</div>}</div></section> }

function EvaluationView({ data, mode, setMode, onRun }: { data: Evaluation | null; mode: string; setMode: (mode: string) => void; onRun: () => void }) { const metrics = data?.metrics; return <section className="evaluation-layout"><div className="panel suite-action"><div><p className="eyebrow">BENCHMARK CONTROL</p><h2>Run the full suite</h2><span>Execute all registered cases as one tracked batch.</span></div><select value={mode} onChange={event => setMode(event.target.value)}><option value="agent">Agent</option><option value="baseline">Baseline</option></select><button className="primary-button" onClick={onRun} disabled={!data?.cases.length}>Run suite</button></div><div className="stat-grid"><div className="stat-card"><span className="stat-icon success">●</span><div><small>Pass rate</small><strong>{metrics ? `${Math.round(metrics.pass_rate * 100)}%` : '—'}</strong></div></div><div className="stat-card"><span className="stat-icon neutral">●</span><div><small>Runs</small><strong>{metrics?.total ?? '—'}</strong></div></div><div className="stat-card"><span className="stat-icon warning">●</span><div><small>Benchmark cases</small><strong>{data?.cases.length ?? '—'}</strong></div></div></div><div className="panel history-panel"><div className="panel-head"><div><p className="eyebrow">METRIC HISTORY</p><h2>Batch pass rate</h2></div></div><div className="history-list">{data?.history.map(snapshot => <div className="history-row" key={snapshot.id}><span>{formatDate(snapshot.created_at)} · {snapshot.mode}</span><div className="history-track"><i style={{ width: `${Math.max(snapshot.pass_rate * 100, 3)}%` }} /></div><b>{Math.round(snapshot.pass_rate * 100)}%</b></div>)}{!data?.history.length && <div className="empty">Completed suites will appear here as historical snapshots.</div>}</div></div><div className="content-grid"><div className="panel wide"><div className="panel-head"><div><p className="eyebrow">REPRODUCIBLE CASES</p><h2>Benchmark suite</h2></div></div><div className="evaluation-list">{data?.cases.map(item => <div className="evaluation-case" key={item.id}><strong>{item.name}</strong><span>{item.title}</span><small>{item.description}</small></div>)}{!data?.cases.length && <div className="empty">No benchmark cases have been registered yet.</div>}</div></div><div className="panel"><div className="panel-head"><div><p className="eyebrow">BATCHES</p><h2>Recent suites</h2></div></div><div className="mode-list">{data?.batches.map(batch => <div className="health-row" key={batch.id}><span>{batch.name} <small>{batch.status}</small></span><b>{batch.completed}/{batch.total}</b></div>)}{!data?.batches.length && <div className="empty">No suite runs yet.</div>}</div></div></div><div className="panel comparison-panel"><div className="panel-head"><div><p className="eyebrow">BASELINE DELTA</p><h2>Agent versus baseline</h2></div></div><div className="comparison-list">{Object.entries(metrics?.by_case ?? {}).map(([caseID, modes]) => <div className="comparison-row" key={caseID}><strong>{caseID}</strong>{Object.entries(modes).map(([mode, value]) => <span key={mode}><i>{mode}</i> {value.passed}/{value.total}</span>)}</div>)}{!Object.keys(metrics?.by_case ?? {}).length && <div className="empty">Run the same case in agent and baseline modes to compare outcomes.</div>}</div></div><div className="panel runs-panel"><div className="panel-head"><div><p className="eyebrow">RUN HISTORY</p><h2>Evaluation runs</h2></div></div><div className="task-table"><div className="table-row table-header"><span>Case</span><span>Mode</span><span>Result</span></div>{data?.runs.map(run => <div className="table-row" key={run.id}><span><strong>{run.case_id}</strong><small>{formatDate(run.created_at)}</small></span><span>{run.mode}</span><span><i className={`status-pill ${run.passed ? 'success' : 'danger'}`}>{run.passed ? 'PASSED' : 'FAILED'}</i></span></div>)}{!data?.runs.length && <div className="empty">No evaluation runs yet.</div>}</div></div></section> }

export default App
