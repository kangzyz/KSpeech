<script setup lang="ts">
import { computed } from 'vue'
import { argbToCss, argbToHex } from '../captionStyle'

const props = defineProps<{ modelValue: number; label: string }>()
const emit = defineEmits<{ 'update:modelValue': [value: number] }>()

const rgb = computed({
  get: () => argbToHex(props.modelValue),
  set: (value: string) => {
    const alpha = (props.modelValue >>> 24) & 0xff
    emit('update:modelValue', ((alpha << 24) | Number.parseInt(value.slice(1), 16)) >>> 0)
  },
})

const alpha = computed({
  get: () => Math.round((((props.modelValue >>> 24) & 0xff) / 255) * 100),
  set: (value: number) => {
    const nextAlpha = Math.round((value / 100) * 255)
    emit('update:modelValue', ((nextAlpha << 24) | (props.modelValue & 0xffffff)) >>> 0)
  },
})

const preview = computed(() => argbToCss(props.modelValue))
</script>

<template>
  <label class="color-field">
    <span class="color-name">
      {{ label }}
      <code>{{ rgb.toUpperCase() }}</code>
    </span>
    <span class="color-controls">
      <span class="color-swatch">
        <span class="swatch-fill" :style="{ background: preview }" />
        <input v-model="rgb" type="color" :aria-label="`${label}色值`" />
      </span>
      <!--
        The fill percentage rides along as a custom property: a range input can
        only paint a two-tone track from a gradient stop it is told about.
      -->
      <input
        v-model.number="alpha"
        type="range"
        min="0"
        max="100"
        :style="{ '--fill': `${alpha}%` }"
        :aria-label="`${label}不透明度`"
      />
      <output>{{ alpha }}%</output>
    </span>
  </label>
</template>
