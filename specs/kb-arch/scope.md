# kb-arch 范围

- 扫哪里：`internal/knowledge/` 及其调用方（`main.go` 知识库段、`internal/agent/manager.go` 注入点、`src/components/KnowledgePanel.tsx`）
- 为什么是这里：用户点名
- 不扫什么：前端其他组件、channel 的广播编排、llm 客户端
- GLOSSARY.md：无
- docs/adr：无
