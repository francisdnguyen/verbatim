import { useEffect, useState } from 'react'
import { getCurrentUser, logout } from './api'
import AuthForm from './components/AuthForm'
import Landing from './components/Landing'
import SubmitForm from './components/SubmitForm'
import ThemeToggle from './components/ThemeToggle'
import VideoPanel from './components/VideoPanel'
import type { User, Video } from './types'
import './App.css'

type LoggedOutView = 'landing' | 'login' | 'register'

function App() {
  const [user, setUser] = useState<User | null>(null)
  // The session lives in an httpOnly cookie now, invisible to JS, so on
  // load we have to ask the backend who (if anyone) it belongs to rather
  // than reading a token out of localStorage synchronously.
  const [checkingSession, setCheckingSession] = useState(true)
  const [video, setVideo] = useState<Video | null>(null)
  const [view, setView] = useState<LoggedOutView>('landing')

  useEffect(() => {
    getCurrentUser()
      .then(setUser)
      .finally(() => setCheckingSession(false))
  }, [])

  async function handleLogout() {
    await logout()
    setUser(null)
    setVideo(null)
    setView('landing')
  }

  if (checkingSession) {
    return null
  }

  return (
    <div className="min-h-screen flex flex-col items-center justify-center px-4 py-12">
      <ThemeToggle />

      {user && (
        <div className="flex items-center justify-between w-full max-w-2xl mb-8">
          <h1 className="text-3xl font-semibold">Verbatim</h1>
          <div className="flex items-center gap-3">
            <span className="text-sm text-[var(--text)]">{user.email}</span>
            <button type="button" onClick={handleLogout} className="text-sm underline">
              Log out
            </button>
          </div>
        </div>
      )}

      {!user && view === 'landing' && <Landing onNavigate={setView} />}

      {!user && view !== 'landing' && (
        <AuthForm
          initialMode={view}
          onBack={() => setView('landing')}
          onAuthenticated={(auth) => setUser(auth.user)}
        />
      )}

      {user && !video && <SubmitForm onSubmitted={setVideo} />}

      {user && video && (
        <VideoPanel video={video} onVideoUpdate={setVideo} onReset={() => setVideo(null)} />
      )}
    </div>
  )
}

export default App
