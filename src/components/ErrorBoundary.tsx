import { Component, type ErrorInfo, type ReactNode } from 'react'

/**
 * ErrorBoundary 兜住整个应用。
 *
 * React 的问题：任何组件抛异常，整棵树会被卸载，用户看到纯白窗口，
 * 连「哪里错了」都不知道。曾经点进群聊就是白屏——一个 channel.members
 * 为 null 就让整个应用消失。
 *
 * 这里做三件事：
 *   1. 不让白屏发生，显示可读的错误
 *   2. 给「重试」按钮，多数瞬时错误重试即可恢复
 *   3. 把错误留在界面上，方便用户截图反馈
 */
type Props = { children: ReactNode }
type State = { error: Error | null; info: string }

export default class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null, info: '' }

  static getDerivedStateFromError(error: Error): Partial<State> {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // 控制台保留完整堆栈，便于开发期定位
    console.error('[ErrorBoundary]', error, info.componentStack)
    this.setState({ info: info.componentStack ?? '' })
  }

  private reset = () => this.setState({ error: null, info: '' })

  render() {
    const { error, info } = this.state
    if (!error) return this.props.children

    return (
      <div
        style={{
          height: '100vh',
          display: 'grid',
          placeItems: 'center',
          padding: 'var(--space-6)',
          background: 'var(--bg-base)',
          color: 'var(--text-primary)',
        }}
      >
        <div style={{ maxWidth: 560, width: '100%' }}>
          <div style={{ fontSize: 28, marginBottom: 'var(--space-3)' }}>⚠️</div>
          <div style={{ fontSize: 'var(--text-lg)', fontWeight: 600, marginBottom: 8 }}>
            界面出错了
          </div>
          <div
            style={{
              fontSize: 'var(--text-sm)',
              color: 'var(--text-secondary)',
              marginBottom: 'var(--space-4)',
              lineHeight: 1.7,
            }}
          >
            你的数据没有丢失 —— 会话、频道、知识库都在本地文件里。
            这是渲染层的错误，重试通常能恢复。
          </div>

          <div
            className="mono"
            style={{
              background: 'var(--bg-inset)',
              border: '1px solid var(--border-subtle)',
              borderRadius: 'var(--radius-md)',
              padding: 'var(--space-3)',
              fontSize: 'var(--text-xs)',
              color: 'var(--danger)',
              marginBottom: 'var(--space-4)',
              maxHeight: 180,
              overflow: 'auto',
              whiteSpace: 'pre-wrap',
              wordBreak: 'break-word',
            }}
          >
            {error.message || String(error)}
            {info ? `\n${info.trim().split('\n').slice(0, 6).join('\n')}` : ''}
          </div>

          <div style={{ display: 'flex', gap: 8 }}>
            <button className="c-btn c-btn--primary c-btn--sm" onClick={this.reset}>
              重试
            </button>
            <button
              className="c-btn c-btn--secondary c-btn--sm"
              onClick={() => window.location.reload()}
            >
              重新加载
            </button>
          </div>
        </div>
      </div>
    )
  }
}
