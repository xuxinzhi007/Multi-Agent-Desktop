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

// 必须先确定跑在真窗口还是纯浏览器里，再挂载 React。
// 不等这一步就渲染，桌面窗口会被误判成预览，整个界面变成假数据。
void installPreviewRuntime().then(() => {
  createRoot(document.getElementById('root')!).render(
    <StrictMode>
      <ErrorBoundary>
        <App />
      </ErrorBoundary>
    </StrictMode>,
  )
})
