package builtin

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"time"
)

// 写安全：读的时候记版本，写的时候校版本。
//
// 为什么需要：多个会话 / 子 Agent 同时改同一个仓库时，"锚点还在"不等于
// "文件还是你读到的那一份"。典型事故是 A 读完、B 改了一行、A 再写——
// 锚点仍能唯一匹配，于是把 B 的改动覆盖掉，而且没有任何报错。
// 版本校验把这种静默覆盖变成一次明确的失败，让写入方重新读一遍。
//
// 机制刻意做得很轻：
//   - 只记在进程内（重启即失效，代价是重启后第一次写要重读，可接受）；
//
//   - 版本 = 文件大小 + 修改时间(纳秒) + 内容 sha1 前 8 位，三者任一变化都变；
//
//   - 不落盘、不做锁。锁挡不住"读—想—写"之间的那段窗口，版本校验正好覆盖它。

// fileVersion 是同一份文件的指纹。
type fileVersion struct {
	size int64
	mod  int64 // UnixNano
	sum  string
}

func (v fileVersion) String() string {
	return fmt.Sprintf("%d-%d-%s", v.size, v.mod, v.sum)
}

// short 是给模型看的形式：足够唯一，又不至于让每行输出都很长。
func (v fileVersion) short() string {
	if len(v.sum) >= 12 {
		return v.sum[:12]
	}
	return v.sum
}

// fingerprintFile 计算当前磁盘版本的指纹。
func fingerprintFile(abs string) (fileVersion, error) {
	st, err := os.Stat(abs)
	if err != nil {
		return fileVersion{}, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return fileVersion{}, err
	}
	sum := sha1.Sum(data)
	return fileVersion{
		size: st.Size(),
		mod:  st.ModTime().UnixNano(),
		sum:  hex.EncodeToString(sum[:]),
	}, nil
}

// readState 是"这个进程最后一次读到某个文件的哪个版本"。
//
// 键用绝对路径（调用方已 resolve 过 root 边界）。
// 多个会话共享同一张表：语义是"进程内最后一次读"，比按会话隔离更严格，
// 只会多报一次 READ 要求，不会漏报覆盖。
type readState struct {
	version string
	at      time.Time
}

var readStates = struct {
	sync.Mutex
	m map[string]readState
}{m: map[string]readState{}}

// RememberRead 记录一次读取的版本。供 ReadFile 调用。
func RememberRead(abs, version string) {
	readStates.Lock()
	readStates.m[abs] = readState{version: version, at: time.Now()}
	readStates.Unlock()
}

// ForgetRead 丢弃记录。删除文件后调用，避免后续 add 被旧版本拦住。
func ForgetRead(abs string) {
	readStates.Lock()
	delete(readStates.m, abs)
	readStates.Unlock()
}

func remembered(abs string) (readState, bool) {
	readStates.Lock()
	defer readStates.Unlock()
	st, ok := readStates.m[abs]
	return st, ok
}

// staleWriteError 是"文件已被别人改过"的结构化错误。
// 单独一个类型是为了让上层（审批预览、测试）能区分它和普通 IO 错误。
type staleWriteError struct {
	path   string
	reason string
	want   string
	have   string
}

func (e *staleWriteError) Error() string {
	return fmt.Sprintf(
		"STALE_WRITE: %s %s（读时 %s，现在 %s）。先 read_file 拿最新内容再改；"+
			"若确认要基于当前内容写，带上 ifVersion=%s。",
		e.path, e.reason, e.want, e.have, e.have)
}

// writeGuard 是写之前的唯一校验入口，返回当前版本（写成功后用它更新状态）。
//
// 规则（两条都要，缺一条就会漏掉一半场景）：
//  1. 给了 ifVersion：必须等于磁盘当前版本。等于说明"我要改的就是我手里这份"。
//  2. 没给：只在"本进程读过这个文件、且版本已经变了"时拒绝。
//     没读过的新文件不拦——否则模型写新文件要先读一个不存在的文件。
func writeGuard(abs, ifVersion string) (fileVersion, error) {
	cur, err := fingerprintFile(abs)
	if err != nil {
		return fileVersion{}, err
	}
	if ifVersion != "" {
		if ifVersion != cur.String() && ifVersion != cur.short() {
			return cur, &staleWriteError{
				path:   abs,
				reason: "的版本与 ifVersion 不一致，说明已被改过",
				want:   ifVersion,
				have:   cur.short(),
			}
		}
		return cur, nil
	}
	if st, ok := remembered(abs); ok && st.version != cur.String() {
		old := st.version
		if len(old) >= 12 {
			old = old[:12]
		}
		return cur, &staleWriteError{
			path:   abs,
			reason: "在你读取之后被改过（可能来自另一个会话）",
			want:   old,
			have:   cur.short(),
		}
	}
	return cur, nil
}

// rememberWrite 在写成功后刷新状态：此刻磁盘内容就是"最新读到的"。
func rememberWrite(abs string) {
	if v, err := fingerprintFile(abs); err == nil {
		RememberRead(abs, v.String())
	}
}

// versionHint 是附在 read_file 输出末尾的一行，让模型能把它带回 ifVersion。
// 不放在 Meta 里是因为 Meta 不回填模型上下文，模型就无从拿到这个值。
func versionHint(abs string) string {
	v, err := fingerprintFile(abs)
	if err != nil {
		return ""
	}
	RememberRead(abs, v.String())
	return fmt.Sprintf("\n[file version %s · 写这个文件时带上 ifVersion=%s]\n", v.short(), v.short())
}
