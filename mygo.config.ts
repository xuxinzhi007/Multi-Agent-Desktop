import { defineConfig } from "mygo-cli";

export default defineConfig({
  name: "multi-agent-desktop",
  identifier: "com.multiagent.desktop",
  version: "0.3.0",
  devUrl: "http://localhost:5173",
  devCommand: "npm run dev:web",
  buildCommand: "npm run build:web",
  frontendDist: "dist",
  bindings: "src/mygo.ts",
  out: "build",
});
