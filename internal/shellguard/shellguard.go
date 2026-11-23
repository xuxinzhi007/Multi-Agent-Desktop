// Package shellguard 在命令执行前挡掉明显会毁掉机器的写法。
//
// 这不是沙箱，也不可能覆盖所有变体。目标只有三个：
//   - 整盘/根目录/系统目录的删除与格式化
//   - 关机、重启、清盘
//   - fork bomb
//
// 误杀正常开发命令（rm -rf node_modules、git clean）比漏杀更糟，
// 所以规则只锚定根目录、盘符、系统目录和家目录，不按关键字一刀切。
package shellguard

import (
	"regexp"
	"strings"
)

var rules = []struct {
	re     *regexp.Regexp
	reason string
}{
	{
		re:     regexp.MustCompile(`(?i)\brm\s+-[a-z]*r[a-z]*f[a-z]*\s+(/|/\*|~|~/\*|~\s|/etc|/usr|/var|/home)`),
		reason: "拒绝删除根目录、家目录或系统目录",
	},
	{
		re:     regexp.MustCompile(`(?i)\brm\s+-[a-z]*f[a-z]*r[a-z]*\s+(/|/\*|~|~/\*|/etc|/usr|/var|/home)`),
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
	return false, ""
}
