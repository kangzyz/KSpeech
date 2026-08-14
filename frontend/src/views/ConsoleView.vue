<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { api } from '../api'
import { consoleStyleFrom } from '../captionStyle'
import { formatDuration } from '../format'
import AppIcon from '../components/AppIcon.vue'
import AssistantChat from '../components/AssistantChat.vue'
import InsightList from '../components/InsightList.vue'
import MessageStream from '../components/MessageStream.vue'
import type { AppSnapshot } from '../types'

const props = defineProps<{ snapshot: AppSnapshot }>()

const actionError = ref('')
const hint = ref('')
const starting = ref(false)
const cancelRequested = ref(false)
const shell = ref<HTMLElement | null>(null)
let hintTimer: number | undefined
let observer: ResizeObserver | undefined

/*
 * Pane geometry. The message stream takes whatever is left, so only the two
 * side panes carry a width. Both are remembered per machine: they are window
 * furniture rather than recognition settings, and the configuration store is
 * reserved for the latter.
 */
const LAYOUT_KEY = 'kspeech.console.layout'
const MIN_PANE = 190
const MAX_PANE = 560
const STREAM_MIN = 240
const SPLIT = 6

const showInsights = ref(true)
const showChat = ref(true)
const insightsWidth = ref(250)
const chatWidth = ref(300)
const shellWidth = ref(0)

const readLayout = () => {
  try {
    const raw = window.localStorage.getItem(LAYOUT_KEY)
    if (!raw) return
    const stored = JSON.parse(raw) as Partial<Record<string, unknown>>
    if (typeof stored.insights === 'boolean') showInsights.value = stored.insights
    if (typeof stored.chat === 'boolean') showChat.value = stored.chat
    if (typeof stored.insightsWidth === 'number') insightsWidth.value = stored.insightsWidth
    if (typeof stored.chatWidth === 'number') chatWidth.value = stored.chatWidth
  } catch {
    // A layout that cannot be read is not worth reporting; the defaults apply.
  }
}

const writeLayout = () => {
  try {
    window.localStorage.setItem(
      LAYOUT_KEY,
      JSON.stringify({
        insights: showInsights.value,
        chat: showChat.value,
        insightsWidth: insightsWidth.value,
        chatWidth: chatWidth.value,
      }),
    )
  } catch {
    // Storage can be denied; the layout then simply lasts for this run.
  }
}

/*
 * A side pane is shown only when the stream still gets a usable column after
 * it. Shrinking the window therefore folds the panes away instead of crushing
 * all three, and widening it brings back the ones the user had open.
 */
const insightsFits = computed(
  () => shellWidth.value === 0 || shellWidth.value >= STREAM_MIN + insightsWidth.value + SPLIT,
)
const insightsVisible = computed(() => showInsights.value && insightsFits.value)
const chatFits = computed(() => {
  const used = STREAM_MIN + (insightsVisible.value ? insightsWidth.value + SPLIT : 0)
  return shellWidth.value === 0 || shellWidth.value >= used + chatWidth.value + SPLIT
})
const chatVisible = computed(() => showChat.value && chatFits.value)

const style = computed(() => ({
  ...consoleStyleFrom(props.snapshot.config),
  '--pane-insights': `${insightsWidth.value}px`,
  '--pane-chat': `${chatWidth.value}px`,
}))

const time = computed(() => formatDuration(props.snapshot.runningSeconds))
const running = computed(() => props.snapshot.status === 'running')
const idle = computed(() => props.snapshot.status === 'stopped' && !starting.value)

const primary = computed(() => {
  if (starting.value) return { icon: 'stop', title: '取消启动', tone: 'stop' }
  if (running.value) return { icon: 'pause', title: '暂停识别', tone: 'accent' }
  if (props.snapshot.status === 'paused') return { icon: 'play', title: '继续识别', tone: 'accent' }
  return { icon: 'play', title: '开始识别', tone: 'accent' }
})

const showHint = (message: string) => {
  hint.value = message
  window.clearTimeout(hintTimer)
  hintTimer = window.setTimeout(() => (hint.value = ''), 5000)
}

const run = async (operation: () => Promise<void>) => {
  actionError.value = ''
  try {
    await operation()
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : String(error)
  }
}

const togglePrimary = async () => {
  if (starting.value) {
    cancelRequested.value = true
    await run(api.stop)
    return
  }
  if (running.value) {
    await run(api.pause)
    return
  }
  actionError.value = ''
  starting.value = true
  cancelRequested.value = false
  try {
    await api.start()
  } catch (error) {
    // A user-requested cancel surfaces as a start failure; it is not an error.
    if (!cancelRequested.value) {
      actionError.value = error instanceof Error ? error.message : String(error)
    }
  } finally {
    starting.value = false
    cancelRequested.value = false
  }
}

const toggleLock = async () => {
  const next = !props.snapshot.locked
  if (next && !props.snapshot.config['notification.ShownLockUsage']) {
    showHint('窗口已锁定：位置和大小固定住了，字幕照样可以翻看。再点一次或从托盘菜单解锁。')
    // Best effort: the backend rejects notification writes during a run, and a
    // repeated hint is harmless.
    api.setConfig('notification.ShownLockUsage', true).catch(() => {})
  }
  await run(() => api.setLocked(next))
}

const togglePane = (pane: 'insights' | 'chat') => {
  if (pane === 'insights') showInsights.value = !showInsights.value
  else showChat.value = !showChat.value
  writeLayout()
}

