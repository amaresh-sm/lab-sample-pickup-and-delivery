<script setup lang="ts">
withDefaults(defineProps<{
  label: string;
  inputId: string;
  hint?: string;
  className?: string;
}>(), {
  hint: "",
  className: "",
});
</script>

<template>
  <div :class="['field', className]">
    <label :for="inputId">{{ label }}</label>
    <slot />
    <small v-if="hint" :id="`${inputId}-hint`">{{ hint }}</small>
  </div>
</template>
