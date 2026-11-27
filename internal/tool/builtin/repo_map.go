package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"multi-agent-desktop/internal/tool"
)

// RepoMap 给模型一张"这个目录里有什么"的符号级地图。
//
// 为什么不是向量索引 / 全仓 AST 数据库：那需要常驻服务与持久化，维护成本远超收益。
// 模型真正缺的是"知道有哪几个函数、在哪个文件"，一次 200 行的清单就能省掉
// 五六次 grep + read。目录结构 + 声明名 + 首行注释，足够做导航。
//
// 边界都写死：文件数、每文件符号数、总行数、单文件大小。宁可少给，不许卡死。
type RepoMap struct {
	WorkDir   string
	ExtraDirs []string
	// Scope 是会话 id。非空时，本会话写过的文件在地图里排前面。
	Scope string
	// Request 是这一轮用户的原话。里面点到的声明名和路径段决定哪些文件排前面。
	Request string
}

func (*RepoMap) Name() string          { return "repo_map" }
func (*RepoMap) Writes() bool          { return false }
func (*RepoMap) ConcurrencySafe() bool { return true }

func (*RepoMap) Description() string {
	return "List declarations (functions, types, consts) of Go/TS files under a directory with their line numbers, " +
		"so you can navigate without reading whole files and pass the line to read_file as offset. " +
		"Files and declarations named in the current request, or edited earlier in this session, come first. " +
		"Capped at ~200 lines."
}

func (*RepoMap) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "Directory to scan, relative to the working directory. Defaults to '.'."},
			"focus": {"type": "string", "description": "Optional keyword; files and symbols matching it are listed first."},
			"maxLines": {"type": "integer", "minimum": 40, "maximum": 400, "description": "Output line budget, default 220."}
		},
		"additionalProperties": false
	}`)
}

const (
	repoMapMaxFiles     = 400
	repoMapMaxSyms      = 12
	repoMapMaxFileBytes = 512 << 10
	repoMapDefaultLines = 220
	// repoMapParseSyms 是每个文件最多解析的声明数。先全解析、排完序再截到
	// repoMapMaxSyms：点名的声明排在文件靠后的位置时不能被提前截掉。
	repoMapParseSyms = 400
	// repoMapMaxAsked 是从用户原话里最多取多少个标识符，防止贴进来的长日志拖慢排序。
	repoMapMaxAsked = 64
)

// 排序权重。点名的声明 > 本会话写过 > 路径对得上 > focus 关键词。
const (
	repoMapScoreAskedSym  = 6
	repoMapScoreWritten   = 5
	repoMapScoreAskedPath = 4
	repoMapScoreFocusPath = 3
	repoMapScoreFocusSym  = 2
)

// repoMapExtraSkip 是在共享 skipDirs 之外、这张地图同样不该扫的目录。
// 工作树目录必须跳过：否则每张地图都会把自己那份检出列一遍。
var repoMapExtraSkip = map[string]bool{
	"vendor": true, "target": true, ".next": true, "coverage": true, ".mad-worktrees": true,
}

func skipForMap(name string) bool {
	return skipDirs[name] || repoMapExtraSkip[name]
}

type repoFile struct {
	rel     string
	mod     time.Time
	symbols []repoSymbol
	score   int
}

type repoSymbol struct {
	kind string // func / type / const / var / class / interface
	name string
	doc  string
	line int  // 声明所在行，1 基
	hit  bool // 被 focus 或用户原话点到，文件内排前面
}

// Execute 扫描并渲染。排序依据见 rank：原话点名、本会话写过、focus 命中、修改时间。
func (r *RepoMap) Execute(ctx context.Context, raw json.RawMessage) tool.Result {
	var in struct {
		Path     string `json:"path"`
		Focus    string `json:"focus"`
		MaxLines int    `json:"maxLines"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		res := tool.Fail("cannot parse arguments: %v", err)
		res.Meta.BadArgs = true
		return res
	}
	root, err := pickRoot(r.WorkDir, r.ExtraDirs, in.Path)
	if err != nil {
		return tool.Fail("%v", err)
	}
	budget := in.MaxLines
	if budget <= 0 {
		budget = repoMapDefaultLines
	}

	start := time.Now()
	files := r.scan(root, strings.ToLower(strings.TrimSpace(in.Focus)))
	if len(files) == 0 {
		return tool.Fail("no Go/TS files found under %s", displayPath(in.Path))
	}

	var b strings.Builder
	lines := 0
	shown := 0
	// 每一行写入前都查预算：maxLines 是硬上限，输出不能是 N+1 行。
	for _, f := range files {
		if lines >= budget {
			break
		}
		fmt.Fprintf(&b, "%s\n", f.rel)
		lines++
		shown++
		for _, s := range f.symbols {
			if lines >= budget {
				break
			}
			line := fmt.Sprintf("  %d: %s %s", s.line, s.kind, s.name)
			if s.doc != "" {
				line += "  // " + s.doc
			}
			b.WriteString(line + "\n")
			lines++
		}
	}
	if lines < budget {
		fmt.Fprintf(&b, "\n(%d files shown, %d symbols, %dms)",
			shown, countSymbols(files[:shown]), time.Since(start).Milliseconds())
	}

	res := tool.OK(b.String())
	res.Meta.DurationMS = int(time.Since(start).Milliseconds())
	return res
}

