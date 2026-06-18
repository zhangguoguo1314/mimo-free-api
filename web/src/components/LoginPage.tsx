import { useState } from 'react'
import { motion } from 'framer-motion'
import { Lock, Loader2, Eye, EyeOff, Shield } from 'lucide-react'
import { useSettings } from '../contexts/SettingsContext'
import { login as apiLogin, setPassword as apiSetPassword } from '../lib/api'

interface LoginPageProps {
  mode: 'login' | 'setup'
  onSuccess: () => void
}

export function LoginPage({ mode, onSuccess }: LoginPageProps) {
  const { resolvedTheme } = useSettings()
  const isDark = resolvedTheme === 'dark'

  const [password, setPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [showPassword, setShowPassword] = useState(false)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')

  const handleLogin = async () => {
    if (!password.trim()) {
      setError(isDark ? '请输入密码' : 'Please enter password')
      return
    }
    setLoading(true)
    setError('')
    try {
      const result = await apiLogin(password)
      if (result.success && result.token) {
        localStorage.setItem('admin_token', result.token)
        onSuccess()
      } else {
        setError(isDark ? '密码错误' : 'Wrong password')
      }
    } catch (e) {
      setError(isDark ? '登录失败，请重试' : 'Login failed, please try again')
    } finally {
      setLoading(false)
    }
  }

  const handleSetup = async () => {
    if (!password.trim()) {
      setError(isDark ? '请输入密码' : 'Please enter password')
      return
    }
    if (password.length < 6) {
      setError(isDark ? '密码长度至少6位' : 'Password must be at least 6 characters')
      return
    }
    if (password !== confirmPassword) {
      setError(isDark ? '两次密码不一致' : 'Passwords do not match')
      return
    }
    setLoading(true)
    setError('')
    try {
      const result = await apiSetPassword(password)
      if (result.success) {
        // After setting password, auto-login
        const loginResult = await apiLogin(password)
        if (loginResult.success && loginResult.token) {
          localStorage.setItem('admin_token', loginResult.token)
          onSuccess()
        }
      } else {
        setError(isDark ? '设置失败，请重试' : 'Setup failed, please try again')
      }
    } catch (e) {
      setError(isDark ? '设置失败，请重试' : 'Setup failed, please try again')
    } finally {
      setLoading(false)
    }
  }

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    if (mode === 'login') {
      handleLogin()
    } else {
      handleSetup()
    }
  }

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter') {
      handleSubmit(e)
    }
  }

  const inputClass = isDark
    ? 'w-full bg-white/[0.04] border border-white/[0.08] rounded-xl px-4 py-3 text-white placeholder-gray-500 focus:outline-none focus:border-purple-500/50 focus:ring-1 focus:ring-purple-500/20 transition-all text-sm'
    : 'w-full bg-white border border-gray-200 rounded-xl px-4 py-3 text-gray-800 placeholder-gray-400 focus:outline-none focus:border-blue-500/50 focus:ring-1 focus:ring-blue-500/20 transition-all text-sm'

  return (
    <div className="min-h-screen flex items-center justify-center relative overflow-hidden">
      {/* Background */}
      <div className={`absolute inset-0 ${isDark ? 'bg-gray-950' : 'bg-[#f8f9fc]'}`} />
      <div className="absolute inset-0 bg-grid opacity-30" />
      <div className="absolute inset-0 bg-glow opacity-20" />

      <motion.div
        initial={{ opacity: 0, y: 20, scale: 0.98 }}
        animate={{ opacity: 1, y: 0, scale: 1 }}
        transition={{ duration: 0.5, ease: 'easeOut' }}
        className={`relative z-10 w-full max-w-sm mx-4 rounded-2xl border p-8 shadow-2xl ${
          isDark ? 'bg-gray-900/80 border-gray-700/50 backdrop-blur-xl' : 'bg-white/80 border-gray-200/50 backdrop-blur-xl'
        }`}
      >
        {/* Header */}
        <div className="text-center mb-8">
          <motion.div
            initial={{ opacity: 0, scale: 0.8 }}
            animate={{ opacity: 1, scale: 1 }}
            transition={{ delay: 0.2, duration: 0.4 }}
            className={`w-16 h-16 mx-auto mb-4 rounded-2xl flex items-center justify-center ${
              isDark ? 'bg-gradient-to-br from-purple-500/20 to-blue-500/20 border border-purple-500/20' : 'bg-gradient-to-br from-blue-50 to-indigo-50 border border-blue-200'
            }`}
          >
            <Shield className={`w-8 h-8 ${isDark ? 'text-purple-400' : 'text-blue-500'}`} />
          </motion.div>
          <h1 className={`text-2xl font-bold ${isDark ? 'text-white' : 'text-gray-800'}`}>
            MiMo Free API
          </h1>
          <p className={`text-sm mt-1 ${isDark ? 'text-gray-400' : 'text-gray-500'}`}>
            {mode === 'login'
              ? (isDark ? '请输入管理员密码' : 'Enter admin password')
              : (isDark ? '首次使用，请设置管理员密码' : 'First time, set admin password')
            }
          </p>
        </div>

        {/* Form */}
        <form onSubmit={handleSubmit} className="space-y-4">
          <div className="space-y-1.5">
            <label className={`text-sm ${isDark ? 'text-gray-400' : 'text-gray-500'} flex items-center gap-1.5`}>
              <Lock className="w-3.5 h-3.5" />
              {isDark ? '密码' : 'Password'}
            </label>
            <div className="relative">
              <input
                type={showPassword ? 'text' : 'password'}
                value={password}
                onChange={e => { setPassword(e.target.value); setError('') }}
                onKeyDown={handleKeyDown}
                className={inputClass}
                placeholder={isDark ? '输入密码' : 'Enter password'}
                autoFocus
              />
              <button
                type="button"
                onClick={() => setShowPassword(!showPassword)}
                className={`absolute right-3 top-1/2 -translate-y-1/2 ${isDark ? 'text-gray-400 hover:text-white' : 'text-gray-400 hover:text-gray-600'} transition-colors`}
              >
                {showPassword ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
              </button>
            </div>
          </div>

          {mode === 'setup' && (
            <div className="space-y-1.5">
              <label className={`text-sm ${isDark ? 'text-gray-400' : 'text-gray-500'} flex items-center gap-1.5`}>
                <Lock className="w-3.5 h-3.5" />
                {isDark ? '确认密码' : 'Confirm Password'}
              </label>
              <div className="relative">
                <input
                  type={showPassword ? 'text' : 'password'}
                  value={confirmPassword}
                  onChange={e => { setConfirmPassword(e.target.value); setError('') }}
                  onKeyDown={handleKeyDown}
                  className={inputClass}
                  placeholder={isDark ? '再次输入密码' : 'Enter password again'}
                />
              </div>
            </div>
          )}

          {/* Error */}
          {error && (
            <motion.p
              initial={{ opacity: 0, y: -5 }}
              animate={{ opacity: 1, y: 0 }}
              className="text-red-500 text-sm text-center"
            >
              {error}
            </motion.p>
          )}

          {/* Submit */}
          <motion.button
            type="submit"
            disabled={loading}
            className="w-full flex items-center justify-center gap-2 px-6 py-3 rounded-xl font-medium text-sm transition-all bg-gradient-to-r from-blue-500 to-indigo-500 text-white hover:shadow-lg hover:shadow-blue-500/25 disabled:opacity-50"
            whileHover={{ scale: 1.01 }}
            whileTap={{ scale: 0.99 }}
          >
            {loading ? (
              <Loader2 className="w-4 h-4 animate-spin" />
            ) : (
              <Lock className="w-4 h-4" />
            )}
            {loading
              ? (isDark ? '处理中...' : 'Processing...')
              : mode === 'login'
                ? (isDark ? '登录' : 'Login')
                : (isDark ? '设置密码' : 'Set Password')
            }
          </motion.button>
        </form>
      </motion.div>
    </div>
  )
}
