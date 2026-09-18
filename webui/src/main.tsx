import React from "react";
import ReactDOM from "react-dom/client";
import App from "./App";
import "./index.css";
import "@copilotkit/react-ui/styles.css";
import "@copilotkit/react-core/v2/styles.css";

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);