import { createApp } from "vue";
import { createPinia } from "pinia";
import "maplibre-gl/dist/maplibre-gl.css";
import App from "./App.vue";
import { router } from "./router";
import "./styles.css";

createApp(App).use(createPinia()).use(router).mount("#app");
