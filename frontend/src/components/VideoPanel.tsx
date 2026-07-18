import { useEffect, useState } from 'react'
import { askQuestion, getErrorMessage, getVideoStatus } from '../api'
import type { Source, Video } from '../types'

interface VideoPanelProps {
  video: Video
  onVideoUpdate: (video: Video) => void
  onReset: () => void
}

interface QAEntry {
  question: string
  answer: string
  sources: Source[]
}

const POLL_INTERVAL_MS = 2000

function VideoPanel({ video, onVideoUpdate, onReset }: VideoPanelProps) {
  const [history, setHistory] = useState<QAEntry[]>([])
  const [question, setQuestion] = useState('')
  const [asking, setAsking] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (video.status !== 'pending' && video.status !== 'processing') {
      return
    }
    const interval = setInterval(async () => {
      try {
        const updated = await getVideoStatus(video.id)
        onVideoUpdate(updated)
      } catch {
        // Transient poll failure — retry on the next tick rather than
        // surfacing an error for a single missed poll.
      }
    }, POLL_INTERVAL_MS)
    return () => clearInterval(interval)
  }, [video.id, video.status, onVideoUpdate])

  async function handleAsk(e: React.FormEvent) {
    e.preventDefault()
    setError(null)

    if (question.trim() === '') {
      setError('Enter a question.')
      return
    }

    setAsking(true)
    try {
      const response = await askQuestion(video.id, question.trim())
      setHistory((prev) => [...prev, { question: question.trim(), answer: response.answer, sources: response.sources }])
      setQuestion('')
    } catch (err) {
      setError(getErrorMessage(err, 'Something went wrong asking that question. Please try again.'))
    } finally {
      setAsking(false)
    }
  }

  return (
    <div className="mx-auto max-w-2xl w-full flex flex-col gap-4">
      <div className="flex items-center justify-between">
        <p className="text-sm text-[var(--text)]">
          Status: <span className="font-semibold">{video.status}</span>
        </p>
        <button type="button" onClick={onReset} className="text-sm underline">
          Submit another
        </button>
      </div>

      {(video.status === 'pending' || video.status === 'processing') && (
        <p>Processing your video... this page will update automatically.</p>
      )}

      {video.status === 'failed' && (
        <p className="text-red-500">This video failed to process. Try submitting again.</p>
      )}

      {video.status === 'ready' && (
        <>
          <form onSubmit={handleAsk} className="flex flex-col gap-2">
            <input
              type="text"
              value={question}
              onChange={(e) => setQuestion(e.target.value)}
              placeholder="Ask a question about this video..."
              className="border border-[var(--border)] rounded px-3 py-2"
            />
            {error && <p className="text-red-500 text-sm">{error}</p>}
            <button
              type="submit"
              disabled={asking}
              className="bg-[var(--accent)] text-white rounded px-4 py-2 disabled:opacity-50 self-start"
            >
              {asking ? 'Asking...' : 'Ask'}
            </button>
          </form>

          <div className="flex flex-col gap-4">
            {history.map((entry, i) => (
              <div key={i} className="border border-[var(--border)] rounded p-3">
                <p className="font-semibold">{entry.question}</p>
                <p className="mt-1">{entry.answer}</p>
                {entry.sources.length > 0 && (
                  <p className="mt-2 text-sm text-[var(--text)]">
                    Sources:{' '}
                    {entry.sources
                      .map((s) => `${formatTimestamp(s.start_seconds)}-${formatTimestamp(s.end_seconds)}`)
                      .join(', ')}
                  </p>
                )}
              </div>
            ))}
          </div>
        </>
      )}
    </div>
  )
}

function formatTimestamp(seconds: number): string {
  const total = Math.floor(seconds)
  const mm = Math.floor(total / 60)
  const ss = total % 60
  return `${String(mm).padStart(2, '0')}:${String(ss).padStart(2, '0')}`
}

export default VideoPanel
