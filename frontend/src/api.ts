import axios from 'axios'
import type { AskResponse, AuthResponse, User, Video } from './types'

// Must match the backend's middleware.CSRFCookieName exactly.
const CSRF_COOKIE_NAME = 'verbatim_csrf'

const client = axios.create({
  baseURL: import.meta.env.VITE_API_BASE_URL ?? 'http://localhost:8080',
  // The session lives entirely in an httpOnly cookie now (never in JS-visible
  // storage), so every request needs the browser to actually attach it.
  withCredentials: true,
})

// getCookie reads a plain (non-httpOnly) cookie by name — used only for the
// CSRF cookie, which is deliberately readable by JS so it can be echoed back
// in a header (the double-submit pattern the backend's AuthMiddleware checks).
function getCookie(name: string): string | null {
  const match = document.cookie.match(new RegExp('(?:^|; )' + name + '=([^;]*)'))
  return match ? decodeURIComponent(match[1]) : null
}

client.interceptors.request.use((config) => {
  const method = config.method?.toUpperCase()
  if (method && method !== 'GET' && method !== 'HEAD' && method !== 'OPTIONS') {
    const csrfToken = getCookie(CSRF_COOKIE_NAME)
    if (csrfToken) {
      config.headers['X-CSRF-Token'] = csrfToken
    }
  }
  return config
})

export async function register(email: string, password: string): Promise<AuthResponse> {
  const { data } = await client.post('/api/auth/register', { email, password })
  return data
}

export async function login(email: string, password: string): Promise<AuthResponse> {
  const { data } = await client.post('/api/auth/login', { email, password })
  return data
}

export async function logout(): Promise<void> {
  await client.post('/api/auth/logout')
}

// getCurrentUser asks the backend who (if anyone) the session cookie belongs
// to — the session-bootstrap call on page load, now that the token itself
// isn't readable from JS to check locally. Returns null on a 401 (no/expired
// session) rather than throwing, since "not logged in" is an expected state,
// not an error.
export async function getCurrentUser(): Promise<User | null> {
  try {
    const { data } = await client.get<AuthResponse>('/api/auth/me')
    return data.user
  } catch (err) {
    if (axios.isAxiosError(err) && err.response?.status === 401) {
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
