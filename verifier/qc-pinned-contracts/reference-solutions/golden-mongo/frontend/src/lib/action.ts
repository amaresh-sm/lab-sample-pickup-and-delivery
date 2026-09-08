import { ref } from "vue";
import { errorMessage } from "./api";

/** Provides consistent busy/error state for retry-safe workflow actions. */
export function useAction() {
  const busy = ref(false);
  const error = ref("");

  async function run<T>(operation: () => Promise<T>): Promise<T | null> {
    busy.value = true;
    error.value = "";
    try {
      return await operation();
    } catch (cause) {
      error.value = errorMessage(cause);
      return null;
    } finally {
      busy.value = false;
    }
  }

  return { busy, error, run };
}
