/**
 * 工具名的唯一口径。
 *
 * 单聊的思考链、检查器的列表、改动清单都要说同一种话：
 * 不一致时同一个 Bash 会在三处分别显示成「执行」「Bash」「bash」。
 * 另外「哪些工具算改了文件」也只有这一份判断，避免把 Read / Glob 的
 * 搜索路径当成落盘的改动。
 */

const LABELS: Record<string, string> = {
  read_file: '读取',
  write_file: '写入',
  edit_file: '修改',
  apply_patch: '改文件',
  bash: '执行',
  Read: '读取',
  Write: '写入',
  Edit: '修改',
  MultiEdit: '修改',
  Bash: '执行',
  Glob: '找文件',
  Grep: '搜索',
  TodoWrite: '列计划',
  WebFetch: '抓网页',
  WebSearch: '联网搜索',
}

export function toolLabel(name: string): string {
  return LABELS[name] ?? LABELS[name.toLowerCase()] ?? name
}

// 归一化后比较：Write / write / write_file / writeFile 视为同一个。
const WRITE_TOOLS = new Set([
  'write',
  'writefile',
  'edit',
  'editfile',
  'multiedit',
  'notebookedit',
  'applypatch',
  'patch',
  'strreplace',
  'createfile',
  'deletefile',
])

/** 只有真正写盘的工具才算「改了文件」。 */
export function isWriteTool(name: string): boolean {
  return WRITE_TOOLS.has(name.toLowerCase().replace(/[_\-\s]/g, ''))
}
