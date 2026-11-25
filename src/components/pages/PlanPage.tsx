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
 * 计划页（§6.1 助手 → 计划 / Schedules）。
 *
 * M1：调度器（internal/scheduler）尚未启动，固定禁用态。
 * 定时提醒、cron 任务、上次 / 下次运行与失败降频等待后端接入。
 */
export default function PlanPage({ phase = 'disabled' }: { phase?: PagePhase }) {

  return (
    <div className="c-col" style={{ flex: 1, minWidth: 0 }}>
      <PageHeader title="计划" badge={<span className="c-badge c-badge--disabled">未接入</span>} />
      <PageBody>
        {phase === 'loading' && <SkeletonRows rows={3} />}
        {phase === 'error' && <ErrorState message="调度器未启动，计划列表暂时读不到。" />}
        {phase === 'empty' && (
          <EmptyState
            icon="🗓"
            title="还没有计划"
            hint="定时提醒、cron 任务与早报会列在这里，显示上次 / 下次运行时间与失败次数。"
          >
            <button type="button" className="c-btn c-btn--secondary c-btn--sm" disabled>
              新建计划
            </button>
          </EmptyState>
        )}
        {phase === 'disabled' && (
          <DisabledState
            icon="🗓"
            title="调度器尚未启动"
            description="这里将管理定时提醒、cron / 间隔任务、静默时段与失败降频。当前没有调度器后端，不会创建或执行任何定时任务。"
            note="调度器落地后，本页会提供新建 / 停用计划与失败原因查看。"
          >
            <button type="button" className="c-btn c-btn--secondary c-btn--sm" disabled>
              新建计划
            </button>
            <button type="button" className="c-btn c-btn--ghost c-btn--sm" disabled>
              查看静默时段
            </button>
          </DisabledState>
        )}
      </PageBody>
    </div>
  )
}
