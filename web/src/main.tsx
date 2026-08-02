import { useEffect, useState } from 'react'
import './styles.css'

type Task = { id: string; repository_id: string; title: string; description: string; status: string; updated_at: string }
type Overview = { repositories: number; tasks: number; completed: number; failed: number; human_review: number; active: number; average_run_latency_ms: number; status_counts: Record<string, number> }
type TimelineEvent = { id: string; type: string; label: string; status?: string; error?: string; started_at: string; ended_at?: string; latency_ms?: number; payload?: unknown }
type Memory = { id: string; kind: string; content: string; source?: string; score?: number; access_count?: number; created_at: string }

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

  const openTask = async (task: Task) => {
    setSelected(task); setView('tasks')
    try { const result = await get<{ events: TimelineEvent[] }>(`/tasks/${task.id}/timeline`); setTimeline(result.events) } catch (err) { setError(err instanceof Error ? err.message : 'Unable to load timeline') }
  }
  const searchMemory = async () => {
    if (!memoryRepo) return
    try { const result = await get<Memory[]>(`/memory/search?repository_id=${encodeURIComponent(memoryRepo)}&query=${encodeURIComponent(memoryQuery)}`); setMemories(result) } catch (err) { setError(err instanceof Error ? err.message : 'Unable to search memory') }
  }

  return <div className="app-shell">
    <aside className="sidebar">
      <div className="brand"><span className="brand-mark">CC</span><div><strong>CodeCoDriver</strong><small>control room</small></div></div>
      <nav>{[['overview', 'Overview'], ['tasks', 'Task trace'], ['memory', 'Memory']].map(([key, label]) => <button className={view === key ? 'nav-item active' : 'nav-item'} onClick={() => setView(key)} key={key}><span className={`nav-dot ${key}`} />{label}</button>)}</nav>
      <div className="sidebar-foot"><span className="online-dot" /> Runtime online<div className="version">v0.1 · local execution</div></div>
    </aside>
    <main className="main-content">
      <header className="topbar"><div><p className="eyebrow">ENGINEERING AGENT RUNTIME</p><h1>{view === 'overview' ? 'Control room' : view === 'tasks' ? 'Task trace' : 'Memory inspector'}</h1></div><button className="refresh" onClick={() => void refresh()} aria-label="Refresh dashboard">↻ <span>Refresh</span></button></header>
      {error && <div className="error-banner">{error}</div>}
      {view === 'overview' && <OverviewView overview={overview} tasks={tasks} onTask={openTask} />}
      {view === 'tasks' && <TasksView tasks={tasks} selected={selected} timeline={timeline} onTask={openTask} />}
      {view === 'memory' && <MemoryView query={memoryQuery} repo={memoryRepo} setQuery={setMemoryQuery} setRepo={setMemoryRepo} onSearch={() => void searchMemory()} memories={memories} />}
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

export default App
