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
 * 记忆页（§6.1 助手 → 记忆）。
 *
 * 注意区分：记忆（跨会话的每日笔记 / 事实 / 决策沉淀）与现有知识库
 * （人工整理、可挂载到会话的资料）不是一回事。这里只给一个跳转到
 * 知识库检查器的入口，不把知识库伪装成本页的记忆列表。
 *
 * M1：记忆后端（internal/memory）尚未接入，固定禁用态。
 */
export default function MemoryPage({
  onOpenKnowledge,
  phase = 'disabled',
}: {
  onOpenKnowledge: () => void
  phase?: PagePhase
}) {

  return (
    <div className="c-col" style={{ flex: 1, minWidth: 0 }}>
      <PageHeader title="记忆" badge={<span className="c-badge c-badge--disabled">未接入</span>} />
      <PageBody>
        {phase === 'loading' && <SkeletonRows rows={4} />}
        {phase === 'error' && <ErrorState message="记忆读取失败，请稍后重试。" />}
        {phase === 'empty' && (
          <EmptyState
            icon="🧠"
            title="还没有记忆"
            hint="跨会话沉淀的事实、决策与每日笔记会出现在这里；它们和知识库资料分开管理。"
          >
            <button type="button" className="c-btn c-btn--secondary c-btn--sm" disabled>
              导入记忆
            </button>
          </EmptyState>
        )}
        {phase === 'disabled' && (
          <DisabledState
            icon="🧠"
            title="记忆后端尚未接入"
            description="这里将沉淀跨会话的每日笔记、事实与决策，带来源、置信度与删除 / 固化操作。目前还没有记忆存储与检索后端，本页不展示任何记忆条目。"
            note="现有的人工资料请使用知识库（检查器 → 知识），二者不是同一套数据。"
          >
            {/* 知识库是现有能力，入口可用；记忆相关操作待后端接入后开放 */}
            <button type="button" className="c-btn c-btn--secondary c-btn--sm" onClick={onOpenKnowledge}>
              打开知识库
            </button>
            <button type="button" className="c-btn c-btn--ghost c-btn--sm" disabled>
              导入记忆
            </button>
            <button type="button" className="c-btn c-btn--ghost c-btn--sm" disabled>
              新建记忆
            </button>
          </DisabledState>
        )}
      </PageBody>
    </div>
  )
}
