<script setup lang="ts">
import { computed } from 'vue'
import { consoleStyleFrom, messageStyleFrom } from '../captionStyle'

const props = defineProps<{ config: Record<string, unknown>; text?: string }>()

// The preview is one message in the stream, drawn by the same functions the
// console itself uses, so what the settings page shows is what the window does.
const backdrop = computed(() => consoleStyleFrom(props.config))
const style = computed(() => messageStyleFrom(props.config))
</script>

<template>
  <div class="caption-preview">
    <div class="caption-preview-panel" :style="backdrop">
      <p class="caption-preview-text" :style="style">
        {{ text || '这是一行字幕预览效果' }}
      </p>
    </div>
  </div>
</template>
