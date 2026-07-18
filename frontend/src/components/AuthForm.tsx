import { useState } from 'react'
import { getErrorMessage, login, register } from '../api'
import type { AuthResponse } from '../types'

interface AuthFormProps {
  onAuthenticated: (auth: AuthResponse) => void
  initialMode?: 'login' | 'register'
  onBack?: () => void
}

type Mode = 'login' | 'register'

function AuthForm({ onAuthenticated, initialMode = 'login', onBack }: AuthFormProps) {
  const [mode, setMode] = useState<Mode>(initialMode)
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setError(null)

    if (email.trim() === '' || password === '') {
      setError('Enter an email and password.')
      return
    }

    setSubmitting(true)
    try {
      const auth = mode === 'login' ? await login(email.trim(), password) : await register(email.trim(), password)
      onAuthenticated(auth)
    } catch (err) {
      setError(getErrorMessage(err, 'Something went wrong. Please try again.'))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="mx-auto max-w-md w-full">
      {onBack && (
        <button type="button" onClick={onBack} className="text-sm mb-4 underline">
          ← Back
        </button>
      )}

      <div className="flex gap-2 mb-4">
        <button
          type="button"
          onClick={() => setMode('login')}
          className={`px-4 py-2 rounded ${mode === 'login' ? 'bg-[var(--accent)] text-white' : 'bg-[var(--code-bg)]'}`}
        >
          Log in
        </button>
        <button
          type="button"
          onClick={() => setMode('register')}
          className={`px-4 py-2 rounded ${mode === 'register' ? 'bg-[var(--accent)] text-white' : 'bg-[var(--code-bg)]'}`}
        >
          Register
        </button>
      </div>

      <form onSubmit={handleSubmit} className="flex flex-col gap-3">
        <input
          type="email"
          placeholder="Email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          className="border border-[var(--border)] rounded px-3 py-2"
        />
        <input
          type="password"
          placeholder="Password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          className="border border-[var(--border)] rounded px-3 py-2"
        />

        {error && <p className="text-red-500 text-sm">{error}</p>}

        <button
          type="submit"
          disabled={submitting}
          className="bg-[var(--accent)] text-white rounded px-4 py-2 disabled:opacity-50"
        >
          {submitting ? (mode === 'login' ? 'Logging in...' : 'Registering...') : mode === 'login' ? 'Log in' : 'Register'}
        </button>
      </form>
    </div>
  )
}

export default AuthForm
