<script setup lang="ts">
import { nextTick, ref } from "vue";
import { useRouter } from "vue-router";
import { errorMessage } from "../lib/api";
import { useWorkspaceStore } from "../stores/workspace";
import FieldGroup from "./FieldGroup.vue";

const store = useWorkspaceStore();
const router = useRouter();
const username = ref("");
const password = ref("");
const busy = ref(false);
const error = ref("");
const errorElement = ref<HTMLElement | null>(null);

async function submit(): Promise<void> {
  busy.value = true;
  error.value = "";
  try {
    await store.login(username.value, password.value);
    await router.replace("/dashboard");
  } catch (cause) {
    error.value = errorMessage(cause);
    await nextTick();
    errorElement.value?.focus();
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <main class="login-page">
    <section class="login-story" aria-label="Product overview">
      <div class="login-brand">
        <span aria-hidden="true">LF</span>
        <strong>Labflow</strong>
      </div>
      <div class="story-copy">
        <span class="eyebrow">Clinical logistics control</span>
        <h1>One trusted journey from collection to laboratory.</h1>
        <p>
          Coordinate every route, custody exchange, handling checkpoint, and
          deadline from one secure workspace.
        </p>
        <ul>
          <li><i aria-hidden="true">01</i>Live operational visibility</li>
          <li><i aria-hidden="true">02</i>Verified chain of custody</li>
          <li><i aria-hidden="true">03</i>Deadline-safe replanning</li>
        </ul>
      </div>
      <p class="story-foot">
        Designed for sensitive clinical operations · Access is audited
      </p>
    </section>
    <section class="login-card" aria-labelledby="login-title">
      <div>
        <span class="eyebrow">Secure workspace</span>
        <h2 id="login-title">Welcome back</h2>
        <p>Use the account supplied by your operations administrator.</p>
      </div>
      <form @submit.prevent="submit">
        <FieldGroup label="Username" input-id="login-user">
          <input
            id="login-user"
            v-model="username"
            name="username"
            autocomplete="username"
            autofocus
            required
          />
        </FieldGroup>
        <FieldGroup label="Password" input-id="login-pass">
          <input
            id="login-pass"
            v-model="password"
            name="password"
            type="password"
            autocomplete="current-password"
            required
          />
        </FieldGroup>
        <button
          id="login-submit"
          class="primary-button login-submit"
          :disabled="busy"
        >
          <span v-if="busy" class="spinner" role="status">
            <i aria-hidden="true" /> Signing in
          </span>
          <template v-else>Sign in</template>
        </button>
      </form>
      <p
        ref="errorElement"
        class="login-error"
        role="alert"
        tabindex="-1"
      >
        {{ error }}
      </p>
      <p class="security-note">
        Sessions are kept only in this browser tab and expire on the server.
      </p>
    </section>
  </main>
</template>
