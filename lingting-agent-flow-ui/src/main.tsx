import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import "@/index.css";

import { AppShell } from "@/app/AppShell";

const root = document.getElementById("root");

if (!root) {
  throw new Error("未找到应用挂载节点");
}

createRoot(root).render(
  <StrictMode>
    <AppShell />
  </StrictMode>,
);
