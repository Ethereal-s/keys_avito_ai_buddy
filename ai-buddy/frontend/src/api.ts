export type User = { id: number; login: string; name: string; role: string; mentor: string }
export type Task = { id: number; stage: string; title: string; status: 'todo' | 'in_progress' | 'done' }
export type Leave = { id: number; from: string; to: string; status: string; external_id: string; approver: string }
export type Approval = Leave & { intern: string }
export type Action = { type: string; from: string; to: string }
export type Reply = { text: string; intent: string; source?: string; action?: Action }
export type Message = { role: string; text: string }
export type Metrics = { events: Record<string, number>; tasks_done: number; tasks_total: number; interns: number }
export type Checklist = { tasks: Task[]; done: number; total: number }

let token = sessionStorage.getItem('token') ?? ''
export const hasToken = () => token !== ''
export const setToken = (t: string) => {
  token = t
  if (t) sessionStorage.setItem('token', t)
  else sessionStorage.removeItem('token')
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const r = await fetch('/api' + path, {
    method,
    headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + token },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  const data = await r.json().catch(() => ({}))
  if (!r.ok) throw new Error(data.error ?? 'Ошибка ' + r.status)
  return data as T
}

export const api = {
  login: (login: string, password: string) => call<{ token: string; user: User }>('POST', '/login', { login, password }),
  me: () => call<User>('GET', '/me'),
  messages: () => call<Message[]>('GET', '/messages'),
  chat: (text: string) => call<Reply>('POST', '/chat', { text }),
  checklist: () => call<Checklist>('GET', '/checklist'),
  setTask: (id: number, status: Task['status']) => call<unknown>('PATCH', `/checklist/${id}`, { status }),
  leaves: () => call<Leave[]>('GET', '/leaves'),
  createLeave: (from: string, to: string) => call<Leave>('POST', '/leaves', { from, to }),
  approvals: () => call<Approval[]>('GET', '/approvals'),
  decide: (id: number, decision: 'approve' | 'reject') => call<Leave>('POST', `/approvals/${id}`, { decision }),
  metrics: () => call<Metrics>('GET', '/metrics'),
}
