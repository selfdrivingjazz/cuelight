import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { WebsiteNodeApp } from "./App";
import "./styles.css";

const root = document.getElementById("root");
if (!root) throw new Error("website-node root is missing");
createRoot(root).render(<StrictMode><WebsiteNodeApp /></StrictMode>);
