/**
 * 主区域视图。
 *
 * - fleet / chat：现有会话与舰队视图，回归基线不动
 * - gateway / memory / plan：助手分组下的新页面（M1 前端 IA 占位，后端接入后替换占位态）
 */
export type MainView = 'fleet' | 'chat' | 'gateway' | 'memory' | 'plan'
