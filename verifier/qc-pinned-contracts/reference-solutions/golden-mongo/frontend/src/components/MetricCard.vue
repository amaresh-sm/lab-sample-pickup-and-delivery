<script setup lang="ts">
withDefaults(defineProps<{
  label: string;
  value: string | number;
  detail: string;
  tone?: "teal" | "blue" | "amber" | "coral";
}>(), {
  tone: "teal",
});
</script>

<template>
  <article :class="['metric', `metric-${tone}`]">
    <span>{{ label }}</span>
    <strong>{{ value }}</strong>
    <small>{{ detail }}</small>
  </article>
</template>
