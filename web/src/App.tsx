import { useState, useEffect } from 'react'
import { motion, AnimatePresence } from 'framer-motion'
import { Dashboard } from './components/Dashboard'
import { ConfigPanel } from './components/ConfigPanel'
import { LoginPage } from './components/LoginPage'
import { Sidebar } from './components/Sidebar'
import { AnimatedBackground } from './components/AnimatedBackground'
import { SettingsProvider, useSettings } from './contexts/SettingsContext'
import { getPasswordStatus } from './lib/api'

type Page = 'dashboard' | 'config'
type AuthState = 'checking' | 'authenticated' | 'login' | 'setup'

function AppInner() {
  const [page, setPage] = useState<Page>('dashboard')
  const [authState, setAuthState] = useState<AuthState>('checking')
  const { resolvedTheme } = useSettings()

  useEffect(() => {
    // Check if user has a valid token
    const token = localStorage.getItem('admin_token')
    if (token) {
      // Token exists, assume authenticated
      setAuthState('authenticated')
      return
    }

    // No token, check if password is set
    getPasswordStatus()
      .then(result => {
        if (result.password_set) {
          setAuthState('login')
        } else {
          setAuthState('setup')
        }
      })
      .catch(() => {
        // If API fails, default to login
        setAuthState('login')
      })
  }, [])

  const handleLoginSuccess = () => {
    setAuthState('authenticated')
  }

  // Show loading while checking auth
  if (authState === 'checking') {
    return (
      <div className={`min-h-screen flex items-center justify-center ${resolvedTheme === 'dark' ? 'bg-gray-950' : 'bg-[#f8f9fc]'}`}>
        <motion.div
          animate={{ rotate: 360 }}
          transition={{ duration: 1, repeat: Infinity, ease: 'linear' }}
          className={`w-8 h-8 border-2 border-t-transparent rounded-full ${resolvedTheme === 'dark' ? 'border-purple-400' : 'border-blue-500'}`}
        />
      </div>
    )
  }

  // Show login/setup page
  if (authState === 'login' || authState === 'setup') {
    return (
      <SettingsProvider>
        <LoginPage mode={authState} onSuccess={handleLoginSuccess} />
      </SettingsProvider>
    )
  }

  // Authenticated - show main app
  return (
    <div className={`min-h-screen bg-grid bg-glow relative overflow-hidden ${resolvedTheme === 'dark' ? 'bg-gray-950' : 'bg-[#f8f9fc]'}`}>
      <AnimatedBackground />

      <div className="relative z-10 flex min-h-screen">
        <Sidebar current={page} onNavigate={setPage} />

        <main className="flex-1 p-6 lg:p-8 overflow-y-auto">
          <AnimatePresence mode="wait">
            {page === 'dashboard' && (
              <motion.div
                key="dashboard"
                initial={{ opacity: 0, y: 20 }}
                animate={{ opacity: 1, y: 0 }}
                exit={{ opacity: 0, y: -20 }}
                transition={{ duration: 0.3, ease: 'easeOut' }}
              >
                <Dashboard />
              </motion.div>
            )}
            {page === 'config' && (
              <motion.div
                key="config"
                initial={{ opacity: 0, y: 20 }}
                animate={{ opacity: 1, y: 0 }}
                exit={{ opacity: 0, y: -20 }}
                transition={{ duration: 0.3, ease: 'easeOut' }}
              >
                <ConfigPanel />
              </motion.div>
            )}
          </AnimatePresence>
        </main>
      </div>
    </div>
  )
}

export default function App() {
  return (
    <SettingsProvider>
      <AppInner />
    </SettingsProvider>
  )
}
