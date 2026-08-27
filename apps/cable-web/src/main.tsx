import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { RouterProvider } from "@tanstack/react-router";
import { RuntimeProvider, router } from "./routes";
import "./styles.css";

createRoot(document.getElementById("root")!).render(<StrictMode><RuntimeProvider><RouterProvider router={router} /></RuntimeProvider></StrictMode>);
