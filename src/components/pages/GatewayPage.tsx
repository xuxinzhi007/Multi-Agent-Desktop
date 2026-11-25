import {
  DisabledState,
  EmptyState,
  ErrorState,
  PageBody,
  PageHeader,
  SkeletonRows,
  type PagePhase,
} from './pageKit'

/**
 * 网关页（§6.1 助手 → 网关）。
 *
 * M1：mad-gateway 后端尚未接入，固定禁用态，不伪造在线状态 / 延迟 / 渠道列表。
 * 渠道连接、会话绑定、白名单、最近入站等待网关 API 落地后再接。
 */
export default function GatewayPage({ phase = 'disabled' }: { phase?: PagePhase }) {

  return (
    <div className="c-col" style={{ flex: 1, minWidth: 0 }}>
      <PageHeader title="网关" badge={<span className="c-badge c-badge--disabled">未接入</span>} />
      <PageBody>
        {phase === 'loading' && <SkeletonRows rows={4} />}
        {phase === 'error' && <ErrorState message="网关状态读取失败，请检查后端连接后重试。" />}
        {phase === 'empty' && (
          <EmptyState
            icon="🌐"
            title="未配置渠道"
            hint="接入飞书 / Telegram 等渠道后，渠道连接、会话绑定与最近入站记录会显示在这里。"
          >
            <button type="button" className="c-btn c-btn--secondary c-btn--sm" disabled>
              配置渠道
            </button>
          </EmptyState>
        )}
        {phase === 'disabled' && (
          <DisabledState
            icon="🌐"
            title="网关尚未接入"
            description="渠道连接、会话绑定、白名单与最近入站记录，将在网关后端（mad-gateway）接入后在这里管理。当前桌面端只连接本机会话，这里不会显示任何在线渠道。"
            note="网关 API 落地后，本页会替换为真实的渠道列表与连接状态。"
          >
            <button type="button" className="c-btn c-btn--secondary c-btn--sm" disabled>
              配置渠道
            </button>
            <button type="button" className="c-btn c-btn--secondary c-btn--sm" disabled>
              绑定会话
            </button>
            <button type="button" className="c-btn c-btn--ghost c-btn--sm" disabled>
              查看入站记录
            </button>
          </DisabledState>
        )}
      </PageBody>
    </div>
  )
}
