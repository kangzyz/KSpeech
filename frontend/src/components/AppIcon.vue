<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{ name: string; size?: number }>()

// Every glyph is drawn on the same 24px grid so mixed icon rows stay aligned.
const strokePaths: Record<string, string[]> = {
  pause: ['M9 5v14', 'M15 5v14'],
  history: ['M12 8v4.5l3 1.8', 'M3.8 9.4a8.3 8.3 0 1 1-.4 5.6', 'M3.5 4v5.5H9'],
  lock: ['M6.5 10.5h11v9.5h-11z', 'M9 10.5V7.8a3 3 0 0 1 6 0v2.7'],
  unlock: ['M6.5 10.5h11v9.5h-11z', 'M15 10.5V7.8a3 3 0 0 0-5.7-1.3'],
  settings: [
    'M12 9a3 3 0 1 0 0 6 3 3 0 0 0 0-6z',
    'M19.4 13.5v-3l-2-.6-.5-1.2 1-1.9-2.1-2.1-1.9 1-1.2-.5-.7-2h-3l-.6 2-1.2.5-1.9-1-2.1 2.1 1 1.9-.5 1.2-2 .6v3l2 .6.5 1.2-1 1.9 2.1 2.1 1.9-1 1.2.5.6 2h3l.7-2 1.2-.5 1.9 1 2.1-2.1-1-1.9.5-1.2z',
  ],
  copy: ['M9.5 8.5h10v11h-10z', 'M14.5 8.5v-4h-10v11h4'],
  refresh: ['M19.5 8.5A8 8 0 1 0 20 15', 'M20 4v5h-5'],
  download: ['M12 3.5v11.5', 'm7.5 10.5 4.5 4.5 4.5-4.5', 'M4.5 20h15'],
  trash: ['M4.5 7h15', 'M9.5 7V4.2h5V7', 'M7 7l.9 13h8.2L17 7', 'M12 10.5v6'],
  check: ['m5 12.5 4.5 4.5L19 6.5'],
  alert: ['M12 4.2 3 20h18z', 'M12 9.5v4.2', 'M12 17h.01'],
  close: ['M6 6l12 12', 'M18 6 6 18'],
  search: ['M11 4.5a6.5 6.5 0 1 0 0 13 6.5 6.5 0 0 0 0-13z', 'm15.8 15.8 4 4'],
  mic: ['M12 3.5a2.6 2.6 0 0 1 2.6 2.6v5.4a2.6 2.6 0 0 1-5.2 0V6.1A2.6 2.6 0 0 1 12 3.5z', 'M6 11a6 6 0 0 0 12 0', 'M12 17v3.5', 'M9 20.5h6'],
  speaker: ['M4.5 9.5h3.5L13 5.5v13L8 14.5H4.5z', 'M16.2 9.4a4 4 0 0 1 0 5.2', 'M18.6 7a7.4 7.4 0 0 1 0 10'],
  window: ['M3.5 5h17v14h-17z', 'M3.5 9h17'],
  bell: ['M12 3.5a5.5 5.5 0 0 0-5.5 5.5c0 5-2 6.5-2 6.5h15s-2-1.5-2-6.5A5.5 5.5 0 0 0 12 3.5z', 'M10.4 19a1.9 1.9 0 0 0 3.3 0'],
  palette: [
    'M12 3.5a8.5 8.5 0 0 0 0 17c1.4 0 2-.9 2-1.8 0-1.3-1.1-1.7-1.1-2.8 0-.8.6-1.4 1.5-1.4h1.8a4.3 4.3 0 0 0 4.3-4.3c0-3.7-3.8-6.7-8.5-6.7z',
    'M7.6 12.4h.01',
    'M9.4 8.6h.01',
    'M14 8.1h.01',
  ],
  sliders: ['M5 6.5h14', 'M5 12h14', 'M5 17.5h14', 'M9.5 4.6v3.8', 'M15 10.1v3.8', 'M8 15.6v3.8'],
  package: ['M12 3.6 20 8v8l-8 4.4L4 16V8z', 'M4 8l8 4.4L20 8', 'M12 12.4v8'],
  info: ['M12 3.8a8.2 8.2 0 1 0 0 16.4 8.2 8.2 0 0 0 0-16.4z', 'M12 11v5.2', 'M12 7.8h.01'],
  folder: ['M3.5 6.5h5.6l1.9 2.4h9.5v10.6h-17z'],
  file: ['M6 3.5h7.5L18.5 8v12.5h-12.5z', 'M13.5 3.5V8h5'],
  external: ['M14 4.5h5.5V10', 'M19.5 4.5 11 13', 'M18 14v5.5h-14v-14H10'],
  reset: ['M4.5 12a7.5 7.5 0 1 1 2.6 5.7', 'M4.5 7v5h5'],
  chevron: ['m9.5 6.5 6 5.5-6 5.5'],
  waveform: ['M4 10.2v3.6', 'M8 6.6v10.8', 'M12 3.8v16.4', 'M16 7.6v8.8', 'M20 10.8v2.4'],
  sparkles: [
    'M10.2 3.6 11.8 8l4.4 1.6-4.4 1.6-1.6 4.4-1.6-4.4L4.2 9.6 8.6 8z',
    'M17.6 14.2l.9 2.3 2.3.9-2.3.9-.9 2.3-.9-2.3-2.3-.9 2.3-.9z',
  ],
  send: ['M20.4 3.6 3.8 10.1l6.6 2.9 2.9 6.6z', 'm20.4 3.6-10 9.4'],
  list: ['M9.5 6.5h10', 'M9.5 12h10', 'M9.5 17.5h10', 'M4.6 6.5h.01', 'M4.6 12h.01', 'M4.6 17.5h.01'],
  chat: ['M4.5 5.5h15v10.5h-8.6L6.6 19.4v-3.4H4.5z', 'M8.5 10.7h7'],
  // A lone dash reads as "minimise" everywhere on Windows, which is what
  // hiding the console to the tray amounts to.
  minimise: ['M5.5 12.5h13'],
}

// Solid glyphs read better at small sizes for transport controls.
const solidPaths: Record<string, string[]> = {
  play: ['M8.5 5.4v13.2L19 12z'],
  stop: ['M7 7h10v10H7z'],
}

const isSolid = computed(() => props.name in solidPaths)
const paths = computed(() => solidPaths[props.name] || strokePaths[props.name] || strokePaths.info)
</script>

<template>
  <svg
    :width="size || 18"
    :height="size || 18"
    viewBox="0 0 24 24"
    :fill="isSolid ? 'currentColor' : 'none'"
    :stroke="isSolid ? 'none' : 'currentColor'"
    stroke-width="1.7"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
    focusable="false"
  >
    <path v-for="path in paths" :key="path" :d="path" />
  </svg>
</template>
