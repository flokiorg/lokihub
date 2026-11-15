import React from "react";
import ReactDOM from "react-dom/client";
import App from "src/App.tsx";
import "src/i18n"; // Initialize i18n before React renders
import "src/i18n/types"; // TypeScript augmentation for typed t() keys
import "src/fonts.css";
import "src/index.css";
ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>
);