// pickRoot 解析要扫的目录，并限制在工作区根之内。
func pickRoot(workDir string, extra []string, rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		rel = "."
	}
	return rootsPath(workDir, extra, rel)
}

// scan 遍历目录、解析符号、打分排序。同分时最近修改的在前。
func (r *RepoMap) scan(root, focus string) []repoFile {
	var files []repoFile
	visited := 0
	asked := askedIdents(r.Request)
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // 无权限的子目录跳过，不让整张地图失败
		}
		if d.IsDir() {
			if skipForMap(d.Name()) && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !isCodeFile(d.Name()) {
			return nil
		}
		if visited >= repoMapMaxFiles {
			return filepath.SkipAll
		}
		info, err := d.Info()
		if err != nil || info.Size() > repoMapMaxFileBytes {
			return nil
		}
		visited++
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		syms := parseSymbols(path)
		if len(syms) == 0 {
			return nil
		}
		f := repoFile{rel: rel, mod: info.ModTime(), symbols: syms}
		f.rank(focus, asked, wroteInSession(r.Scope, path))
		files = append(files, f)
		return nil
	})

	sort.SliceStable(files, func(i, j int) bool {
		if files[i].score != files[j].score {
			return files[i].score > files[j].score
		}
		return files[i].mod.After(files[j].mod)
	})
	return files
}

// rank 给文件打分，并把被点到的声明提到文件内的前面，再截到每文件上限。
//
// 思路来自 Aider 的 repo map：地图不是目录清单，是"和这句话最相关的那部分代码"。
// 它用引用图排序；这里没有引用图，只用三个不需要索引的信号：
// 用户原话里出现的标识符、本会话写过的文件、调用方给的 focus 关键词。
func (f *repoFile) rank(focus string, asked map[string]bool, written bool) {
	if written {
		f.score += repoMapScoreWritten
	}
	if focus != "" && strings.Contains(strings.ToLower(f.rel), focus) {
		f.score += repoMapScoreFocusPath
	}
	for _, seg := range pathIdents(f.rel) {
		if asked[seg] {
			f.score += repoMapScoreAskedPath
		}
	}
	for i := range f.symbols {
		s := &f.symbols[i]
		if focus != "" && (strings.Contains(strings.ToLower(s.name), focus) || strings.Contains(strings.ToLower(s.doc), focus)) {
			f.score += repoMapScoreFocusSym
			s.hit = true
		}
		for _, part := range symbolIdents(s.name) {
			if asked[part] {
				f.score += repoMapScoreAskedSym
				s.hit = true
				break
			}
		}
	}
	// 稳定排序：命中的在前，其余保持源码顺序。
	sort.SliceStable(f.symbols, func(i, j int) bool { return f.symbols[i].hit && !f.symbols[j].hit })
	if len(f.symbols) > repoMapMaxSyms {
		f.symbols = f.symbols[:repoMapMaxSyms]
	}
}

var identRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// genericWords 是原话里不算"点名"的词：英文虚词，以及几乎每个仓库里都到处是的名字。
// 不过滤的话，一句"这个 error 哪来的"会把所有带 Error() 方法的文件顶到最前面。
var genericWords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "this": true, "that": true, "from": true,
	"not": true, "but": true, "are": true, "was": true, "can": true, "why": true, "how": true,
	"what": true, "when": true, "where": true, "which": true, "should": true, "would": true,
	"please": true, "does": true, "did": true, "fix": true, "bug": true, "add": true, "use": true,
	"new": true, "get": true, "set": true, "run": true, "make": true,
	"file": true, "files": true, "code": true, "line": true, "lines": true, "todo": true,
	"func": true, "function": true, "method": true, "type": true, "struct": true, "interface": true,
	"const": true, "var": true, "package": true, "import": true, "return": true,
	"error": true, "errors": true, "err": true, "string": true, "int": true, "bool": true,
	"nil": true, "null": true, "true": true, "false": true,
	"test": true, "tests": true, "main": true, "init": true,
}

