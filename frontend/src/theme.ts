import { ref } from 'vue'

/**
 * Which theme styles.css is currently resolving to. Everything visual is
 * normally handled by `prefers-color-scheme` inside the stylesheet; this exists
 * for the one decision CSS cannot make, which is judging a user-chosen caption
 * colour against the panel it will be drawn on.
 */
export const prefersDark = ref(
  typeof window !== 'undefined' && window.matchMedia('(prefers-color-scheme: dark)').matches,
)

if (typeof window !== 'undefined') {
  window
    .matchMedia('(prefers-color-scheme: dark)')
    .addEventListener('change', (event) => (prefersDark.value = event.matches))
}
