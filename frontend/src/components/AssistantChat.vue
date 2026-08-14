<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { api } from '../api'
import { formatClock, formatDate } from '../format'
import { answerSegments } from '../richText'
import AppIcon from './AppIcon.vue'
import type { AppSnapshot } from '../types'

const props = defineProps<{ snapshot: AppSnapshot }>()

const question = ref('')
const sending = ref(false)
const actionError = ref('')
const copiedId = ref<number | null>(null)
const list = ref<HTMLElement | null>(null)
let copiedTimer: number | undefined

const assistant = computed(() => props.snapshot.assistant)
const conversations = computed(() => assistant.value.conversations)
// The thread itself has no limit: once older turns stop fitting one request
// they are folded into a summary that keeps travelling with every question.
const digestedTurns = computed(() => assistant.value.digestedTurns ?? 0)
const notice = computed(() => {
  const state = assistant.value
  if (actionError.value) return { tone: 'error', text: actionError.value, settings: false }
  if (state.lastError) return { tone: 'error', text: state.lastError, settings: false }
  if (!state.enabled) {
    return { tone: 'info', text: '未开启：开启后已完成的字幕会发送到你配置的模型接口。', settings: true }
  }
  if (state.configError) return { tone: 'warn', text: state.configError, settings: true }
  if (state.toolNote) return { tone: 'info', text: state.toolNote, settings: false }
  return null
})

const run = async (operation: () => Promise<void>) => {
  actionError.value = ''
  try {
    await operation()
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : String(error)
  }
}

const ask = async () => {
  const text = question.value.trim()
  if (!text || sending.value) return
  sending.value = true
  actionError.value = ''
  try {
    await api.askAssistant(text)
    question.value = ''
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : String(error)
  } finally {
    sending.value = false
  }
}

// 引用的链接在系统浏览器里打开：主窗口一旦真的导航过去，整个界面就没了。
// 所以链接没有 href，点击和回车都只是调用 openExternal。
const openLink = (url: string) => run(() => api.openExternal(url))

const copy = async (id: number, text: string) => {
  await run(() => api.copyText(text))
  if (actionError.value) return
  copiedId.value = id
  window.clearTimeout(copiedTimer)
  copiedTimer = window.setTimeout(() => (copiedId.value = null), 1400)
}

// An answer streams in under the question that is already on screen, so follow
// the bottom the way a chat log does.
watch(
  () => conversations.value.map((item) => item.status).join(','),
  async () => {
    await nextTick()
    const node = list.value
    if (node) node.scrollTop = node.scrollHeight
  },
)

onBeforeUnmount(() => window.clearTimeout(copiedTimer))
</script>

<template>
  <section class="pane chat-pane">
    <header class="pane-head">
      <h2>AI 对话</h2>
      <span class="result-count">
        <template v-if="assistant.answering">回答中…</template>
        <template v-else>{{ conversations.length }} 条</template>
      </span>
      <button type="button" class="icon-btn" title="AI 助手设置" aria-label="AI 助手设置" @click="run(api.openSettings)">
        <AppIcon name="settings" :size="15" />
      </button>
    </header>

    <p v-if="notice" class="pane-notice" :class="notice.tone" :role="notice.tone === 'error' ? 'alert' : 'status'">
      <AppIcon :name="notice.tone === 'info' ? 'info' : 'alert'" :size="14" />
      <span>{{ notice.text }}</span>
      <button v-if="notice.settings" type="button" class="link-btn" @click="run(api.openSettings)">去设置</button>
    </p>

    <div ref="list" class="chat-list scroll-area">
      <p v-if="digestedTurns > 0" class="thread-digest" :title="assistant.threadDigest">
        <AppIcon name="info" :size="13" />
        <span>更早的 {{ digestedTurns }} 轮已压缩成摘要继续参与追问（悬停查看）</span>
      </p>

      <article v-for="item in conversations" :key="item.id" class="qa-row">
        <header>
          <span class="qa-tag" :class="item.source">{{ item.source === 'auto' ? '识别到提问' : '我的提问' }}</span>
          <time :title="formatDate(item.time)">{{ formatClock(item.time) }}</time>
          <button
            v-if="item.status === 'ready'"
            class="icon-btn"
            :title="copiedId === item.id ? '已复制' : '复制回答'"
            :aria-label="copiedId === item.id ? '已复制' : '复制回答'"
            @click="copy(item.id, item.answer)"
          >
            <AppIcon :name="copiedId === item.id ? 'check' : 'copy'" :size="14" />
          </button>
        </header>
        <p class="qa-question">{{ item.question }}</p>
        <p v-if="item.status === 'pending'" class="qa-answer pending">正在生成回答…</p>
        <p v-else-if="item.status === 'failed'" class="qa-answer failed">{{ item.error }}</p>
        <p v-else class="qa-answer">
          <template v-for="(segment, index) in answerSegments(item.answer)" :key="index"
            ><a
              v-if="segment.kind === 'link'"
              class="qa-link"
              role="link"
              tabindex="0"
              :title="segment.url"
              @click="openLink(segment.url)"
              @keydown.enter.prevent="openLink(segment.url)"
              @keydown.space.prevent="openLink(segment.url)"
              >{{ segment.text }}</a
            ><template v-else>{{ segment.text }}</template></template
          >
        </p>
      </article>

      <div v-if="conversations.length === 0" class="empty-state">
        <AppIcon name="chat" :size="24" />
        <strong>还没有问答</strong>
        <span>字幕里出现问句时会自动回答，也可以在下面直接追问。</span>
      </div>
    </div>

    <form class="chat-ask" @submit.prevent="ask">
      <input
        v-model="question"
        class="input"
        type="text"
        :disabled="!assistant.enabled"
        :placeholder="assistant.enabled ? '基于最近的字幕追问' : '开启 AI 助手后可以在这里追问'"
        aria-label="向 AI 助手提问"
      />
      <button
        class="icon-btn accent"
        type="submit"
        :disabled="!assistant.enabled || !question.trim() || sending"
        title="发送"
        aria-label="发送"
      >
        <AppIcon name="send" :size="16" />
      </button>
    </form>
  </section>
</template>