// askedIdents 从用户原话里取出像标识符的词（小写）。中文原话里夹的英文基本就是
// 函数名、类型名和路径，所以不需要分词，只按标识符形状取。
func askedIdents(request string) map[string]bool {
	if strings.TrimSpace(request) == "" {
		return nil
	}
	out := map[string]bool{}
	for _, m := range identRe.FindAllString(request, -1) {
		w := strings.ToLower(m)
		if len(w) < 3 || genericWords[w] {
			continue
		}
		out[w] = true
		if len(out) >= repoMapMaxAsked {
			break
		}
	}
	return out
}

// pathIdents 把相对路径拆成可以和原话比对的段：每层目录名，加上去掉扩展名的文件名。
func pathIdents(rel string) []string {
	segs := strings.Split(strings.ToLower(rel), "/")
	last := len(segs) - 1
	if i := strings.IndexByte(segs[last], '.'); i > 0 {
		segs[last] = segs[last][:i]
	}
	return segs
}

// symbolIdents 把 `Server.Handler()` 拆成 server、handler。
func symbolIdents(name string) []string {
	return strings.Split(strings.ToLower(strings.TrimSuffix(name, "()")), ".")
}

func isCodeFile(name string) bool {
	switch {
	case strings.HasSuffix(name, ".go"):
		return true
	case strings.HasSuffix(name, ".ts"), strings.HasSuffix(name, ".tsx"):
		return !strings.HasSuffix(name, ".d.ts")
	}
	return false
}

func countSymbols(files []repoFile) int {
	n := 0
	for _, f := range files {
		n += len(f.symbols)
	}
	return n
}

// parseSymbols 取出文件的声明清单。Go 用标准库 AST（准确），TS 用行级正则（够用）。
func parseSymbols(path string) []repoSymbol {
	if strings.HasSuffix(path, ".go") {
		return goSymbols(path)
	}
	return tsSymbols(path)
}

func goSymbols(path string) []repoSymbol {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil
	}
	var out []repoSymbol
	lineOf := func(p token.Pos) int { return fset.Position(p).Line }
	docOf := func(cg *ast.CommentGroup) string {
		if cg == nil {
			return ""
		}
		text := strings.TrimSpace(cg.Text())
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[:i]
		}
		return strings.TrimSpace(text)
	}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			if d.Recv != nil && len(d.Recv.List) > 0 {
				recv := exprName(d.Recv.List[0].Type)
				if recv != "" {
					name = recv + "." + name
				}
			}
			out = append(out, repoSymbol{kind: "func", name: name + "()", doc: docOf(d.Doc), line: lineOf(d.Pos())})
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					kind := "type"
					switch s.Type.(type) {
					case *ast.StructType:
						kind = "struct"
					case *ast.InterfaceType:
						kind = "interface"
					}
					out = append(out, repoSymbol{kind: kind, name: s.Name.Name, doc: docOf(firstDoc(d.Doc, s.Doc)), line: lineOf(s.Pos())})
				case *ast.ValueSpec:
					kind := "var"
					if d.Tok == token.CONST {
						kind = "const"
					}
					for _, n := range s.Names {
						out = append(out, repoSymbol{kind: kind, name: n.Name, doc: docOf(firstDoc(d.Doc, s.Doc)), line: lineOf(n.Pos())})
					}
				}
			}
		}
	}
	return out
}

func firstDoc(a, b *ast.CommentGroup) *ast.CommentGroup {
	if a != nil {
		return a
	}
	return b
}

// exprName 把接收者类型拍平成 `*Server` / `Server`，只用于显示。
func exprName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return exprName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return exprName(t.X)
	case *ast.IndexListExpr:
		return exprName(t.X)
	}
	return ""
}

var tsDecl = regexp.MustCompile(`^(?:export\s+)?(?:default\s+)?(?:declare\s+)?(?:async\s+)?` +
	`(function|class|interface|type|enum|const|let)\s+([A-Za-z_$][A-Za-z0-9_$]*)`)

func tsSymbols(path string) []repoSymbol {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []repoSymbol
	for i, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		m := tsDecl.FindStringSubmatch(trimmed)
		if m == nil {
			continue
		}
		name := m[2]
		if m[1] == "function" {
			name += "()"
		}
		out = append(out, repoSymbol{kind: m[1], name: name, line: i + 1})
		if len(out) >= repoMapParseSyms {
			break
		}
	}
	return out
}