const clampPane = (pane: 'insights' | 'chat', value: number) => {
  const other =
    pane === 'insights'
      ? chatVisible.value
        ? chatWidth.value + SPLIT
        : 0
      : insightsVisible.value
        ? insightsWidth.value + SPLIT
        : 0
  const room = Math.max(MIN_PANE, shellWidth.value - STREAM_MIN - other - SPLIT)
  return Math.round(Math.min(Math.max(value, MIN_PANE), Math.min(MAX_PANE, room)))
}

/** Drags one divider. Both panes grow leftwards, away from the message stream. */
const startPaneResize = (pane: 'insights' | 'chat', event: PointerEvent) => {
  const handle = event.currentTarget as HTMLElement
  event.preventDefault()
  handle.setPointerCapture(event.pointerId)
  const startX = event.clientX
  const startWidth = pane === 'insights' ? insightsWidth.value : chatWidth.value
  const move = (moveEvent: PointerEvent) => {
    const next = clampPane(pane, startWidth - (moveEvent.clientX - startX))
    if (pane === 'insights') insightsWidth.value = next
    else chatWidth.value = next
  }
  const finish = () => {
    handle.removeEventListener('pointermove', move)
    handle.removeEventListener('pointerup', finish)
    handle.removeEventListener('pointercancel', finish)
    handle.releasePointerCapture(event.pointerId)
    writeLayout()
  }
  handle.addEventListener('pointermove', move)
  handle.addEventListener('pointerup', finish)
  handle.addEventListener('pointercancel', finish)
}

onMounted(() => {
  readLayout()
  const node = shell.value
  if (!node) return
  shellWidth.value = node.clientWidth
  observer = new ResizeObserver(() => (shellWidth.value = node.clientWidth))
  observer.observe(node)
})

onBeforeUnmount(() => {
  window.clearTimeout(hintTimer)
  observer?.disconnect()
})
</script>

<template>
  <section ref="shell" class="console" :class="{ locked: snapshot.locked }" :style="style">
    <nav
      class="console-bar"
      :style="{ '--wails-draggable': snapshot.locked ? 'no-drag' : 'drag' }"
      aria-label="KSpeech 控制"
    >
      <button
        type="button"
        class="icon-btn"
        :class="primary.tone"
        :title="primary.title"
        :aria-label="primary.title"
        @click="togglePrimary"
      >
        <AppIcon :name="primary.icon" :size="16" />
      </button>

      <button
        v-if="!starting && !idle"
        type="button"
        class="icon-btn stop"
        title="停止识别"
        aria-label="停止识别"
        @click="run(api.stop)"
      >
        <AppIcon name="stop" :size="14" />
      </button>

      <span class="run-time">
        <span v-if="starting || !idle" class="record-dot" :class="{ paused: !running }" />
        {{ starting ? '启动中' : idle ? '未开始' : time }}
      </span>

      <span class="bar-gap" />

      <button
        type="button"
        class="icon-btn"
        :class="{ active: insightsVisible }"
        :disabled="!insightsFits"
        :title="insightsFits ? '要点总结' : '窗口太窄，拉宽后可以显示要点总结'"
        aria-label="要点总结"
        @click="togglePane('insights')"
      >
        <AppIcon name="list" :size="16" />
      </button>
      <button
        type="button"
        class="icon-btn"
        :class="{ active: chatVisible }"
        :disabled="!chatFits"
        :title="chatFits ? 'AI 对话' : '窗口太窄，拉宽后可以显示 AI 对话'"
        aria-label="AI 对话"
        @click="togglePane('chat')"
      >
        <AppIcon name="chat" :size="16" />
      </button>

      <span class="divider" />

      <button
        type="button"
        class="icon-btn"
        :class="{ active: snapshot.locked }"
        :title="snapshot.locked ? '解锁窗口' : '锁定窗口位置与大小'"
        :aria-label="snapshot.locked ? '解锁窗口' : '锁定窗口'"
        @click="toggleLock"
      >
        <AppIcon :name="snapshot.locked ? 'unlock' : 'lock'" :size="16" />
      </button>
      <button
        type="button"
        class="icon-btn"
        title="隐藏窗口（点击任务栏托盘图标恢复）"
        aria-label="隐藏窗口"
        @click="run(api.hideConsole)"
      >
        <AppIcon name="minimise" :size="16" />
      </button>
      <button type="button" class="icon-btn" title="设置" aria-label="设置" @click="run(api.openSettings)">
        <AppIcon name="settings" :size="16" />
      </button>
    </nav>

    <p v-if="actionError || snapshot.lastError" class="console-toast error" role="alert">
      <AppIcon name="alert" :size="15" />
      <span>{{ actionError || snapshot.lastError }}</span>
    </p>
    <p v-else-if="hint" class="console-toast" role="status">
      <AppIcon name="lock" :size="15" />
      <span>{{ hint }}</span>
    </p>

    <div class="console-body">
      <MessageStream :snapshot="snapshot" />

      <template v-if="insightsVisible">
        <div
          class="pane-split"
          role="separator"
          aria-orientation="vertical"
          aria-label="调整要点总结宽度"
          @pointerdown="startPaneResize('insights', $event)"
        />
        <InsightList :snapshot="snapshot" />
      </template>

      <template v-if="chatVisible">
        <div
          class="pane-split"
          role="separator"
          aria-orientation="vertical"
          aria-label="调整 AI 对话宽度"
          @pointerdown="startPaneResize('chat', $event)"
        />
        <AssistantChat :snapshot="snapshot" />
      </template>
    </div>
  </section>
</template>
