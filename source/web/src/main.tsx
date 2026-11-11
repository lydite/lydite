import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";

const root = document.getElementById("root");
if (root === null) {
  throw new Error("index.html carries no #root element to mount the dashboard into");
}

createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
