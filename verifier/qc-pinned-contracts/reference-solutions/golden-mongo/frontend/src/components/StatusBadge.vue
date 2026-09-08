<script setup lang="ts">
import { computed } from "vue";
import { titleCase } from "../lib/format";

const props = defineProps<{ value: unknown }>();
const normalized = computed(() =>
  String(props.value ?? "unknown").toLowerCase(),
);
</script>

<template>
  <span :class="['status-badge', `status-${normalized}`]">
    {{ titleCase(normalized) }}
  </span>
</template>
