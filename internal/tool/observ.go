// Package tool 里的「观察原文留存」：工具输出的完整版写在磁盘上，
// 上下文里只留一个带 call_id 的指针。
//
// 为什么单独一个文件：写盘要在两个地方用到 —— Registry.truncate（超长输出直接裁）
// 和 loop 的折叠层（400B~8KB 之间不会被裁，但要折叠）。两处必须用同一份命名规则，
// 否则 restore 找不到文件。
package tool

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ObservationsDir 是观察原文的目录名，放在会话数据目录下。
const ObservationsDir = "observations"

// observationName 把外部传来的 call_id 收成一个安全文件名。
//
// call_id 来自模型/宿主，不能当路径用：`../..` 之类会把文件写到目录外。
// 合法字符直接用，其余折叠成 sha1 前 12 位，保证同名可复现。
func observationName(callID string) string {
	trimmed := strings.TrimSpace(callID)
	if trimmed == "" {
		return ""
	}
	ok := true
	for _, r := range trimmed {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		ok = false
		break
	}
	if ok && len(trimmed) <= 96 {
		return trimmed + ".txt"
	}
	sum := sha1.Sum([]byte(trimmed))
	return fmt.Sprintf("x-%s.txt", hex.EncodeToString(sum[:])[:12])
}

// SaveObservation 把原文写到 <dir>/observations/<call_id>.txt。
//
// 幂等：文件已存在且长度一致就不再写。同一轮里 truncate 与 mask 都会调用，
// 重复写没有意义，也避免把更长/更短的版本互相覆盖。
func SaveObservation(dir, callID string, content []byte) (string, error) {
	name := observationName(callID)
	if name == "" {
		return "", fmt.Errorf("call_id 为空，无法留存原文")
	}
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("没有配置观察原文目录")
	}
	obs := filepath.Join(dir, ObservationsDir)
	if err := os.MkdirAll(obs, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(obs, name)
	if st, err := os.Stat(path); err == nil && st.Size() == int64(len(content)) {
		return path, nil
	}
	// 先写临时文件再 rename：中途崩了不会留下半截文件被 restore 读到。
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return path, nil
}

// ReadObservation 读回某个调用的原文。找不到时返回明确的「没有留存」错误，
// 让工具能给模型一句可执行的话，而不是空字符串。
func ReadObservation(dir, callID string) ([]byte, error) {
	name := observationName(callID)
	if name == "" {
		return nil, fmt.Errorf("call_id 为空")
	}
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("没有配置观察原文目录")
	}
	data, err := os.ReadFile(filepath.Join(dir, ObservationsDir, name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("这次调用没有留存原文（可能输出较小，或会话已被清理）")
		}
		return nil, err
	}
	return data, nil
}

// ObservationPlaceholder 是折叠后的占位符。
//
// 必须带上可复制的调用示例：模型看到「已折叠」不一定知道下一步该干什么，
// 直接把 restore 调用写给它，这一步在 gp 上是实测有效的。
func ObservationPlaceholder(toolName, callID string, lines, sizeBytes int) string {
	if toolName == "" {
		toolName = "unknown"
	}
	return fmt.Sprintf(
		"[已折叠工具输出: tool=%s call_id=%q | 原文 %d 行 / %s; 需要原文时调用 restore_observation(call_id=%q)]",
		toolName, callID, lines, HumanBytes(sizeBytes), callID,
	)
}

// HumanBytes 把字节数变成人看得懂的大小，占位符和报错都用它。
func HumanBytes(b int) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%dB", b)
	}
}
