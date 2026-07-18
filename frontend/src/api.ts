import axios from 'axios'
import type { AskResponse, Video } from './types'

const client = axios.create({
  baseURL: import.meta.env.VITE_API_BASE_URL ?? 'http://localhost:8080',
})

export async function getOrCreateDemoUser(): Promise<{ id: string; email: string }> {
  const { data } = await client.post('/api/demo-user')
  return data
}

export async function submitYouTubeVideo(videoUrl: string, lang: string, userId: string): Promise<Video> {
  const { data } = await client.post('/api/videos', {
    video_url: videoUrl,
    lang,
    user_id: userId,
  })
  return data
}

export async function uploadVideo(file: File, lang: string, userId: string): Promise<Video> {
  const form = new FormData()
  form.append('file', file)
  form.append('lang', lang)
  form.append('user_id', userId)
  const { data } = await client.post('/api/videos/upload', form)
  return data
}

export async function getVideoStatus(videoId: string): Promise<Video> {
  const { data } = await client.get(`/api/videos/${videoId}`)
  return data
}

export async function askQuestion(videoId: string, question: string): Promise<AskResponse> {
  const { data } = await client.post(`/api/videos/${videoId}/ask`, { question })
  return data
}

// getErrorMessage extracts the backend's {"error": "..."} body when present,
// falling back to a generic message for network errors or unexpected shapes.
export function getErrorMessage(err: unknown, fallback: string): string {
  if (axios.isAxiosError(err)) {
    const message = err.response?.data?.error
    if (typeof message === 'string') {
      return message
    }
  }
  return fallback
}
