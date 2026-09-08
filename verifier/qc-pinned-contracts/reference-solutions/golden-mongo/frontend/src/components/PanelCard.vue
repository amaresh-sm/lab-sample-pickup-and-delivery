<script setup lang="ts">
import { computed } from "vue";

const props = withDefaults(defineProps<{
  eyebrow?: string;
  title: string;
  description?: string;
  className?: string;
}>(), {
  eyebrow: "",
  description: "",
  className: "",
});

const headingId = computed(
  () => `panel-${props.title.toLowerCase().replace(/[^a-z0-9]+/g, "-")}`,
);
</script>

<template>
  <section
    :class="['panel', className]"
    :aria-labelledby="headingId"
  >
    <div class="panel-heading">
      <div>
        <span v-if="eyebrow" class="eyebrow">{{ eyebrow }}</span>
        <h2 :id="headingId">{{ title }}</h2>
        <p v-if="description">{{ description }}</p>
      </div>
      <div v-if="$slots.actions" class="panel-actions">
        <slot name="actions" />
      </div>
    </div>
    <div class="panel-body">
      <slot />
    </div>
  </section>
</template>
