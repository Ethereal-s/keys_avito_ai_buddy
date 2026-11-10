import { useEffect, useRef, useState } from 'react'
import { api, hasToken, setToken, type Action, type Approval, type Leave, type Message, type Metrics, type Task, type User } from './api'

type Msg = Message & { action?: Action; source?: string }

const STATUS: Record<Task['status'], string> = { todo: 'Не начато', in_progress: 'В работе', done: 'Готово' }
const CHIPS = [
  'Как оформить отгул?',
  'Хочу отгул 20.11',
  'Что мне делать дальше?',
  'Как получить доступ к трекеру?',
  'Как проходит экватор и оценка компетенций стажёра?',
]
const LEAVE_STATUS: Record<string, string> = { pending_approval: 'на согласовании', approved: 'согласована', rejected: 'отклонена' }
const fmt = (iso: string) => iso.split('-').reverse().join('.')

export default function App() {
  const [user, setUser] = useState<User | null>(null)
  const [ready, setReady] = useState(!hasToken())
  useEffect(() => {
    if (hasToken()) api.me().then(setUser).catch(() => setToken('')).finally(() => setReady(true))
  }, [])
  if (!ready) return <p className="center">Загрузка…</p>
  return user ? <Main user={user} onLogout={() => { setToken(''); setUser(null) }} /> : <Login onLogin={setUser} />
}

function Login({ onLogin }: { onLogin: (u: User) => void }) {
  const [login, setLogin] = useState('anna')
  const [password, setPassword] = useState('intern123')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const submit = async () => {
    setBusy(true); setErr('')
    try {
      const r = await api.login(login, password)
      setToken(r.token); onLogin(r.user)
    } catch (e) { setErr((e as Error).message) } finally { setBusy(false) }
  }
  return (
    <div className="login card">
      <h1>AI-buddy</h1>
      <input aria-label="Логин" value={login} onChange={e => setLogin(e.target.value)} placeholder="Логин" />
      <input aria-label="Пароль" type="password" value={password} onChange={e => setPassword(e.target.value)} placeholder="Пароль" />
      <button onClick={submit} disabled={busy}>{busy ? 'Входим…' : 'Войти'}</button>
      {err && <p className="err">{err}</p>}
      <p className="hint">Тестовые учётки: anna / intern123 (стажёр), boris / mentor123 (наставник)</p>
    </div>
  )
}

