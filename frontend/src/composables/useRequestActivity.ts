import { computed, readonly, ref } from 'vue'

const activeRequestCount = ref(0)
const isRequestActive = computed(() => activeRequestCount.value > 0)

export function beginRequestActivity(): void {
  activeRequestCount.value += 1
}

export function endRequestActivity(): void {
  activeRequestCount.value = Math.max(0, activeRequestCount.value - 1)
}

export function useRequestActivity() {
  return {
    /** Number of API calls currently awaiting a response. */
    activeRequestCount: readonly(activeRequestCount),
    /** Whether at least one API call is currently awaiting a response. */
    isRequestActive,
  }
}
