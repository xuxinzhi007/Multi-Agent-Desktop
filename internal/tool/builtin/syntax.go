package builtin

import (
	"fmt"
	"go/parser"
	"go/scanner"
	"go/token"
	"strings"
)

// 写入即诊断：写完 .go 文件立刻解析一遍，语法错误跟着同一次工具结果回去。
//
// 以前语法错误要等模型声称完成、调 verify 才暴露，中间它已经在坏文件上继续改了几步，
// 回头修的时候上下文里全是基于错误状态的推理。Aider 的做法是每次编辑后立刻 lint，
// 这里取最便宜的那一截：只做语法，只用标准库，毫秒级，不起子进程。
//
// 边界：
//   - 只是附注，不改状态。字节已经落盘；多步编辑的中间态允许暂时不合法，
//     所以不能拒绝写入，也不能把结果标成失败。
//   - 只查语法。类型、go vet、测试仍归 verify，那需要整个包的上下文。
//   - 只做 Go。别的语言没有标准库解析器，本轮不引入依赖。

const (
	// syntaxMaxErrors 是附在结果里的错误条数上限：一个漏掉的括号能连带出一串，
	// 全贴会淹掉真正的那一条。
	syntaxMaxErrors = 5
	// syntaxMaxBytes 以上的文件不解析，避免在生成的大文件上白花时间。
	syntaxMaxBytes = 2 << 20
)

// goSyntaxNote 解析 content。能解析、或不是 Go 文件时返回空串；
// 否则返回一段以 SYNTAX 开头、带 文件:行:列 的说明。rel 是给模型看的相对路径。
func goSyntaxNote(rel, content string) string {
	if !strings.HasSuffix(rel, ".go") || len(content) > syntaxMaxBytes {
		return ""
	}
	_, err := parser.ParseFile(token.NewFileSet(), rel, content, parser.SkipObjectResolution)
	if err == nil {
		return ""
	}
	list, ok := err.(scanner.ErrorList)
	if !ok || len(list) == 0 {
		return fmt.Sprintf("SYNTAX: %s no longer parses as Go after this write: %v", rel, err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "SYNTAX: %s no longer parses as Go after this write (it was written as given). Fix before moving on:\n", rel)
	shown := len(list)
	if shown > syntaxMaxErrors {
		shown = syntaxMaxErrors
	}
	for _, e := range list[:shown] {
		fmt.Fprintf(&b, "  %s:%d:%d: %s\n", rel, e.Pos.Line, e.Pos.Column, e.Msg)
	}
	if rest := len(list) - shown; rest > 0 {
		fmt.Fprintf(&b, "  (%d more not shown)\n", rest)
	}
	return strings.TrimRight(b.String(), "\n")
}
