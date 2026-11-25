// Package shellguard 在命令执行前挡掉明显会毁掉机器的写法。
//
// 这不是沙箱，也不可能覆盖所有变体。目标只有三个：
//   - 整盘/根目录/系统目录/家目录的删除与覆盖
//   - 关机、重启、清盘
//   - fork bomb
//
// 判定方式分两层：
//  1. 正则黑名单（直接认字面量）
//  2. 目标解析：把 $HOME、${HOME}、~ 这类写法展开成真实绝对路径，
//     再看这个路径是不是「根/系统目录/家目录本身」。第 2 层解决的是
//     `rm -rf $HOME`、`find ~ -delete` 这类黑名单永远追不完的变体。
//
// 误杀正常开发命令（rm -rf node_modules、git clean）比漏杀更糟，
// 所以只拦「目标是敏感目录本身」，相对路径与目录内的具体子目录一律放行。
package shellguard

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var rules = []struct {
	re     *regexp.Regexp
	reason string
}{
	{
		// 目标必须是「根/家目录/系统目录本身」才算。
		// `~/` 后面接具体子目录（~/Documents/proj）是正常工作区，不能拦。
		re:     regexp.MustCompile(`(?i)\brm\s+-[a-z]*(?:r[a-z]*f|f[a-z]*r)[a-z]*\s+(?:/|/\*|~|~/\*|~/|/etc|/usr|/var|/home|/System|/Library)(?:\s|$|/\*)`),
		reason: "拒绝删除根目录、家目录或系统目录",
	},
	{
		re:     regexp.MustCompile(`(?i)(remove-item|del|erase|rmdir)\s+.*?(c:\s*|c:/\s*|c:\*|c:/\*|/|%systemroot%|%windir%|~)\s*(/s|-recurse|-r|/f|-force|-rf)`),
		reason: "拒绝删除盘符根、系统目录或家目录",
	},
	{
		re:     regexp.MustCompile(`(?i)(remove-item|del|erase|rmdir)\s+.*?(/s|-recurse|-r|/f|-force|-rf).*?(c:\s*|c:/\s*|c:\*|c:/\*|/|%systemroot%|%windir%|~)`),
		reason: "拒绝删除盘符根、系统目录或家目录",
	},
	{
		re:     regexp.MustCompile(`(?i)\b(format-volume|clear-disk|initialize-disk|diskpart|mkfs(\.\w+)?)\b`),
		reason: "拒绝格式化或清空磁盘",
	},
	{
		// Windows 的 format 后面跟盘符才是真的在格式化；`dotnet format` 之类不匹配。
		re:     regexp.MustCompile(`(?i)\bformat\s+([a-z]:(\s|$)|/fs:)`),
		reason: "拒绝格式化磁盘",
	},
	{
		re:     regexp.MustCompile(`(?i)\bdd\s+.*of=/dev/(sd|nvme|hd|disk)`),
		reason: "拒绝直接写块设备",
	},
	{
		re:     regexp.MustCompile(`(?i)\b(shutdown|restart-computer|stop-computer)\b`),
		reason: "拒绝关机或重启",
	},
	{
		re:     regexp.MustCompile(`(?i):\s*\(\s*\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;\s*:`),
		reason: "拒绝 fork bomb",
	},
}

// Check 返回是否拦截以及原因。
func Check(command string) (bool, string) {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return false, ""
	}
	// 去掉多余空白，避免用换行/多空格绕过简单规则。
	cmd = strings.Join(strings.Fields(cmd), " ")
	for _, r := range rules {
		if r.re.MatchString(cmd) {
			return true, r.reason
		}
	}
	return checkTargets(cmd)
}

// sensitivePaths 是「删了就回不来」的目录。比对的是目录本身，不是它下面的子目录。
var sensitivePaths = buildSensitive()

func buildSensitive() map[string]string {
	out := map[string]string{
		"/": "根目录", "/etc": "系统目录", "/usr": "系统目录", "/var": "系统目录",
		"/bin": "系统目录", "/sbin": "系统目录", "/boot": "系统目录", "/lib": "系统目录",
		"/System": "系统目录", "/Library": "系统目录", "/Applications": "系统目录",
		"/private": "系统目录", "/Users": "用户目录根", "/home": "用户目录根",
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && home != "/" {
		// 家目录本身算敏感：删掉它就是丢掉自己的所有文件。
		// ~/Documents 这类子目录不算 —— 那是正常的开发操作。
		out[filepath.Clean(home)] = "家目录"
	}
	return out
}

// varPattern 匹配命令里的 shell 变量与家目录缩写。
var varPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// targetPattern 抓「删除、移动、拷盖、改权限」这类命令的目标参数。
// 只取命令词后面第一个看起来像路径的参数，够用且不误杀。
var targetPattern = regexp.MustCompile(`(?i)\b(rm|rmdir|mv|chmod|chown|truncate|shred|srm|find)\b([^|;&]*)$`)

// splitCommands 把一行拆成多条命令，避免只看第一条。
func splitCommands(cmd string) []string {
	parts := strings.FieldsFunc(cmd, func(r rune) bool {
		return r == ';' || r == '&' || r == '|' || r == '\n'
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// expandTarget 把 $HOME、${HOME}、$USER、~ 展开成可比较的绝对路径。
// 展开不了就原样返回，后面的绝对路径判定会跳过它。
func expandTarget(s string) string {
	home, _ := os.UserHomeDir()
	switch {
	case s == "~" || s == "~/":
		if home != "" {
			return home
		}
	case strings.HasPrefix(s, "~/"):
		if home != "" {
			return filepath.Join(home, s[2:])
		}
	}
	return varPattern.ReplaceAllStringFunc(s, func(m string) string {
		name := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(m, "${"), "$"), "}")
		if name == "HOME" && home != "" {
			return home
		}
		if v, ok := os.LookupEnv(name); ok && v != "" {
			return v
		}
		return m
	})
}

// checkTargets 检查每条子命令的路径目标是否落在敏感目录本身。
func checkTargets(cmd string) (bool, string) {
	for _, sub := range splitCommands(cmd) {
		m := targetPattern.FindStringSubmatch(sub)
		if m == nil {
			continue
		}
		// 只看命令词后面的参数，命令词本身不参与判定。
		for _, field := range strings.Fields(m[2]) {
			if strings.HasPrefix(field, "-") {
				continue
			}
			target := expandTarget(strings.Trim(field, `"'`))
			if !filepath.IsAbs(target) {
				// 相对路径不可能是根或家目录本身，放开。
				continue
			}
			clean := filepath.Clean(target)
			if why, ok := sensitivePaths[clean]; ok {
				return true, "拒绝操作" + why + "（" + clean + "），这是不可恢复的"
			}
		}
	}
	return false, ""
}
