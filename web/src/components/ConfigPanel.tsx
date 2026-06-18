import { useState, useEffect, useRef } from 'react'
import { motion, AnimatePresence } from 'framer-motion'
import { Key, Cpu, Save, Eye, EyeOff, Check, Loader2, Users, Plus, Trash2, Globe, ClipboardPaste, TestTube, Download, Upload, RefreshCw, X } from 'lucide-react'
import { useSettings } from '../contexts/SettingsContext'
import { apiFetch, testAccountModel, testPoolAll, exportAccounts, importAccounts, replaceCookie } from '../lib/api'

interface Account {
  id: string
  service_token: string
  user_id: string
  ph: string
  active: boolean
}

interface ModelTestResult {
  model: string
  loading: boolean
  success?: boolean
  response?: string
  error?: string
}

export function ConfigPanel() {
  const { t, resolvedTheme, lang } = useSettings()
  const isDark = resolvedTheme === 'dark'

  const [apiKey, setApiKey] = useState('')
  const [showKey, setShowKey] = useState(false)
  const [baseUrl, setBaseUrl] = useState(() => localStorage.getItem('api_base_url') || '')
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)
  const [defaultModel, setDefaultModel] = useState('mimo-v2.5')
  const [accounts, setAccounts] = useState<Account[]>([])
  const [loading, setLoading] = useState(true)

  // Add form state
  const [showAdd, setShowAdd] = useState(false)
  const [newId, setNewId] = useState('')
  const [newToken, setNewToken] = useState('')
  const [newUserId, setNewUserId] = useState('')
  const [newPh, setNewPh] = useState('')
  const [adding, setAdding] = useState(false)
  const [pasteJson, setPasteJson] = useState('')
  const [showPaste, setShowPaste] = useState(false)
  const [testingAccount, setTestingAccount] = useState<string | null>(null)
  const [testResults, setTestResults] = useState<Record<string, { status: 'ok' | 'error' | 'testing', message: string }>>({})

  // New states for model test dropdown
  const [showModelDropdown, setShowModelDropdown] = useState<string | null>(null)
  const [modelTestResults, setModelTestResults] = useState<Record<string, ModelTestResult>>({})

  // Cookie replacement modal state
  const [showCookieModal, setShowCookieModal] = useState(false)
  const [cookieAccount, setCookieAccount] = useState<Account | null>(null)
  const [cookieForm, setCookieForm] = useState({ service_token: '', user_id: '', ph: '' })
  const [cookieSaving, setCookieSaving] = useState(false)

  // Pool test state
  const [poolTesting, setPoolTesting] = useState(false)
  const [poolTestResults, setPoolTestResults] = useState<Array<{id: string; healthy: boolean; error?: string}> | null>(null)

  // Import/Export state
  const fileInputRef = useRef<HTMLInputElement>(null)
  const [importing, setImporting] = useState(false)
  const [importResult, setImportResult] = useState<{imported: number; skipped: number; failed: number} | null>(null)

  const loadConfig = () => {
    apiFetch('/admin/api/config')
      .then(r => r.json())
      .then(cfg => {
        setApiKey(cfg.api_key || '')
        setDefaultModel(cfg.default_model || 'mimo-v2.5')
        setAccounts(cfg.accounts || [])
      })
      .catch(() => {})
      .finally(() => setLoading(false))
  }

  useEffect(() => { loadConfig() }, [])

  const handleSave = async () => {
    setSaving(true)
    try {
      localStorage.setItem('api_base_url', baseUrl)
      await apiFetch('/admin/api/config', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ default_model: defaultModel })
      })
      setSaved(true)
      setTimeout(() => setSaved(false), 2000)
    } finally {
      setSaving(false)
    }
  }

  const handleAdd = async () => {
    if (!newId.trim() || !newToken.trim()) return
    setAdding(true)
    try {
      const res = await apiFetch('/admin/api/accounts', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          id: newId.trim(),
          service_token: newToken.trim(),
          user_id: newUserId.trim(),
          ph: newPh.trim(),
          active: true
        })
      })
      if (res.ok) {
        setNewId(''); setNewToken(''); setNewUserId(''); setNewPh('')
        setShowAdd(false)
        loadConfig()
      }
    } finally {
      setAdding(false)
    }
  }

  const handleDelete = async (id: string) => {
    if (!confirm(lang === 'zh' ? `确认删除账号 "${id}"？` : `Delete account "${id}"?`)) return
    const res = await apiFetch('/admin/api/accounts', {
        method: 'DELETE',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id })
    })
    if (res.ok) loadConfig()
  }

  // 处理粘贴的 JSON 配置
  const handlePasteJson = async () => {
    if (!pasteJson.trim()) return

    try {
      const parsed = JSON.parse(pasteJson)
      let accountsToAdd: Account[] = []

      // 支持两种格式：{ accounts: [...] } 或直接 [...]
      if (parsed.accounts && Array.isArray(parsed.accounts)) {
        accountsToAdd = parsed.accounts
      } else if (Array.isArray(parsed)) {
        accountsToAdd = parsed
      } else if (parsed.id && parsed.service_token) {
        accountsToAdd = [parsed as Account]
      }

      if (accountsToAdd.length === 0) {
        alert(lang === 'zh' ? '未找到有效的账号配置' : 'No valid account config found')
        return
      }

      // 逐个添加账号
      let added = 0
      for (const acc of accountsToAdd) {
        if (!acc.id || !acc.service_token) continue

        const res = await apiFetch('/admin/api/accounts', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            id: acc.id,
            service_token: acc.service_token,
            user_id: acc.user_id || '',
            ph: acc.ph || '',
            active: acc.active !== false
          })
        })
        if (res.ok) added++
      }

      if (added > 0) {
        setPasteJson('')
        setShowPaste(false)
        loadConfig()
        alert(lang === 'zh' ? `成功添加 ${added} 个账号` : `Added ${added} accounts`)
      } else {
        alert(lang === 'zh' ? '添加失败，请检查配置格式' : 'Failed to add, please check config format')
      }
    } catch (e) {
      alert(lang === 'zh' ? 'JSON 格式错误: ' + (e as Error).message : 'JSON parse error: ' + (e as Error).message)
    }
  }

  // 测试账号有效性
  const handleTestAccount = async (account: Account) => {
    setTestingAccount(account.id)
    setTestResults(prev => ({ ...prev, [account.id]: { status: 'testing', message: lang === 'zh' ? '测试中...' : 'Testing...' } }))

    try {
      // 调用后端测试接口
      const res = await apiFetch('/admin/api/accounts/test', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id: account.id })
      })

      if (res.ok) {
        const data = await res.json()
        if (data.valid) {
          setTestResults(prev => ({ ...prev, [account.id]: { status: 'ok', message: lang === 'zh' ? '✓ 账号有效' : '✓ Account valid' } }))
        } else {
          setTestResults(prev => ({ ...prev, [account.id]: { status: 'error', message: lang === 'zh' ? '✗ Cookie 已过期' : '✗ Cookie expired' } }))
        }
      } else {
        setTestResults(prev => ({ ...prev, [account.id]: { status: 'error', message: lang === 'zh' ? '✗ 测试失败' : '✗ Test failed' } }))
      }
    } catch (e) {
      setTestResults(prev => ({ ...prev, [account.id]: { status: 'error', message: lang === 'zh' ? '✗ 网络错误' : '✗ Network error' } }))
    } finally {
      setTestingAccount(null)
    }
  }

  // 测试单个模型
  const handleTestModel = async (accountId: string, model: string) => {
    setShowModelDropdown(null)
    setModelTestResults(prev => ({
      ...prev,
      [`${accountId}-${model}`]: { model, loading: true }
    }))

    try {
      const result = await testAccountModel(accountId, model)
      const isSuccess = result.status_code === 200 && !!result.response
      setModelTestResults(prev => ({
        ...prev,
        [`${accountId}-${model}`]: {
          model,
          loading: false,
          success: isSuccess,
          response: isSuccess ? (result.response as string).slice(0, 200) : undefined,
          error: result.error || (isSuccess ? undefined : `HTTP ${result.status_code || '未知错误'}`)
        }
      }))
    } catch (e) {
      setModelTestResults(prev => ({
        ...prev,
        [`${accountId}-${model}`]: {
          model,
          loading: false,
          success: false,
          error: (e as Error).message
        }
      }))
    }
  }

  // Cookie 替换
  const handleOpenCookieModal = (account: Account) => {
    setCookieAccount(account)
    setCookieForm({
      service_token: account.service_token,
      user_id: account.user_id,
      ph: account.ph
    })
    setShowCookieModal(true)
  }

  const handleSaveCookie = async () => {
    if (!cookieAccount) return
    setCookieSaving(true)
    try {
      await replaceCookie(cookieAccount.id, cookieForm)
      setShowCookieModal(false)
      setCookieAccount(null)
      loadConfig()
    } catch (e) {
      alert(lang === 'zh' ? '保存失败: ' + (e as Error).message : 'Save failed: ' + (e as Error).message)
    } finally {
      setCookieSaving(false)
    }
  }

  // 全量测试
  const handlePoolTest = async () => {
    setPoolTesting(true)
    setPoolTestResults(null)
    try {
      const result = await testPoolAll()
      setPoolTestResults(result.results)
    } catch (e) {
      alert(lang === 'zh' ? '全量测试失败' : 'Pool test failed')
    } finally {
      setPoolTesting(false)
    }
  }

  // 导出
  const handleExport = async () => {
    try {
      const blob = await exportAccounts()
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      const date = new Date().toISOString().slice(0, 10)
      a.href = url
      a.download = `mimo-accounts-${date}.json`
      document.body.appendChild(a)
      a.click()
      document.body.removeChild(a)
      URL.revokeObjectURL(url)
    } catch (e) {
      alert(lang === 'zh' ? '导出失败' : 'Export failed')
    }
  }

  // 导入
  const handleImportClick = () => {
    fileInputRef.current?.click()
  }

  const handleImportFile = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    if (!file) return
    setImporting(true)
    setImportResult(null)
    try {
      const text = await file.text()
      const data = JSON.parse(text)
      const accountsArray = data.accounts || (Array.isArray(data) ? data : [])
      const result = await importAccounts(accountsArray)
      setImportResult(result)
      loadConfig()
    } catch (e) {
      alert(lang === 'zh' ? '导入失败: ' + (e as Error).message : 'Import failed: ' + (e as Error).message)
    } finally {
      setImporting(false)
      // Reset file input
      if (fileInputRef.current) fileInputRef.current.value = ''
    }
  }

  const maskToken = (token: string) => {
    if (!token) return '-'
    if (token.length <= 12) return '***'
    return token.slice(0, 6) + '...' + token.slice(-4)
  }

  const inputClass = isDark
    ? 'w-full bg-white/[0.04] border border-white/[0.08] rounded-xl px-4 py-3 text-white placeholder-gray-500 focus:outline-none focus:border-purple-500/50 focus:ring-1 focus:ring-purple-500/20 transition-all font-mono text-sm'
    : 'w-full bg-white border border-gray-200 rounded-xl px-4 py-3 text-gray-800 placeholder-gray-400 focus:outline-none focus:border-blue-500/50 focus:ring-1 focus:ring-blue-500/20 transition-all font-mono text-sm'

  const cardClass = isDark ? 'glass p-6 space-y-5' : 'bg-white rounded-xl border border-gray-100 p-6 space-y-5 shadow-sm'

  if (loading) {
    return (
      <div className="flex items-center justify-center h-64">
        <Loader2 className={`w-6 h-6 animate-spin ${isDark ? 'text-purple-400' : 'text-blue-500'}`} />
      </div>
    )
  }

  return (
    <div className="space-y-8 max-w-3xl">
      {/* Hidden file input for import */}
      <input
        ref={fileInputRef}
        type="file"
        accept=".json"
        className="hidden"
        onChange={handleImportFile}
      />

      {/* Header */}
      <div>
        <motion.h1 className={`text-2xl font-bold ${isDark ? 'text-white' : 'text-gray-800'}`} initial={{ opacity: 0, x: -20 }} animate={{ opacity: 1, x: 0 }}>
          {t('configTitle')}
        </motion.h1>
        <motion.p className={`${isDark ? 'text-gray-400' : 'text-gray-400'} text-sm mt-0.5`} initial={{ opacity: 0 }} animate={{ opacity: 1 }} transition={{ delay: 0.1 }}>
          {t('configSubtitle')}
        </motion.p>
      </div>

      {/* API Settings */}
      <motion.div className={cardClass} initial={{ opacity: 0, y: 20 }} animate={{ opacity: 1, y: 0 }} transition={{ delay: 0.2 }}>
        <h2 className={`text-base font-semibold ${isDark ? 'text-white' : 'text-gray-700'} flex items-center gap-2`}>
          <Key className={`w-4 h-4 ${isDark ? 'text-purple-400' : 'text-blue-500'}`} />
          API {lang === 'zh' ? '设置' : 'Settings'}
        </h2>

        <div className="space-y-1.5">
          <label className={`text-sm ${isDark ? 'text-gray-400' : 'text-gray-500'}`}>API Key</label>
          <div className="relative">
            <input type={showKey ? 'text' : 'password'} value={apiKey} onChange={e => setApiKey(e.target.value)} className={inputClass} placeholder="sk-..." readOnly />
            <button onClick={() => setShowKey(!showKey)} className={`absolute right-3 top-1/2 -translate-y-1/2 ${isDark ? 'text-gray-400 hover:text-white' : 'text-gray-400 hover:text-gray-600'} transition-colors`}>
              {showKey ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
            </button>
          </div>
        </div>

        <div className="space-y-1.5">
          <label className={`text-sm ${isDark ? 'text-gray-400' : 'text-gray-500'} flex items-center gap-1`}>
            <Globe className="w-3.5 h-3.5" /> Base URL
          </label>
          <input type="text" value={baseUrl} onChange={e => setBaseUrl(e.target.value)} className={inputClass}
            placeholder="http://localhost:8080" />
          <p className={`text-xs ${isDark ? 'text-gray-500' : 'text-gray-400'}`}>
            {lang === 'zh' ? '留空则使用当前地址，跨域部署时填完整地址' : 'Leave empty to use current origin, set when deploying separately.'}
          </p>
        </div>

        <div className="space-y-1.5">
          <label className={`text-sm ${isDark ? 'text-gray-400' : 'text-gray-500'} flex items-center gap-1`}>
            <Cpu className="w-3.5 h-3.5" /> {lang === 'zh' ? '默认模型' : 'Default Model'}
          </label>
          <div className="grid grid-cols-2 gap-3">
            {[
              { id: 'mimo-v2.5', label: 'MiMo V2.5', desc: lang === 'zh' ? '全模态 · 图片/音频/文件' : 'Omni · Image/Audio/File' },
              { id: 'mimo-v2.5-pro', label: 'MiMo V2.5 Pro', desc: lang === 'zh' ? '推理增强 · 文本/图片' : 'Reasoning · Text/Image' },
            ].map(m => (
              <button key={m.id} onClick={() => setDefaultModel(m.id)} className={`p-4 rounded-xl border text-left transition-all ${
                defaultModel === m.id
                  ? (isDark ? 'border-purple-500/50 bg-purple-500/10' : 'border-blue-500/50 bg-blue-50')
                  : (isDark ? 'border-white/[0.08] bg-white/[0.03] hover:bg-white/[0.06]' : 'border-gray-200 bg-gray-50 hover:bg-gray-100')
              }`}>
                <p className={`text-sm font-medium ${isDark ? 'text-white' : 'text-gray-800'}`}>{m.label}</p>
                <p className={`text-xs ${isDark ? 'text-gray-400' : 'text-gray-500'} mt-1`}>{m.desc}</p>
              </button>
            ))}
          </div>
        </div>
      </motion.div>

      {/* Accounts */}
      <motion.div className={cardClass} initial={{ opacity: 0, y: 20 }} animate={{ opacity: 1, y: 0 }} transition={{ delay: 0.3 }}>
        <div className="flex items-center justify-between">
          <h2 className={`text-base font-semibold ${isDark ? 'text-white' : 'text-gray-700'} flex items-center gap-2`}>
            <Users className={`w-4 h-4 ${isDark ? 'text-purple-400' : 'text-blue-500'}`} />
            MiMo {lang === 'zh' ? '账号池' : 'Account Pool'}
          </h2>
          <div className="flex items-center gap-2">
            <span className={`text-sm ${isDark ? 'text-gray-400' : 'text-gray-500'}`}>
              {accounts.filter(a => a.active).length}/{accounts.length} active
            </span>
            {/* 全量测试 */}
            <motion.button onClick={handlePoolTest} disabled={poolTesting}
              className={`flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm transition-colors ${
                isDark ? 'bg-amber-500/10 text-amber-400 hover:bg-amber-500/20' : 'bg-amber-50 text-amber-600 hover:bg-amber-100'
              }`} whileHover={{ scale: 1.02 }} whileTap={{ scale: 0.98 }}>
              {poolTesting ? <Loader2 className="w-4 h-4 animate-spin" /> : <RefreshCw className="w-4 h-4" />}
              {lang === 'zh' ? '全量测试' : 'Test All'}
            </motion.button>
            {/* 导出 */}
            <motion.button onClick={handleExport}
              className={`flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm transition-colors ${
                isDark ? 'bg-sky-500/10 text-sky-400 hover:bg-sky-500/20' : 'bg-sky-50 text-sky-600 hover:bg-sky-100'
              }`} whileHover={{ scale: 1.02 }} whileTap={{ scale: 0.98 }}>
              <Download className="w-4 h-4" />
              {lang === 'zh' ? '导出' : 'Export'}
            </motion.button>
            {/* 导入 */}
            <motion.button onClick={handleImportClick} disabled={importing}
              className={`flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm transition-colors ${
                isDark ? 'bg-violet-500/10 text-violet-400 hover:bg-violet-500/20' : 'bg-violet-50 text-violet-600 hover:bg-violet-100'
              }`} whileHover={{ scale: 1.02 }} whileTap={{ scale: 0.98 }}>
              {importing ? <Loader2 className="w-4 h-4 animate-spin" /> : <Upload className="w-4 h-4" />}
              {lang === 'zh' ? '导入' : 'Import'}
            </motion.button>
            <motion.button onClick={() => setShowPaste(!showPaste)}
              className={`flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm transition-colors ${
                isDark ? 'bg-emerald-500/10 text-emerald-400 hover:bg-emerald-500/20' : 'bg-emerald-50 text-emerald-600 hover:bg-emerald-100'
              }`} whileHover={{ scale: 1.02 }} whileTap={{ scale: 0.98 }}>
              <ClipboardPaste className="w-4 h-4" />
              {lang === 'zh' ? '粘贴配置' : 'Paste JSON'}
            </motion.button>
            <motion.button onClick={() => setShowAdd(!showAdd)}
              className={`flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm transition-colors ${
                isDark ? 'bg-purple-500/10 text-purple-400 hover:bg-purple-500/20' : 'bg-blue-50 text-blue-600 hover:bg-blue-100'
              }`} whileHover={{ scale: 1.02 }} whileTap={{ scale: 0.98 }}>
              <Plus className="w-4 h-4" />
              {lang === 'zh' ? '添加' : 'Add'}
            </motion.button>
          </div>
        </div>

        {/* Import result */}
        <AnimatePresence>
          {importResult && (
            <motion.div
              initial={{ opacity: 0, height: 0 }}
              animate={{ opacity: 1, height: 'auto' }}
              exit={{ opacity: 0, height: 0 }}
              className="overflow-hidden"
            >
              <div className={`p-3 rounded-xl border text-sm flex items-center gap-3 ${
                isDark ? 'bg-violet-500/10 border-violet-500/20 text-violet-300' : 'bg-violet-50 border-violet-200 text-violet-700'
              }`}>
                <Check className="w-4 h-4 shrink-0" />
                <span>
                  {lang === 'zh'
                    ? `导入完成: 成功 ${importResult.imported}，跳过 ${importResult.skipped}，失败 ${importResult.failed}`
                    : `Import done: ${importResult.imported} imported, ${importResult.skipped} skipped, ${importResult.failed} failed`
                  }
                </span>
                <button onClick={() => setImportResult(null)} className="ml-auto opacity-60 hover:opacity-100">
                  <X className="w-4 h-4" />
                </button>
              </div>
            </motion.div>
          )}
        </AnimatePresence>

        {/* Pool test results */}
        <AnimatePresence>
          {poolTestResults && (
            <motion.div
              initial={{ opacity: 0, height: 0 }}
              animate={{ opacity: 1, height: 'auto' }}
              exit={{ opacity: 0, height: 0 }}
              className="overflow-hidden"
            >
              <div className={`p-4 rounded-xl border space-y-2 ${
                isDark ? 'bg-amber-500/5 border-amber-500/20' : 'bg-amber-50/50 border-amber-200'
              }`}>
                <div className="flex items-center justify-between">
                  <span className={`text-sm font-medium ${isDark ? 'text-amber-300' : 'text-amber-700'}`}>
                    {lang === 'zh' ? '全量测试结果' : 'Pool Test Results'}
                  </span>
                  <button onClick={() => setPoolTestResults(null)} className={`opacity-60 hover:opacity-100 ${isDark ? 'text-amber-300' : 'text-amber-700'}`}>
                    <X className="w-4 h-4" />
                  </button>
                </div>
                <div className="space-y-1">
                  {poolTestResults.map(r => (
                    <div key={r.id} className={`flex items-center gap-2 text-xs ${isDark ? 'text-gray-300' : 'text-gray-600'}`}>
                      <span className={r.healthy ? 'text-emerald-500' : 'text-red-500'}>
                        {r.healthy ? '✓' : '✗'}
                      </span>
                      <span className="font-mono">{r.id}</span>
                      {r.error && <span className="text-red-400">{r.error}</span>}
                    </div>
                  ))}
                </div>
              </div>
            </motion.div>
          )}
        </AnimatePresence>

        {/* Paste JSON Form */}
        <AnimatePresence>
          {showPaste && (
            <motion.div
              initial={{ opacity: 0, height: 0 }}
              animate={{ opacity: 1, height: 'auto' }}
              exit={{ opacity: 0, height: 0 }}
              className="overflow-hidden"
            >
              <div className={`p-4 rounded-xl border space-y-3 ${isDark ? 'bg-white/[0.02] border-emerald-500/20' : 'bg-emerald-50/50 border-emerald-200'}`}>
                <p className={`text-xs ${isDark ? 'text-gray-400' : 'text-gray-500'}`}>
                  {lang === 'zh' ? '粘贴从浏览器扩展复制的配置 JSON:' : 'Paste config JSON copied from browser extension:'}
                </p>
                <textarea
                  value={pasteJson}
                  onChange={e => setPasteJson(e.target.value)}
                  placeholder={`{ "accounts": [{ "id": "...", "service_token": "...", "user_id": "...", "ph": "..." }] }`}
                  className={`${inputClass} h-32 resize-none`}
                />
                <div className="flex gap-2 justify-end">
                  <button onClick={() => setShowPaste(false)} className={`px-4 py-2 rounded-lg text-sm ${isDark ? 'text-gray-400 hover:text-white' : 'text-gray-500 hover:text-gray-700'} transition-colors`}>
                    {lang === 'zh' ? '取消' : 'Cancel'}
                  </button>
                  <motion.button onClick={handlePasteJson}
                    className="flex items-center gap-2 px-4 py-2 bg-gradient-to-r from-emerald-500 to-teal-500 text-white rounded-lg text-sm disabled:opacity-50"
                    whileHover={{ scale: 1.02 }} whileTap={{ scale: 0.98 }}>
                    <ClipboardPaste className="w-4 h-4" />
                    {lang === 'zh' ? '导入账号' : 'Import'}
                  </motion.button>
                </div>
              </div>
            </motion.div>
          )}
        </AnimatePresence>

        {/* Add Form */}
        <AnimatePresence>
          {showAdd && (
            <motion.div
              initial={{ opacity: 0, height: 0 }}
              animate={{ opacity: 1, height: 'auto' }}
              exit={{ opacity: 0, height: 0 }}
              className="overflow-hidden"
            >
              <div className={`p-4 rounded-xl border space-y-3 ${isDark ? 'bg-white/[0.02] border-purple-500/20' : 'bg-blue-50/50 border-blue-200'}`}>
                <div className="grid grid-cols-2 gap-3">
                  <input type="text" placeholder="Account ID (唯一标识)" value={newId} onChange={e => setNewId(e.target.value)} className={inputClass} />
                  <input type="text" placeholder="user_id" value={newUserId} onChange={e => setNewUserId(e.target.value)} className={inputClass} />
                </div>
                <input type="password" placeholder="service_token (Cookie中的完整值)" value={newToken} onChange={e => setNewToken(e.target.value)} className={inputClass} />
                <input type="text" placeholder="xiaomichatbot_ph" value={newPh} onChange={e => setNewPh(e.target.value)} className={inputClass} />
                <div className="flex gap-2 justify-end">
                  <button onClick={() => setShowAdd(false)} className={`px-4 py-2 rounded-lg text-sm ${isDark ? 'text-gray-400 hover:text-white' : 'text-gray-500 hover:text-gray-700'} transition-colors`}>
                    {lang === 'zh' ? '取消' : 'Cancel'}
                  </button>
                  <motion.button onClick={handleAdd} disabled={adding || !newId.trim() || !newToken.trim()}
                    className="flex items-center gap-2 px-4 py-2 bg-gradient-to-r from-blue-500 to-indigo-500 text-white rounded-lg text-sm disabled:opacity-50"
                    whileHover={{ scale: 1.02 }} whileTap={{ scale: 0.98 }}>
                    {adding ? <Loader2 className="w-4 h-4 animate-spin" /> : <Plus className="w-4 h-4" />}
                    {lang === 'zh' ? '确认添加' : 'Confirm'}
                  </motion.button>
                </div>
              </div>
            </motion.div>
          )}
        </AnimatePresence>

        {/* Account List */}
        {accounts.length === 0 ? (
          <div className={`text-center py-8 ${isDark ? 'text-gray-500' : 'text-gray-400'}`}>
            <Users className="w-8 h-8 mx-auto mb-2 opacity-40" />
            <p className="text-sm">{lang === 'zh' ? '暂无账号，点击上方"添加"' : 'No accounts. Click "Add" above'}</p>
          </div>
        ) : (
          <div className="space-y-3">
            <AnimatePresence>
              {accounts.map((acc, idx) => (
                <motion.div key={acc.id || idx}
                  className={`p-4 rounded-xl border ${isDark ? 'bg-white/[0.02] border-white/[0.06]' : 'bg-gray-50 border-gray-100'}`}
                  initial={{ opacity: 0, y: 10 }}
                  animate={{ opacity: 1, y: 0 }}
                  exit={{ opacity: 0, x: -20 }}
                  transition={{ delay: idx * 0.05 }}
                >
                  <div className="flex items-center gap-4">
                    <div className="w-10 h-10 rounded-full bg-gradient-to-br from-blue-400 to-indigo-500
                      flex items-center justify-center text-white text-xs font-bold shrink-0">
                      {(acc.user_id || String(idx + 1)).slice(-2)}
                    </div>

                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-2">
                        <span className={`text-sm font-medium ${isDark ? 'text-white' : 'text-gray-800'}`}>
                          {acc.id || `Account ${idx + 1}`}
                        </span>
                        <span className={`px-2 py-0.5 rounded-md text-[10px] font-semibold ${
                          acc.active ? 'bg-emerald-500/15 text-emerald-500' : 'bg-red-500/15 text-red-500'
                        }`}>
                          {acc.active ? 'Active' : 'Disabled'}
                        </span>
                      </div>
                      <div className={`flex flex-wrap gap-x-4 gap-y-0.5 mt-1 text-xs ${isDark ? 'text-gray-400' : 'text-gray-500'}`}>
                        <span className="font-mono">user_id: {acc.user_id || '-'}</span>
                        <span className="font-mono">ph: {acc.ph || '-'}</span>
                        <span className="font-mono">token: {maskToken(acc.service_token)}</span>
                      </div>
                      {testResults[acc.id] && (
                        <div className={`mt-2 text-xs ${
                          testResults[acc.id].status === 'ok' ? 'text-emerald-500' :
                          testResults[acc.id].status === 'error' ? 'text-red-500' : 'text-amber-500'
                        }`}>
                          {testResults[acc.id].message}
                        </div>
                      )}
                      {/* Model test results */}
                      {(['mimo-v2.5', 'mimo-v2.5-pro'] as const).map(model => {
                        const resultKey = `${acc.id}-${model}`
                        const result = modelTestResults[resultKey]
                        if (!result) return null
                        return (
                          <div key={resultKey} className={`mt-1 text-xs ${
                            result.loading ? 'text-amber-500' :
                            result.success ? 'text-emerald-500' : 'text-red-500'
                          }`}>
                            {result.loading && (
                              <span className="flex items-center gap-1">
                                <Loader2 className="w-3 h-3 animate-spin" />
                                {model}: {lang === 'zh' ? '测试中...' : 'Testing...'}
                              </span>
                            )}
                            {!result.loading && result.success && (
                              <span>{model}: {result.response}</span>
                            )}
                            {!result.loading && !result.success && (
                              <span>{model}: {result.error || (lang === 'zh' ? '测试失败' : 'Test failed')}</span>
                            )}
                          </div>
                        )
                      })}
                    </div>

                    {/* 测试模型按钮 (带下拉) */}
                    <div className="relative">
                      <motion.button
                        onClick={() => setShowModelDropdown(showModelDropdown === acc.id ? null : acc.id)}
                        className={`p-2 rounded-lg transition-colors ${isDark ? 'text-gray-500 hover:text-cyan-400 hover:bg-cyan-500/10' : 'text-gray-400 hover:text-cyan-500 hover:bg-cyan-50'}`}
                        whileHover={{ scale: 1.1 }} whileTap={{ scale: 0.9 }}
                      >
                        <Cpu className="w-4 h-4" />
                      </motion.button>
                      <AnimatePresence>
                        {showModelDropdown === acc.id && (
                          <motion.div
                            initial={{ opacity: 0, y: -5, scale: 0.95 }}
                            animate={{ opacity: 1, y: 0, scale: 1 }}
                            exit={{ opacity: 0, y: -5, scale: 0.95 }}
                            className={`absolute right-0 top-full mt-1 w-48 rounded-xl border shadow-lg z-50 ${
                              isDark ? 'bg-gray-800 border-gray-700' : 'bg-white border-gray-200'
                            }`}
                          >
                            <div className="p-1">
                              <button
                                onClick={() => handleTestModel(acc.id, 'mimo-v2.5')}
                                className={`w-full text-left px-3 py-2 rounded-lg text-sm transition-colors flex items-center gap-2 ${
                                  isDark ? 'text-gray-300 hover:bg-white/[0.06]' : 'text-gray-600 hover:bg-gray-50'
                                }`}
                              >
                                {modelTestResults[`${acc.id}-mimo-v2.5`]?.loading ? (
                                  <Loader2 className="w-3.5 h-3.5 animate-spin" />
                                ) : (
                                  <TestTube className="w-3.5 h-3.5" />
                                )}
                                mimo-v2.5
                              </button>
                              <button
                                onClick={() => handleTestModel(acc.id, 'mimo-v2.5-pro')}
                                className={`w-full text-left px-3 py-2 rounded-lg text-sm transition-colors flex items-center gap-2 ${
                                  isDark ? 'text-gray-300 hover:bg-white/[0.06]' : 'text-gray-600 hover:bg-gray-50'
                                }`}
                              >
                                {modelTestResults[`${acc.id}-mimo-v2.5-pro`]?.loading ? (
                                  <Loader2 className="w-3.5 h-3.5 animate-spin" />
                                ) : (
                                  <TestTube className="w-3.5 h-3.5" />
                                )}
                                mimo-v2.5-pro
                              </button>
                            </div>
                          </motion.div>
                        )}
                      </AnimatePresence>
                    </div>

                    {/* 替换Cookie按钮 */}
                    <motion.button onClick={() => handleOpenCookieModal(acc)}
                      className={`p-2 rounded-lg transition-colors ${isDark ? 'text-gray-500 hover:text-orange-400 hover:bg-orange-500/10' : 'text-gray-400 hover:text-orange-500 hover:bg-orange-50'}`}
                      whileHover={{ scale: 1.1 }} whileTap={{ scale: 0.9 }}>
                      <RefreshCw className="w-4 h-4" />
                    </motion.button>

                    {/* 测试账号有效性 */}
                    <motion.button onClick={() => handleTestAccount(acc)}
                      disabled={testingAccount === acc.id}
                      className={`p-2 rounded-lg transition-colors ${isDark ? 'text-gray-500 hover:text-blue-400 hover:bg-blue-500/10' : 'text-gray-400 hover:text-blue-500 hover:bg-blue-50'}`}
                      whileHover={{ scale: 1.1 }} whileTap={{ scale: 0.9 }}>
                      {testingAccount === acc.id ? <Loader2 className="w-4 h-4 animate-spin" /> : <TestTube className="w-4 h-4" />}
                    </motion.button>
                    {/* 删除 */}
                    <motion.button onClick={() => handleDelete(acc.id)}
                      className={`p-2 rounded-lg transition-colors ${isDark ? 'text-gray-500 hover:text-red-400 hover:bg-red-500/10' : 'text-gray-400 hover:text-red-500 hover:bg-red-50'}`}
                      whileHover={{ scale: 1.1 }} whileTap={{ scale: 0.9 }}>
                      <Trash2 className="w-4 h-4" />
                    </motion.button>
                  </div>
                </motion.div>
              ))}
            </AnimatePresence>
          </div>
        )}
      </motion.div>

      {/* Cookie Replacement Modal */}
      <AnimatePresence>
        {showCookieModal && cookieAccount && (
          <motion.div
            initial={{ opacity: 0 }}
            animate={{ opacity: 1 }}
            exit={{ opacity: 0 }}
            className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 backdrop-blur-sm"
            onClick={() => setShowCookieModal(false)}
          >
            <motion.div
              initial={{ opacity: 0, scale: 0.95, y: 20 }}
              animate={{ opacity: 1, scale: 1, y: 0 }}
              exit={{ opacity: 0, scale: 0.95, y: 20 }}
              className={`w-full max-w-lg mx-4 rounded-2xl border p-6 space-y-5 shadow-2xl ${
                isDark ? 'bg-gray-900 border-gray-700' : 'bg-white border-gray-200'
              }`}
              onClick={e => e.stopPropagation()}
            >
              <div className="flex items-center justify-between">
                <h3 className={`text-lg font-semibold ${isDark ? 'text-white' : 'text-gray-800'}`}>
                  {lang === 'zh' ? '替换 Cookie' : 'Replace Cookie'} - {cookieAccount.id}
                </h3>
                <button onClick={() => setShowCookieModal(false)}
                  className={`p-1.5 rounded-lg transition-colors ${isDark ? 'text-gray-400 hover:text-white hover:bg-white/10' : 'text-gray-400 hover:text-gray-600 hover:bg-gray-100'}`}>
                  <X className="w-5 h-5" />
                </button>
              </div>

              <div className="space-y-4">
                <div className="space-y-1.5">
                  <label className={`text-sm ${isDark ? 'text-gray-400' : 'text-gray-500'}`}>service_token</label>
                  <input
                    type="text"
                    value={cookieForm.service_token}
                    onChange={e => setCookieForm(prev => ({ ...prev, service_token: e.target.value }))}
                    className={inputClass}
                    placeholder="service_token"
                  />
                </div>
                <div className="space-y-1.5">
                  <label className={`text-sm ${isDark ? 'text-gray-400' : 'text-gray-500'}`}>user_id</label>
                  <input
                    type="text"
                    value={cookieForm.user_id}
                    onChange={e => setCookieForm(prev => ({ ...prev, user_id: e.target.value }))}
                    className={inputClass}
                    placeholder="user_id"
                  />
                </div>
                <div className="space-y-1.5">
                  <label className={`text-sm ${isDark ? 'text-gray-400' : 'text-gray-500'}`}>ph</label>
                  <input
                    type="text"
                    value={cookieForm.ph}
                    onChange={e => setCookieForm(prev => ({ ...prev, ph: e.target.value }))}
                    className={inputClass}
                    placeholder="xiaomichatbot_ph"
                  />
                </div>
              </div>

              <div className="flex gap-3 justify-end">
                <button onClick={() => setShowCookieModal(false)}
                  className={`px-4 py-2 rounded-lg text-sm transition-colors ${isDark ? 'text-gray-400 hover:text-white' : 'text-gray-500 hover:text-gray-700'}`}>
                  {lang === 'zh' ? '取消' : 'Cancel'}
                </button>
                <motion.button onClick={handleSaveCookie} disabled={cookieSaving}
                  className="flex items-center gap-2 px-5 py-2 bg-gradient-to-r from-orange-500 to-amber-500 text-white rounded-lg text-sm disabled:opacity-50"
                  whileHover={{ scale: 1.02 }} whileTap={{ scale: 0.98 }}>
                  {cookieSaving ? <Loader2 className="w-4 h-4 animate-spin" /> : <Save className="w-4 h-4" />}
                  {lang === 'zh' ? '保存' : 'Save'}
                </motion.button>
              </div>
            </motion.div>
          </motion.div>
        )}
      </AnimatePresence>

      {/* Save */}
      <motion.div className="flex justify-end" initial={{ opacity: 0 }} animate={{ opacity: 1 }} transition={{ delay: 0.4 }}>
        <motion.button onClick={handleSave} disabled={saving}
          className={`flex items-center gap-2 px-6 py-3 rounded-xl font-medium text-sm transition-all ${
            saved ? (isDark ? 'bg-green-500/20 text-green-400 border border-green-500/30' : 'bg-green-50 text-green-600 border border-green-200')
              : 'bg-gradient-to-r from-blue-500 to-indigo-500 text-white hover:shadow-lg hover:shadow-blue-500/25'
          } disabled:opacity-50`}
          whileHover={{ scale: 1.02 }} whileTap={{ scale: 0.98 }}>
          {saving ? <Loader2 className="w-4 h-4 animate-spin" /> : saved ? <Check className="w-4 h-4" /> : <Save className="w-4 h-4" />}
          {saving ? (lang === 'zh' ? '保存中...' : 'Saving...') : saved ? (lang === 'zh' ? '已保存' : 'Saved') : (lang === 'zh' ? '保存配置' : 'Save Config')}
        </motion.button>
      </motion.div>
    </div>
  )
}
