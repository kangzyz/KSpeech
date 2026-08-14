<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import ConsoleView from './views/ConsoleView.vue'
import SettingsView from './views/SettingsView.vue'
import { api } from './api'
import type { AppSnapshot, AssistantState, LiveState } from './types'

const view = new URLSearchParams(window.location.search).get('view') || 'console'
const snapshot = ref<AppSnapshot | null>(null)
const loadingError = ref('')
let unsubscribe: (() => void) | undefined
let unsubscribeLive: (() => void) | undefined
let unsubscribeAssistant: (() => void) | undefined
let pendingLive: LiveState | undefined
let pendingAssistant: AssistantState | undefined

const viewComponent = computed(() => (view === 'settings' ? SettingsView : ConsoleView))

onMounted(async () => {
  document.documentElement.dataset.view = view
  try {
    let receivedFullState = false
    unsubscribe = api.onState((next) => {
      receivedFullState = true
      pendingLive = undefined
      snapshot.value = next
    })
    unsubscribeLive = api.onLiveState((live) => {
      if (snapshot.value) snapshot.value = { ...snapshot.value, ...live }
      else pendingLive = live
    })
    unsubscribeAssistant = api.onAssistantState((assistant) => {
      if (snapshot.value) snapshot.value = { ...snapshot.value, assistant }
      else pendingAssistant = assistant
    })
    const initial = await api.bootstrap()
    if (!receivedFullState) {
      snapshot.value = { ...initial, ...pendingLive }
      if (pendingAssistant) snapshot.value.assistant = pendingAssistant
    }
  } catch (error) {
    loadingError.value = error instanceof Error ? error.message : String(error)
  }
})

onBeforeUnmount(() => {
  unsubscribe?.()
  unsubscribeLive?.()
  unsubscribeAssistant?.()
})
</script>

<template>
  <main v-if="snapshot" :class="['app-shell', `view-${view}`]">
    <component :is="viewComponent" :snapshot="snapshot" />
  </main>
  <main v-else-if="loadingError" class="fatal-state">
    <div>
      <span class="eyebrow">KSpeech</span>
      <h1>无法连接应用服务</h1>
      <p>{{ loadingError }}</p>
      <p>请退出后重新启动 KSpeech；若问题持续存在，请检查 %APPDATA%\KSpeech 目录的读写权限。</p>
    </div>
  </main>
  <main v-else class="boot-state" aria-label="正在启动 KSpeech">
    <span class="boot-mark" />
    <span>正在启动 KSpeech</span>
  </main>
</template>
