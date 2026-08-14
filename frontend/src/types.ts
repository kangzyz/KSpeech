export type JobStatus = 'stopped' | 'running' | 'paused'

export interface HistoryEntry {
  id: number
  time: string
  text: string
  /** Speaker label of the audio input this sentence came from. */
  speaker?: string
  /** Plugin key of that audio input, used to keep one speaker one colour. */
  channel?: string
}

/** One audio input's live caption line. */
export interface ChannelState {
  key: string
  label: string
  text: string
}

export interface PluginOption {
  key: string
  name: string
  description: string
  available: boolean
  needsAudio?: boolean
  fields?: ConfigField[]
  /** Audio sources only: whether this input is captured, and under what name. */
  enabled?: boolean
  label?: string
}

/** One labeled audio input, as the settings page sends it back. */
export interface AudioChannelInput {
  source: string
  label: string
}

export interface ConfigFieldOption {
  value: string | number | boolean
  label: string
}

export interface ConfigField {
  key: string
  label: string
  type: 'text' | 'password' | 'file' | 'folder' | 'number' | 'checkbox' | 'select' | 'message'
  value?: unknown
  options?: ConfigFieldOption[]
  hint?: string
}

export interface ResourceItem {
  id: string
  name: string
  description: string
  displayVersion: string
  local: boolean
  removable: boolean
  needsUpdate: boolean
  installable: boolean
  busy?: boolean
  progress?: number
  error?: string
}

export interface AssistantInsight {
  id: number
  time: string
  text: string
}

export interface AssistantConversation {
  id: number
  time: string
  question: string
  answer: string
  source: 'auto' | 'manual'
  status: 'pending' | 'ready' | 'failed'
  error?: string
}

export interface AssistantState {
  enabled: boolean
  configured: boolean
  summarize: boolean
  autoAnswer: boolean
  model: string
  provider?: string
  tools: string[]
  toolNote?: string
  summarizing: boolean
  answering: boolean
  pendingLines: number
  insights: AssistantInsight[]
  conversations: AssistantConversation[]
  /** 更早的问答被压缩成的摘要，以及它覆盖了多少轮。对话本身不设上限。 */
  threadDigest?: string
  digestedTurns?: number
  configError?: string
  lastError?: string
}

export interface AppSnapshot {
  status: JobStatus
  runningSeconds: number
  text: string
  channels: ChannelState[]
  locked: boolean
  history: HistoryEntry[]
  config: Record<string, unknown>
  audioSources: PluginOption[]
  recognizers: PluginOption[]
  resources: ResourceItem[]
  /** Installed punctuation models, valued by the file the recognizer loads. */
  punctuationModels: ConfigFieldOption[]
  assistant: AssistantState
  version: string
  commit: string
  platform: string
  lastError?: string
}

export interface LiveState {
  status: JobStatus
  runningSeconds: number
  text: string
  channels: ChannelState[]
  lastError?: string
}

export interface AppEvent {
  snapshot: AppSnapshot
}

export interface AppNotification {
  title: string
  message: string
  level: 'info' | 'warning' | 'error'
}
