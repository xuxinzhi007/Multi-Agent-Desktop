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
}

func (*RepoMap) Name() string          { return "repo_map" }
func (*RepoMap) Writes() bool          { return false }
func (*RepoMap) ConcurrencySafe() bool { return true }

func (*RepoMap) Description() string {
	return "List declarations (functions, types, consts) of Go/TS files under a directory, " +
		"so you can navigate without reading whole files. Capped at ~200 lines."
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
}

// Execute 扫描并渲染。focus 命中越多、修改越近，排得越前。
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
			line := fmt.Sprintf("  %s %s", s.kind, s.name)
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

// scan 遍历目录、解析符号、按"focus 命中 + 最近修改"排序。
func (r *RepoMap) scan(root, focus string) []repoFile {
	var files []repoFile
	visited := 0
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
		if len(syms) > repoMapMaxSyms {
			syms = syms[:repoMapMaxSyms]
		}
		f := repoFile{rel: rel, mod: info.ModTime(), symbols: syms}
		if focus != "" {
			if strings.Contains(strings.ToLower(rel), focus) {
				f.score += 3
			}
			for _, s := range syms {
				if strings.Contains(strings.ToLower(s.name), focus) || strings.Contains(strings.ToLower(s.doc), focus) {
					f.score += 2
				}
			}
		}
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
			out = append(out, repoSymbol{kind: "func", name: name + "()", doc: docOf(d.Doc)})
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
					out = append(out, repoSymbol{kind: kind, name: s.Name.Name, doc: docOf(firstDoc(d.Doc, s.Doc))})
				case *ast.ValueSpec:
					kind := "var"
					if d.Tok == token.CONST {
						kind = "const"
					}
					for _, n := range s.Names {
						out = append(out, repoSymbol{kind: kind, name: n.Name, doc: docOf(firstDoc(d.Doc, s.Doc))})
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
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		m := tsDecl.FindStringSubmatch(trimmed)
		if m == nil {
			continue
		}
		name := m[2]
		if m[1] == "function" {
			name += "()"
		}
		out = append(out, repoSymbol{kind: m[1], name: name})
		if len(out) >= repoMapMaxSyms {
			break
		}
	}
	return out
}
