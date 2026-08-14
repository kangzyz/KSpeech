<script setup lang="ts">
import AppIcon from './AppIcon.vue'
import ToggleSwitch from './ToggleSwitch.vue'
import type { ConfigField } from '../types'

defineProps<{ fields: ConfigField[]; disabled?: boolean }>()
const emit = defineEmits<{ change: [field: ConfigField, value: unknown] }>()

const asText = (value: unknown) => (value === undefined || value === null ? '' : String(value))
const isPath = (type: string) => type === 'file' || type === 'folder'
</script>

<template>
  <template v-for="field in fields" :key="field.key">
    <p v-if="field.type === 'message'" class="field-note">{{ field.hint }}</p>

    <div v-else-if="field.type === 'checkbox'" class="switch-row">
      <div>
        <strong>{{ field.label }}</strong>
        <small v-if="field.hint">{{ field.hint }}</small>
      </div>
      <ToggleSwitch
        :model-value="Boolean(field.value)"
        :disabled="disabled"
        :label="field.label"
        @update:model-value="emit('change', field, $event)"
      />
    </div>

    <label v-else class="field">
      <span class="field-label">{{ field.label }}</span>

      <select
        v-if="field.type === 'select'"
        class="select"
        :disabled="disabled"
        :value="asText(field.value)"
        @change="emit('change', field, ($event.target as HTMLSelectElement).value)"
      >
        <option v-for="option in field.options" :key="String(option.value)" :value="String(option.value)">
          {{ option.label }}
        </option>
      </select>

      <span v-else-if="isPath(field.type)" class="input-row">
        <input
          class="input mono"
          type="text"
          spellcheck="false"
          :disabled="disabled"
          :value="asText(field.value)"
          :placeholder="field.type === 'folder' ? '文件夹绝对路径' : '文件绝对路径'"
          @change="emit('change', field, ($event.target as HTMLInputElement).value)"
        />
        <AppIcon :name="field.type === 'folder' ? 'folder' : 'file'" :size="17" />
      </span>

      <input
        v-else
        class="input"
        :class="{ mono: field.type === 'password' }"
        :type="field.type === 'number' ? 'number' : field.type === 'password' ? 'password' : 'text'"
        :disabled="disabled"
        :value="asText(field.value)"
        @change="
          emit(
            'change',
            field,
            field.type === 'number'
              ? Number(($event.target as HTMLInputElement).value)
              : ($event.target as HTMLInputElement).value,
          )
        "
      />

      <span v-if="field.hint" class="field-hint">{{ field.hint }}</span>
    </label>
  </template>
</template>
