<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import { api } from '../api'
import { formatClock, formatDate } from '../format'
import AppIcon from './AppIcon.vue'
import type { AppSnapshot } from '../types'

const props = defineProps<{ snapshot: AppSnapshot }>()

const actionError = ref('')
const copiedId = ref<number | null>(null)
let copiedTimer: number | undefined

const assistant = computed(() => props.snapshot.assistant)
const insights = computed(() => assistant.value.insights)
const hasContent = computed(
  () => insights.value.length > 0 || assistant.value.conversations.length > 0,
)

const run = async (operation: () => Promise<void>) => {
  actionError.value = ''
  try {
    await operation()
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : String(error)
  }
}

const copy = async (id: number, text: string) => {
  await run(() => api.copyText(text))
  if (actionError.value) return
  copiedId.value = id
  window.clearTimeout(copiedTimer)
  copiedTimer = window.setTimeout(() => (copiedId.value = null), 1400)
}

onBeforeUnmount(() => window.clearTimeout(copiedTimer))
</script>

<template>
  <section class="pane insight-pane">
    <header class="pane-head">
      <h2>要点总结</h2>
      <span class="result-count">
        <template v-if="assistant.summarizing">汇总中…</template>
        <template v-else-if="assistant.pendingLines > 0">{{ assistant.pendingLines }} 句待汇总</template>
        <template v-else>{{ insights.length }} 条</template>
      </span>
      <button
        type="button"
        class="icon-btn"
        :disabled="!hasContent"
        title="清空要点与问答"
        aria-label="清空要点与问答"
        @click="run(api.clearAssistant)"
      >
        <AppIcon name="trash" :size="15" />
      </button>
    </header>

    <p v-if="actionError" class="pane-error" role="alert">{{ actionError }}</p>

    <div class="insight-list scroll-area">
      <article v-for="insight in insights" :key="insight.id" class="insight-row">
        <header>
          <time :title="formatDate(insight.time)">{{ formatClock(insight.time) }}</time>
          <button
            class="icon-btn"
            :title="copiedId === insight.id ? '已复制' : '复制这一条'"
            :aria-label="copiedId === insight.id ? '已复制' : '复制这一条'"
            @click="copy(insight.id, insight.text)"
          >
            <AppIcon :name="copiedId === insight.id ? 'check' : 'copy'" :size="14" />
          </button>
        </header>
        <p>{{ insight.text }}</p>
      </article>

      <div v-if="insights.length === 0" class="empty-state">
        <AppIcon name="list" :size="24" />
        <strong>还没有要点</strong>
        <span v-if="!assistant.enabled">开启 AI 助手后，这里会定期整理结论、待办和时间节点。</span>
        <span v-else-if="!assistant.summarize">要点汇总已在设置里关掉。</span>
        <span v-else>开始识别后，每隔一段时间会把这段话里的结论、待办和时间节点整理到这里。</span>
      </div>
    </div>
  </section>
</template>
