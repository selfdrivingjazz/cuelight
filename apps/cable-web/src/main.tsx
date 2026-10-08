import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { RouterProvider } from "@tanstack/react-router";
import { GuideAuth, readGuideSession } from "./hosted";
import { RuntimeProvider, router } from "./routes";
import "./styles.css";

let auth: GuideAuth | undefined;
let authError: string | undefined;
try {
  auth = new GuideAuth(readGuideSession(window.location, window.history).token);
} catch (error) {
  authError = error instanceof Error ? error.message : "This guide link is invalid.";
}

createRoot(document.getElementById("root")!).render(<StrictMode><RuntimeProvider auth={auth} authError={authError}><RouterProvider router={router} /></RuntimeProvider></StrictMode>);