function Main({ user, onLogout }: { user: User; onLogout: () => void }) {
  const [msgs, setMsgs] = useState<Msg[]>([])
  const [tasks, setTasks] = useState<Task[]>([])
  const [leaves, setLeaves] = useState<Leave[]>([])
  const [metrics, setMetrics] = useState<Metrics | null>(null)
  const [approvals, setApprovals] = useState<Approval[]>([])
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const end = useRef<HTMLDivElement>(null)
  const isMentor = user.role === 'mentor'

  const refresh = async () => {
    try {
      const [c, l] = await Promise.all([api.checklist(), api.leaves()])
      setTasks(c.tasks); setLeaves(l)
      if (isMentor) {
        setMetrics(await api.metrics())
        setApprovals(await api.approvals())
      }
    } catch (e) { setErr((e as Error).message) }
  }
  useEffect(() => {
    api.messages().then(setMsgs).catch(e => setErr(e.message))
    refresh()
  }, [])
  useEffect(() => {
    const id = setInterval(refresh, 10000) // подтягиваем новые заявки и решения
    return () => clearInterval(id)
  }, [])
  useEffect(() => { end.current?.scrollIntoView({ behavior: 'smooth' }) }, [msgs, busy])

  const send = async (t: string) => {
    if (!t.trim() || busy) return
    setText(''); setErr(''); setBusy(true)
    setMsgs(m => [...m, { role: 'user', text: t }])
    try {
      const r = await api.chat(t)
      setMsgs(m => [...m, { role: 'assistant', text: r.text, action: r.action, source: r.source }])
    } catch (e) { setErr((e as Error).message) } finally { setBusy(false); refresh() }
  }
  const confirm = async (a: Action) => {
    setBusy(true); setErr('')
    try { await api.createLeave(a.from, a.to); setMsgs(await api.messages()) }
    catch (e) { setErr((e as Error).message) } finally { setBusy(false); refresh() }
  }
  const decide = async (id: number, d: 'approve' | 'reject') => {
    try { await api.decide(id, d) } catch (e) { setErr((e as Error).message) }
    refresh()
  }
  const setStatus = async (id: number, s: Task['status']) => {
    try { await api.setTask(id, s) } catch (e) { setErr((e as Error).message) }
    refresh()
  }

  const done = tasks.filter(t => t.status === 'done').length
  const stages = [...new Set(tasks.map(t => t.stage))]

  return (
    <div className="layout">
      <header>
        <b>AI-buddy</b>
        <span>{user.name} · {isMentor ? 'наставник' : 'стажёр'}</span>
        <button className="link" onClick={onLogout}>Выйти</button>
      </header>
      <section className="card chat">
        <div className="chips">{CHIPS.map(c => <button key={c} className="chip" disabled={busy} onClick={() => send(c)}>{c}</button>)}</div>
        <div className="msgs">
          {msgs.length === 0 && <p className="hint">Задайте вопрос или выберите подсказку выше.</p>}
          {msgs.map((m, i) => (
            <div key={i} className={'msg ' + m.role}>
              {m.text}
              {m.source && <small>Источник: {m.source}</small>}
              {m.action && <button disabled={busy} onClick={() => confirm(m.action!)}>Подтвердить заявку</button>}
            </div>
          ))}
          {busy && <div className="msg assistant">AI-buddy думает…</div>}
          <div ref={end} />
        </div>
        {err && <p className="err">{err}</p>}
        <div className="input">
          <input aria-label="Сообщение" value={text} placeholder="Ваш вопрос…" onChange={e => setText(e.target.value)} onKeyDown={e => e.key === 'Enter' && send(text)} />
          <button disabled={busy} onClick={() => send(text)}>Отправить</button>
        </div>
      </section>
      <aside>
        {tasks.length > 0 && (
          <div className="card">
            <h3>Адаптация: {done} из {tasks.length}</h3>
            <progress value={done} max={tasks.length} />
            {stages.map(s => (
              <div key={s}>
                <h4>{s}</h4>
                {tasks.filter(t => t.stage === s).map(t => (
                  <div key={t.id} className="task">
                    <span className={t.status}>{t.title}</span>
                    <select aria-label={t.title} value={t.status} onChange={e => setStatus(t.id, e.target.value as Task['status'])}>
                      {Object.entries(STATUS).map(([k, v]) => <option key={k} value={k}>{v}</option>)}
                    </select>
                  </div>
                ))}
              </div>
            ))}
          </div>
        )}
        {isMentor && (
          <div className="card">
            <h3>Заявки стажёров</h3>
            {approvals.length === 0 && <p className="hint">Заявок нет.</p>}
            {approvals.map(a => (
              <div key={a.id} className="task">
                <span>{a.external_id}: {a.intern}, {fmt(a.from)}{a.from !== a.to && ' — ' + fmt(a.to)} · <b>{LEAVE_STATUS[a.status] ?? a.status}</b></span>
                {a.status === 'pending_approval' && (
                  <span>
                    <button onClick={() => decide(a.id, 'approve')}>Согласовать</button>{' '}
                    <button className="danger" onClick={() => decide(a.id, 'reject')}>Отклонить</button>
                  </span>
                )}
              </div>
            ))}
          </div>
        )}
        <div className="card">
          <h3>Мои заявки на отсутствие</h3>
          {leaves.length === 0 && <p className="hint">Пока нет заявок.</p>}
          {leaves.map(l => (
            <p key={l.id}>{l.external_id}: {fmt(l.from)}{l.from !== l.to && ' — ' + fmt(l.to)} · <b>{LEAVE_STATUS[l.status] ?? l.status}</b></p>
          ))}
        </div>
        {metrics && (
          <div className="card">
            <h3>Метрики пользы</h3>
            <p>Вопросов по базе знаний: {metrics.events.chat_kb ?? 0}</p>
            <p>Эскалаций наставнику: {metrics.events.chat_escalate ?? 0}</p>
            <p>Отказов по правам: {metrics.events.chat_denied ?? 0}</p>
            <p>Заявок создано в чате: {metrics.events.leave_created ?? 0}</p>
            <p>Шагов адаптации: {metrics.tasks_done} из {metrics.tasks_total}</p>
          </div>
        )}
      </aside>
    </div>
  )
}
