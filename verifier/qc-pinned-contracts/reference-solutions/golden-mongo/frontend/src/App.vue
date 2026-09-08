<script setup lang="ts">
import { onMounted } from "vue";
import { RouterView } from "vue-router";
import AppShell from "./components/AppShell.vue";
import LoginPage from "./components/LoginPage.vue";
import { useWorkspaceStore } from "./stores/workspace";

const store = useWorkspaceStore();

onMounted(() => {
  if (store.isAuthenticated) void store.initialize();
});
</script>

<template>
  <LoginPage v-if="!store.isAuthenticated" />
  <AppShell v-else>
    <RouterView />
  </AppShell>
</template>
