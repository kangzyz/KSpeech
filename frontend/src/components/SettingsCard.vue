<script setup lang="ts">
defineProps<{ title: string; description?: string; disabled?: boolean }>()
</script>

<template>
  <section class="card" :class="{ disabled }">
    <header class="card-header">
      <div>
        <h2>{{ title }}</h2>
        <p v-if="description">{{ description }}</p>
      </div>
      <slot name="action" />
    </header>
    <div class="card-body">
      <slot />
    </div>
  </section>
</template>
