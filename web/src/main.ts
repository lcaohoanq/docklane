import { mount } from "svelte";
import App from "./App.svelte";
import "@fontsource-variable/inter/wght.css";
import "@fontsource-variable/space-grotesk/wght.css";
import "./styles.css";

mount(App, {
  target: document.getElementById("app")!,
});
