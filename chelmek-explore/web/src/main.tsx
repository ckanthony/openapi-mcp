import React from "react";
import { createRoot } from "react-dom/client";
import { Theme } from "@carbon/react";
import "./styles.scss";
import { App } from "./App";

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <Theme theme="g100">
      <App />
    </Theme>
  </React.StrictMode>,
);
