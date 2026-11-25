# 研究：工作区边界怎么做，以及要不要改造 Grok Bot

> 调研日期：2026-11-25
> 一手来源：**本地实测 Grok Bot 0.78.4 的打包产物**（`/Applications/Grok Bot.app`，`~/.grokbot/settings.json`）；Claude Code 官方文档 `code.claude.com/docs/en/*.md`（当日 curl，HTTP 200）；本仓库代码。
> ⭐ 实测/官方原文｜🔶 我的推论

---

## 0. 先纠正一个前提

**Grok Bot 不是开源项目，拿不来改造。**

⭐ 本地 `app.asar` 里的 `package.json`：

```json
{
  "name": "sand",
  "productName": "Grok Bot",
  "version": "0.78.4",
  "author": "SpaceXAI",
  "homepage": "https://cursor.com",
  "private": true,
  "dependencies": { "@anysphere/agent": "workspace:*", "@anysphere/agent-core": "workspace:*", … }
}
```

- `"private": true`，依赖全是 `@anysphere/*` 的 **workspace 私有包**（Cursor/Anysphere 内部仓库），没有 LICENSE 文件
- 解包能读到的只是**编译压缩后的产物**（`main.cjs` 里变量名已成了 `CC`、`$ie`、`PC`），不是可维护的源码
- 🔶 结论：**可以参考它的机制，不能改造它的代码**。拿打包产物二次分发有法律风险，且拿不到那 30+ 个私有依赖，根本编译不起来。

🔶 上一轮的调研已经给过同赛道的结论：最接近的开源项目 Crystal（并行会话 + worktree 桌面应用）2026-02 已废弃。**「改造一个壳」这条路本身也不成立。**

---

## 1. 你的限制卡在哪（本仓库实测）

⭐ 三处硬编码单根：

| 位置 | 行为 |
|---|---|
| `internal/tool/builtin/builtin.go:safePath` | `filepath.Rel(root, abs)` 以 `..` 开头就拒：`path %q is outside the working directory %q` |
| `internal/workdir/TooBroad` | 主目录、桌面、文稿、下载、盘符根一律拒绝（这条是对的，macOS 权限弹窗问题） |
| `Agent.WorkDir` | **单个字符串**，一个会话只能有一个根 |

🔶 后果：真实排查经常是「改 data-service，同时读 multi-agent-desktop」。单根下模型只能报越界，用户得换会话或搬目录——这就是你说的「其他项目在外面就不方便」。

---

## 2. 三家怎么做的（一手证据）

### 2.1 ⭐ Grok Bot：工作区是**一组根**，权限是**三档 + 上限**

解包 `dist/local-exec-daemon/main.cjs` 实测：

```js
// 工作区可以是数组
o = Array.isArray(this.workspacePath) ? this.workspacePath : [this.workspacePath ?? process.cwd()]

// 越界守卫：多根 + worktree 主检出
function $ie({ targetPath, workspacePaths, mainWorktreePath }) {
  if (targetPath === undefined || workspacePaths.length === 0
      || !workspacePaths.every(isAbs) || uC(targetPath, workspacePaths)) return false;
  let candidates = isAbsolute(targetPath) ? [resolve(targetPath)]
                   : workspacePaths.map(root => …)
  …
}
```

权限档位（枚举原文）：

```js
CC = ["always", "ask", "never"]
```

`~/.grokbot/settings.json` 实测字段：

```json
{ "localToolPermission": "ask", "localEgressAllowed": true }
```

代码里还有两个值得抄的设计：

- **`localToolPermissionCeiling`**：`getLocalToolPermission()` 返回 `PC(choice ?? "ask", ceiling)` —— **用户选的档位不能超过一个上限**（策略/管理员封顶）
- **`localToolPermissionByMachineId`**：按机器分别记档位（同一账号在台式机和笔记本上可以不同）
- 另有 `permissionMode: ["default","readonly"]` 用于子 agent / skill 定义

🔶 这直接印证了上一轮我做的 `ask / auto / readonly` 三档方向是对的（always≈auto，never≈readonly）。

### 2.2 ⭐ Claude Code：`--add-dir` + `permissions.additionalDirectories`

官方 `cli-reference.md` 原文：

> `--add-dir` — Add additional working directories for Claude to read and edit files. **Grants file access; Claude Code doesn't discover most `.claude/` configuration from these directories.** Validates that each path exists as a directory. … To persist these directories across sessions, set `permissions.additionalDirectories` in settings.
> 例：`claude --add-dir ../apps ../lib`

官方 `settings.md` 原文：

> **The key waits for trust.** `permissions.allow` rules, **`permissions.additionalDirectories`** … apply only after each teammate **trusts the folder**. Until then they still see prompts.

