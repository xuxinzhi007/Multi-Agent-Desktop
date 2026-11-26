import { StrictMode, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import './styles/components.css'
// 必须在任何用到 mygo-runtime 的模块之前执行：
// 浏览器里没有 MyGo 注入的 globalThis.mygo，不先装上替身，
// 每个 API.* 调用和事件订阅都会抛 "runtime not found"，
// 整个应用直接进 ErrorBoundary。
import { installPreviewRuntime, waitForHostRuntime } from './lib/preview-runtime'
import { installHttpRuntime } from './lib/mygo-http'
import App from './App.tsx'
import ErrorBoundary from './components/ErrorBoundary.tsx'

function mount(node: ReactNode) {
  createRoot(document.getElementById('root')!).render(
    <StrictMode>
      <ErrorBoundary>{node}</ErrorBoundary>
    </StrictMode>,
  )
}

// 只有一套页面。宿主按三级判定，顺序不能调换：
//   1) 原生窗口：等宿主注入 globalThis.mygo（注入比 React 启动慢，必须等）；
//   2) 浏览器 + 桌面进程在跑：连它内嵌的网关，装上同签名的 HTTP runtime。
//
//      于是浏览器里跑的就是**桌面这一套界面**，数据、交互、审批全部一致，
//      不再维护第二套网页（旧的 /remote 只读工作台已删除）；
//   3) 两者都没有：才装预览替身（假数据，写操作会显式说"未真正执行"）。
async function boot() {
  if (await waitForHostRuntime()) return
  if (await installHttpRuntime()) return
  await installPreviewRuntime()
}

void boot().then(() => mount(<App />))
