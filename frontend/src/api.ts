import axios from 'axios'
import type { AskResponse, AuthResponse, User, Video } from './types'

const client = axios.create({
  baseURL: import.meta.env.VITE_API_BASE_URL ?? 'http://localhost:8080',
  // The session lives entirely in an httpOnly cookie now (never in JS-visible
  // storage), so every request needs the browser to actually attach it.
  withCredentials: true,
})

// The CSRF token lives only in memory, never a cookie or localStorage.
// Why not a second cookie (the usual double-submit shape): the frontend
// (Vercel) and backend (EC2) are on genuinely different domains in
// production, and a cookie is only ever readable by script running on the
// domain that set it — this page's JS can never read a cookie the backend
// set, no matter its SameSite/Secure attributes. The backend instead signs
// this same value into the session cookie's JWT and hands it back in the
// JSON body of register/login/me, which this page's own fetch legitimately
// reads. Lost on a full page reload by design — getCurrentUser() re-learns
// it from a fresh /me call every time the app boots.
let csrfToken: string | null = null

client.interceptors.request.use((config) => {
  const method = config.method?.toUpperCase()
  if (method && method !== 'GET' && method !== 'HEAD' && method !== 'OPTIONS' && csrfToken) {
    config.headers['X-CSRF-Token'] = csrfToken
  }
  return config
})

export async function register(email: string, password: string): Promise<AuthResponse> {
  const { data } = await client.post<AuthResponse>('/api/auth/register', { email, password })
  csrfToken = data.csrf_token
  return data
}

export async function login(email: string, password: string): Promise<AuthResponse> {
  const { data } = await client.post<AuthResponse>('/api/auth/login', { email, password })
  csrfToken = data.csrf_token
  return data
}

export async function logout(): Promise<void> {
  await client.post('/api/auth/logout')
  csrfToken = null
}

// getCurrentUser asks the backend who (if anyone) the session cookie belongs
// to — the session-bootstrap call on page load, now that the token itself
// isn't readable from JS to check locally. Also re-establishes the in-memory
// CSRF token, which a fresh page load never has. Returns null on a 401
// (no/expired session) rather than throwing, since "not logged in" is an
// expected state, not an error.
export async function getCurrentUser(): Promise<User | null> {
  try {
    const { data } = await client.get<AuthResponse>('/api/auth/me')
    csrfToken = data.csrf_token
    return data.user
  } catch (err) {
    if (axios.isAxiosError(err) && err.response?.status === 401) {
      csrfToken = null
      return null
    }
    throw err
  }
}

export async function submitYouTubeVideo(videoUrl: string, lang: string): Promise<Video> {
  const { data } = await client.post('/api/videos', {
    video_url: videoUrl,
    lang,
  })
  return data
}

export async function uploadVideo(file: File, lang: string): Promise<Video> {
  const form = new FormData()
  form.append('file', file)
  form.append('lang', lang)
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
