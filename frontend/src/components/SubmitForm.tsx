import { useState } from 'react'
import { getErrorMessage, submitYouTubeVideo, uploadVideo } from '../api'
import type { Video } from '../types'

interface SubmitFormProps {
  userId: string
  onSubmitted: (video: Video) => void
}

type Mode = 'youtube' | 'upload'

function SubmitForm({ userId, onSubmitted }: SubmitFormProps) {
  const [mode, setMode] = useState<Mode>('youtube')
  const [videoUrl, setVideoUrl] = useState('')
  const [file, setFile] = useState<File | null>(null)
  const [lang, setLang] = useState('en')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setError(null)

    if (mode === 'youtube' && videoUrl.trim() === '') {
      setError('Enter a YouTube URL.')
      return
    }
    if (mode === 'upload' && !file) {
      setError('Choose a file to upload.')
      return
    }

    setSubmitting(true)
    try {
      const video =
        mode === 'youtube'
          ? await submitYouTubeVideo(videoUrl.trim(), lang, userId)
          : await uploadVideo(file as File, lang, userId)
      onSubmitted(video)
    } catch (err) {
      setError(getErrorMessage(err, 'Something went wrong submitting the video. Please try again.'))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="mx-auto max-w-md w-full">
      <div className="flex gap-2 mb-4">
        <button
          type="button"
          onClick={() => setMode('youtube')}
          className={`px-4 py-2 rounded ${mode === 'youtube' ? 'bg-[var(--accent)] text-white' : 'bg-[var(--code-bg)]'}`}
        >
          YouTube URL
        </button>
        <button
          type="button"
          onClick={() => setMode('upload')}
          className={`px-4 py-2 rounded ${mode === 'upload' ? 'bg-[var(--accent)] text-white' : 'bg-[var(--code-bg)]'}`}
        >
          Upload file
        </button>
      </div>

      <form onSubmit={handleSubmit} className="flex flex-col gap-3">
        {mode === 'youtube' ? (
          <input
            type="text"
            placeholder="https://www.youtube.com/watch?v=..."
            value={videoUrl}
            onChange={(e) => setVideoUrl(e.target.value)}
            className="border border-[var(--border)] rounded px-3 py-2"
          />
        ) : (
          <input
            type="file"
            accept="video/*,audio/*"
            onChange={(e) => setFile(e.target.files?.[0] ?? null)}
            className="border border-[var(--border)] rounded px-3 py-2"
          />
        )}

        <input
          type="text"
          value={lang}
          onChange={(e) => setLang(e.target.value)}
          placeholder="Language (e.g. en)"
          className="border border-[var(--border)] rounded px-3 py-2"
        />

        {error && <p className="text-red-500 text-sm">{error}</p>}

        <button
          type="submit"
          disabled={submitting}
          className="bg-[var(--accent)] text-white rounded px-4 py-2 disabled:opacity-50"
        >
          {submitting ? 'Submitting...' : 'Submit'}
        </button>
      </form>
    </div>
  )
}

export default SubmitForm
