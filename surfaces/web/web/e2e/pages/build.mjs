import { build } from "esbuild";
import { solidPlugin } from "esbuild-plugin-solid";

// Builds the React and Solid host pages for chat-element.e2e.ts into dist/.
// Each bundles its own framework, and neither imports anything from src/, so
// the element meets them the way a third-party app would load it.
const common = { bundle: true, format: "esm", target: "es2022", outdir: new URL("dist", import.meta.url).pathname, logLevel: "warning" };
await build({ ...common, entryPoints: [new URL("react-app.jsx", import.meta.url).pathname], jsx: "automatic", jsxImportSource: "react", tsconfigRaw: {} });
await build({ ...common, entryPoints: [new URL("solid-app.jsx", import.meta.url).pathname], plugins: [solidPlugin()] });
