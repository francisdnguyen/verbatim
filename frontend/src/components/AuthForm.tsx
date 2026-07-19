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
  const [confirmPassword, setConfirmPassword] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setError(null)

    if (email.trim() === '' || password === '') {
      setError('Enter an email and password.')
      return
    }
    if (mode === 'register' && password !== confirmPassword) {
      setError('Passwords do not match.')
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
    <div className="mx-auto max-w-md w-full border border-[var(--border)] rounded-xl p-8">
      <div className="text-center mb-6">
        <div className="text-4xl mb-2">🎙️</div>
        <h2 className="text-2xl font-bold text-[var(--text-h)]">
          {mode === 'login' ? 'Welcome to Verbatim' : 'Join Verbatim'}
        </h2>
        <p className="text-sm text-[var(--text)] mt-1">
          {mode === 'login' ? 'Sign in to your account' : 'Sign up to get started'}
        </p>
      </div>

      <form onSubmit={handleSubmit} className="flex flex-col gap-4">
        <div className="flex flex-col gap-1">
          <label className="text-sm font-medium text-[var(--text-h)]">Email</label>
          <input
            type="email"
            placeholder="your@email.com"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            className="border border-[var(--border)] rounded px-3 py-2"
          />
        </div>

        <div className="flex flex-col gap-1">
          <label className="text-sm font-medium text-[var(--text-h)]">Password</label>
          <input
            type="password"
            placeholder={mode === 'register' ? 'At least 8 characters' : 'Enter your password'}
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            className="border border-[var(--border)] rounded px-3 py-2"
          />
        </div>

        {mode === 'register' && (
          <div className="flex flex-col gap-1">
            <label className="text-sm font-medium text-[var(--text-h)]">Confirm password</label>
            <input
              type="password"
              placeholder="Re-enter your password"
              value={confirmPassword}
              onChange={(e) => setConfirmPassword(e.target.value)}
              className="border border-[var(--border)] rounded px-3 py-2"
            />
          </div>
        )}

        {error && <p className="text-red-500 text-sm">{error}</p>}

        <button
          type="submit"
          disabled={submitting}
          className="w-full bg-[var(--accent)] text-white rounded px-4 py-2 font-semibold disabled:opacity-50"
        >
          {submitting
            ? mode === 'login' ? 'Signing in...' : 'Creating account...'
            : mode === 'login' ? 'Sign In' : 'Create Account'}
        </button>
      </form>

      <p className="text-center text-sm mt-4">
        {mode === 'login' ? (
          <>
            Don't have an account?{' '}
            <button type="button" onClick={() => setMode('register')} className="text-[var(--accent)] underline">
              Create one
            </button>
          </>
        ) : (
          <>
            Already have an account?{' '}
            <button type="button" onClick={() => setMode('login')} className="text-[var(--accent)] underline">
              Sign in
            </button>
          </>
        )}
      </p>

      {onBack && (
        <div className="border-t border-[var(--border)] mt-6 pt-4 text-center">
          <button type="button" onClick={onBack} className="text-sm underline">
            ← Back to Home
          </button>
        </div>
      )}
    </div>
  )
}

export default AuthForm
