import { Toaster, toast as sonnerToast } from 'sonner'

export type Toast = {
  id: string
  message: string
  type: 'success' | 'error' | 'info'
  duration?: number
}

/**
 * Toast API 保持原样（toast / toastSuccess / toastError / toastInfo），
 * 底层换成 sonner —— 它自带堆叠、去重、并发上限、无障碍。
 */
export function toast(message: string, type: Toast['type'] = 'info', duration = 3000) {
  const opts = { duration }
  if (type === 'success') return sonnerToast.success(message, opts)
  if (type === 'error') return sonnerToast.error(message, opts)
  return sonnerToast(message, opts)
}

export function toastSuccess(message: string, duration?: number) {
  toast(message, 'success', duration)
}

export function toastError(message: string, duration?: number) {
  toast(message, 'error', duration)
}

export function toastInfo(message: string, duration?: number) {
  toast(message, 'info', duration)
}

/** 挂载一次即可，由 App 渲染。 */
export default function ToastContainer() {
  return (
    <Toaster
      position="top-right"
      visibleToasts={2} // 与 Warp 的「最多 2 个」一致
      closeButton
      toastOptions={{
        style: {
          background: 'var(--bg-elevated)',
          border: '1px solid var(--border-default)',
          color: 'var(--text-primary)',
          fontSize: 'var(--text-sm)',
          borderRadius: 'var(--radius-md)',
          boxShadow: 'var(--shadow-lg)',
        },
      }}
    />
  )
}
