<script setup lang="ts">
defineProps<{ title: string; description: string }>();
</script>

<template>
  <div class="empty-state">
    <span aria-hidden="true">✓</span>
    <strong>{{ title }}</strong>
    <p>{{ description }}</p>
  </div>
</template>
