<script setup lang="ts">
withDefaults(defineProps<{
  kind?: "success" | "error" | "neutral";
  testId?: string;
}>(), {
  kind: "neutral",
  testId: undefined,
});
</script>

<template>
  <output
    :class="['inline-result', `result-${kind}`]"
    aria-live="polite"
    :data-testid="testId"
  >
    <slot />
  </output>
</template>
