interface LandingProps {
  onNavigate: (mode: 'login' | 'register') => void
}

function Landing({ onNavigate }: LandingProps) {
  return (
    <div className="w-full">
      <nav className="flex items-center justify-between px-6 py-4 max-w-5xl mx-auto">
        <span className="text-xl font-semibold text-[var(--text-h)]">Verbatim</span>
        <div className="flex items-center gap-4">
          <button type="button" onClick={() => onNavigate('login')} className="text-sm text-[var(--text-h)]">
            Log in →
          </button>
          <button
            type="button"
            onClick={() => onNavigate('register')}
            className="bg-[var(--accent)] text-white rounded px-4 py-2 text-sm font-semibold"
          >
            Register
          </button>
        </div>
      </nav>

      <div className="flex flex-col items-center text-center px-4 py-24 gap-6">
        <h1 className="text-5xl font-bold text-[var(--text-h)]">Ask your videos anything.</h1>
        <div className="text-6xl font-extrabold text-[var(--accent)]">Verbatim</div>
        <p className="max-w-lg text-[var(--text)]">
          Upload a video or paste a YouTube link. Get instant answers, cited to the exact moment they're said.
        </p>
        <button
          type="button"
          onClick={() => onNavigate('register')}
          className="bg-[var(--accent)] text-white rounded px-6 py-3 font-semibold"
        >
          Get started
        </button>
      </div>
    </div>
  )
}

export default Landing
