import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import './styles/components.css'
// 必须在任何用到 mygo-runtime 的模块之前执行：
// 浏览器里没有 MyGo 注入的 globalThis.mygo，不先装上替身，
// 每个 API.* 调用和事件订阅都会抛 "runtime not found"，
// 整个应用直接进 ErrorBoundary。
import { installPreviewRuntime } from './lib/preview-runtime'
import App from './App.tsx'
import ErrorBoundary from './components/ErrorBoundary.tsx'

installPreviewRuntime()

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ErrorBoundary>
      <App />
    </ErrorBoundary>
  </StrictMode>,
)