🔶 两条必须抄的约束：
1. **附加目录只放宽文件访问，不加载那边的配置**（否则一个恶意仓库能塞 skill/hook 进来）
2. **附加目录要校验存在性**，且受信任机制约束

### 2.3 🔶 Codex

`openai/codex` 的 `docs/sandbox.md` 现在只是一句转链到 `developers.openai.com/codex/security`；该页当日抓取只拿到导航，**没拿到 `writable_roots` 原文**。因此本报告**不对 Codex 的具体字段名作断言**。它的沙箱分档（read-only / workspace-write / danger-full-access）是公开常识，但本次没有一手证据，标注为待核。

---

## 3. 已落地：多根工作区（本仓库）

按上面两家的机制实现，**不抄代码**：

| 改动 | 位置 |
|---|---|
| `Roots` 类型：主目录在首位，其余为附加目录；去重、转绝对、逐个校验 | `internal/workdir/roots.go` |
| `Roots.Resolve`：相对路径先主目录、不存在再逐个附加目录试；绝对路径校验落在任一根内 | 同上 |
| 文件工具带 `ExtraDirs`，`safePath` → `rootsPath`；**单根行为完全不变** | `internal/tool/builtin/builtin.go` |
| `Agent.ExtraDirs` + `SetAgentExtraDirs` + `EffectiveExtraDirs` | `internal/agent/manager.go` |
| 持久化白名单四处同步（Save / Load / Snapshot / viewMap） | 同上 |
| `API.SetAgentExtraDirs` | `main.go` |
| 工作目录弹层里的「附加目录」列表，可加可删 | `src/components/AgentView.tsx` |

**三条刻意保留的边界**：

1. **bash 的 cwd 仍是主目录**。附加目录只给文件读写——命令在哪跑必须是确定的，否则 `git status` 之类的相对语义会漂。
2. **附加目录同样过 `TooBroad`**：不能拿它把主目录/桌面/文稿/下载放进来。
3. **不从附加目录加载技能或知识**。放宽的是文件访问，不是配置——和 Claude Code 一致。

测试：`internal/workdir/roots_test.go`（跨根解析、主目录优先、穿越拒绝、过宽拒绝、去重、单根不变）、`internal/agent/config_test.go`（往返持久化、校验）、`e2e/extra-dirs.spec.ts`。

---

## 4. 还值得从 Grok Bot 抄的两件事（未做）

| 机制 | 价值 | 成本 |
|---|---|---|
| **权限上限 `ceiling`** | 用户选 auto，但策略封顶在 ask —— 团队/生产环境有用 | 小：`Center` 加一个 ceiling 字段，取小 |
| **按机器记档位** | 同一份配置在台式机和笔记本上档位不同 | 中：需要机器 id，且配置要分机器存 |

🔶 我的建议：**ceiling 值得做**（你现在有生产环境只读的需求，ceiling 能防止误切到 auto）；按机器记档位对单人使用价值不大，先不做。

---

## 5. 关于「改造成成熟可用项目」

🔶 直说：**不需要换底座。** 上一轮的成熟度评估结论仍然成立——

- 你已经有 19k 行业务 Go + 12k 行测试 + 67 个 e2e，核心循环/审批/审计/飞书都是真实现
- 差的是**签名分发、密钥进钥匙串、失败可解释**这三件，不是架构
- 换成 Electron 壳只会让你多背 344MB 体积和一套 JS 工具链，`docs/design.md` §2.1 当初放弃 Electron 的理由没有变

**该做的是继续补那三件，以及把已经解锁的多 agent 协作跑出真实数据。**

---

## 附：来源

| # | 来源 | 方式 |
|---|---|---|
| 1 | Grok Bot 0.78.4 `app.asar` → `package.json` / `dist/local-exec-daemon/main.cjs` | 本地 `@electron/asar extract` 实测 |
| 2 | `~/.grokbot/settings.json` | 本地读取 |
| 3 | Claude Code · cli-reference（`--add-dir`） | `https://code.claude.com/docs/en/cli-reference.md` |
| 4 | Claude Code · settings（`additionalDirectories` 与 trust） | `https://code.claude.com/docs/en/settings.md` |
| 5 | Claude Code · permission-modes | `https://code.claude.com/docs/en/permission-modes.md` |
| 6 | Codex sandbox 文档转链 | `https://raw.githubusercontent.com/openai/codex/main/docs/sandbox.md`（正文未取到，不作断言） |

本仓库内相关：`docs/reviews/research-product-value-and-maturity.md`（同赛道竞品与成熟度）、`docs/design.md` §2.1（壳的选型）、§5.9b（权限模式）。