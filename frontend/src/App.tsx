import { useEffect, useState } from 'react'
import { getOrCreateDemoUser } from './api'
import SubmitForm from './components/SubmitForm'
import VideoPanel from './components/VideoPanel'
import type { Video } from './types'
import './App.css'

const USER_ID_STORAGE_KEY = 'verbatim_user_id'

function App() {
  const [userId, setUserId] = useState<string | null>(() => localStorage.getItem(USER_ID_STORAGE_KEY))
  const [video, setVideo] = useState<Video | null>(null)

  useEffect(() => {
    if (userId) {
      return
    }
    getOrCreateDemoUser().then((user) => {
      localStorage.setItem(USER_ID_STORAGE_KEY, user.id)
      setUserId(user.id)
    })
  }, [userId])

  return (
    <div className="min-h-screen flex flex-col items-center justify-center px-4 py-12">
      <h1 className="text-3xl font-semibold mb-8">Verbatim</h1>

      {!userId && <p>Setting things up...</p>}

      {userId && !video && <SubmitForm userId={userId} onSubmitted={setVideo} />}

      {userId && video && (
        <VideoPanel video={video} onVideoUpdate={setVideo} onReset={() => setVideo(null)} />
      )}
    </div>
  )
}

export default App
