export type VideoSourceType = 'youtube' | 'upload'
export type VideoStatus = 'pending' | 'processing' | 'ready' | 'failed'

export interface Video {
  id: string
  user_id: string
  source_type: VideoSourceType
  source_url?: string
  s3_key?: string
  title?: string
  status: VideoStatus
  created_at: string
  updated_at: string
}

export interface Source {
  chunk_index: number
  start_seconds: number
  end_seconds: number
  chunk_text: string
}

export interface AskResponse {
  answer: string
  sources: Source[]
}

export interface User {
  id: string
  email: string
  created_at: string
}

export interface AuthResponse {
  user: User
  csrf_token: string
}
