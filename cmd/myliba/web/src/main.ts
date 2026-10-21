import { createApp } from "vue";
import App from "./App.vue";
import SharePage from "./SharePage.vue";
import "./style.css";

createApp(/^\/s\/[0-9a-f-]{36}$/.test(window.location.pathname) ? SharePage : App).mount("#app");
