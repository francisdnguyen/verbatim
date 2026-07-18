import { useState } from 'react'
import { TOKEN_STORAGE_KEY } from './api'
import AuthForm from './components/AuthForm'
import Landing from './components/Landing'
import SubmitForm from './components/SubmitForm'
import ThemeToggle from './components/ThemeToggle'
import VideoPanel from './components/VideoPanel'
import type { User, Video } from './types'
import './App.css'

type LoggedOutView = 'landing' | 'login' | 'register'

function App() {
  const [token, setToken] = useState<string | null>(() => localStorage.getItem(TOKEN_STORAGE_KEY))
  const [user, setUser] = useState<User | null>(null)
  const [video, setVideo] = useState<Video | null>(null)
  const [view, setView] = useState<LoggedOutView>('landing')

  function handleLogout() {
    localStorage.removeItem(TOKEN_STORAGE_KEY)
    setToken(null)
    setUser(null)
    setVideo(null)
    setView('landing')
  }

  return (
    <div className="min-h-screen flex flex-col items-center justify-center px-4 py-12">
      <ThemeToggle />

      {token && (
        <div className="flex items-center justify-between w-full max-w-2xl mb-8">
          <h1 className="text-3xl font-semibold">Verbatim</h1>
          <div className="flex items-center gap-3">
            {user && <span className="text-sm text-[var(--text)]">{user.email}</span>}
            <button type="button" onClick={handleLogout} className="text-sm underline">
              Log out
            </button>
          </div>
        </div>
      )}

      {!token && view === 'landing' && <Landing onNavigate={setView} />}

      {!token && view !== 'landing' && (
        <AuthForm
          initialMode={view}
          onBack={() => setView('landing')}
          onAuthenticated={(auth) => {
            localStorage.setItem(TOKEN_STORAGE_KEY, auth.token)
            setToken(auth.token)
            setUser(auth.user)
          }}
        />
      )}

      {token && !video && <SubmitForm onSubmitted={setVideo} />}

      {token && video && (
        <VideoPanel video={video} onVideoUpdate={setVideo} onReset={() => setVideo(null)} />
      )}
    </div>
  )
}

export default App
