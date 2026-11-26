package approval

import "strings"

// subcommandTools 是带子命令的多段工具：记忆签名要取到第二个 token 才够细，
// 否则记住 `git status` 会把 `git push` 一并放过。
var subcommandTools = map[string]bool{
	"git":     true,
	"go":      true,
	"npm":     true,
	"npx":     true,
	"pnpm":    true,
	"yarn":    true,
	"cargo":   true,
	"docker":  true,
	"kubectl": true,
	"make":    true,
}

// bashConnectors 是不能作为命令首 token 的连接符 / 重定向符。
var bashConnectors = map[string]bool{
	"|": true, "||": true, "&&": true, "&": true, ";": true, ";;": true,
	">": true, ">>": true, "<": true, "<<": true, ">|": true, "(": true, ")": true,
}

// bashSignature 从一条 bash 命令里抽出「命令族签名」。规则确定性、可解释：
//   - 单段工具（ls / cat / rm 等）：签名 = 第一个 token；
//   - 多段工具（git / go / npm / npx / pnpm / yarn / cargo / docker / kubectl /
//     make）：签名 = 第一个 token + 第二个 token（子命令），例如 `go test`、
//     `git status`；同子命令带不同参数视为同签名（`git status` ≡ `git status --short`）。
//
// 解析不出时返回 ("", false)，调用方不入记忆、保守询问。退化情形：
// 命令为空、首 token 是管道 / 重定向 / 连接符、引号未闭合、多段工具缺子命令。
// 退化是有意的：拿不准命令族就别把记忆记宽，宁可贵一次点击，不放开一类命令。
func bashSignature(command string) (string, bool) {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return "", false
	}
	if unclosedQuote(cmd) {
		return "", false
	}
	tokens := shellTokens(cmd)
	if len(tokens) == 0 || bashConnectors[tokens[0]] {
		return "", false
	}
	base := tokens[0]
	if subcommandTools[base] {
		if len(tokens) < 2 {
			return "", false
		}
		return base + " " + tokens[1], true
	}
	return base, true
}

// unclosedQuote 报告字符串里单/双引号是否有未闭合的一侧。
func unclosedQuote(s string) bool {
	var single, double bool
	for _, r := range s {
		switch r {
		case '\'':
			single = !single
		case '"':
			double = !double
		}
	}
	return single || double
}

// shellTokens 按空白切分，保留单/双引号内的空白，并剥掉引号本身。
func shellTokens(s string) []string {
	var tokens []string
	var cur strings.Builder
	inSingle, inDouble := false, false
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r == '\'' && !inDouble:
			inSingle = !inSingle
		case r == '"' && !inSingle:
			inDouble = !inDouble
		case (r == ' ' || r == '\t' || r == '\n') && !inSingle && !inDouble:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return tokens
}
