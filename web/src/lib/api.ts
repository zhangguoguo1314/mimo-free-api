const ENV_BASE_URL = import.meta.env.VITE_API_BASE_URL ?? ''

function getBaseUrl(): string {
  return localStorage.getItem('api_base_url') || ENV_BASE_URL
}

function getAuthToken(): string | null {
  return localStorage.getItem('admin_token')
}

export async function apiFetch(path: string, init?: RequestInit): Promise<Response> {
  const token = getAuthToken()
  const headers = new Headers(init?.headers)
  if (token) {
    headers.set('X-Admin-Token', token)
  }
  return fetch(`${getBaseUrl()}${path}`, { ...init, headers })
}

// 模型测试
export async function testAccountModel(accountId: string, model: string): Promise<{account_id: string; model: string; status_code: number; response?: string; error?: string}> {
  const res = await apiFetch('/admin/api/accounts/test-model', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ account_id: accountId, model })
  })
  return res.json()
}

// 测试账号所有模型
export async function testAccountAllModels(accountId: string): Promise<{account_id: string; results: Array<{model: string; status_code: number; response?: string; error?: string}>}> {
  const res = await apiFetch('/admin/api/accounts/test-all-models', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ account_id: accountId })
  })
  return res.json()
}

export interface PoolStatus {
  id: string
  enabled: boolean
  healthy: boolean
  active: number
  rate_used: number
  rate_limit: number
  cooldown_remaining: number
  daily_used: number
  daily_limit: number
  fail429_count: number
  source: string
  added_at: string
}

export async function fetchPoolStatus(): Promise<{ total: number; accounts: PoolStatus[] }> {
  const res = await apiFetch('/admin/api/pool/status')
  return res.json()
}

// Toggle account enabled/disabled
export async function toggleAccount(id: string, enabled: boolean): Promise<void> {
  await apiFetch('/admin/api/accounts/toggle', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ id, enabled })
  })
}

// 账号池全量测试
export async function testPoolAll(): Promise<{results: Record<string, boolean>; healthy: number; total: number}> {
  const res = await apiFetch('/admin/api/pool/test-all', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' }
  })
  return res.json()
}

// 导出账号池（完整数据，包含敏感字段）
export async function exportAccounts(): Promise<Blob> {
  const token = getAuthToken()
  const headers: Record<string, string> = {}
  if (token) headers['X-Admin-Token'] = token
  const res = await fetch(`${getBaseUrl()}/admin/api/accounts/export?full=true`, { headers })
  if (!res.ok) throw new Error('Export failed')
  return res.blob()
}

// 导入账号池
export async function importAccounts(accounts: any[]): Promise<{added: number; skipped: number; errors?: {id: string; reason: string}[]}> {
  const res = await apiFetch('/admin/api/accounts/import', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ accounts })
  })
  return res.json()
}

// 替换Cookie
export async function replaceCookie(accountId: string, data: {service_token: string; user_id: string; ph: string}): Promise<any> {
  const res = await apiFetch('/admin/api/accounts/cookie', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ account_id: accountId, ...data })
  })
  return res.json()
}

// 密码相关
export async function getPasswordStatus(): Promise<{password_set: boolean}> {
  const res = await apiFetch('/admin/api/password/status')
  return res.json()
}

export async function setPassword(password: string): Promise<{success: boolean; status?: string}> {
  const res = await apiFetch('/admin/api/password/set', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ password })
  })
  return res.json()
}

export async function login(password: string): Promise<{success: boolean; status?: string; token?: string}> {
  const res = await apiFetch('/admin/api/password/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ password })
  })
  return res.json()
}

export async function changePassword(oldPassword: string, newPassword: string): Promise<{success: boolean}> {
  const res = await apiFetch('/admin/api/password/change', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ old_password: oldPassword, new_password: newPassword })
  })
  return res.json()
}
