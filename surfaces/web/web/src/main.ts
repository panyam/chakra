import { BasePage } from "@panyam/tsappkit";
import { AgentWebPage } from "./page.js";

// Bundle entry (esbuild -> static/app.js): boots the island page once the DOM
// is ready. What mounts where is in page.ts and the spec shell.go writes.
BasePage.loadAfterPageLoaded("agentWebPage", AgentWebPage, "AgentWebPage");
