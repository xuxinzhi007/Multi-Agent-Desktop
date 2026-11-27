package clitui

import (
	"strings"
	"testing"
)

func suggestFixture() []SuggestItem {
	return []SuggestItem{
		{Name: "/help", Hint: "帮助"},
		{Name: "/status", Hint: "状态"},
		{Name: "/context", Hint: "上下文"},
		{Name: "/compact", Args: "[保留重点]", Hint: "压缩"},
		{Name: "/copy", Args: "[序号]", Hint: "复制"},
		{Name: "/cost", Hint: "消耗"},
		{Name: "/cd", Args: "[目录]", Hint: "切目录"},
		{Name: "/add-dir", Args: "[目录]", Hint: "附加目录"},
	}
}

func TestMatchScoreRanks(t *testing.T) {
	cases := []struct {
		name, query string
		want        int
	}{
		{"/compact", "compact", MatchExact},
		{"/compact", "com", MatchPrefix},
		{"/add-dir", "dir", MatchWordBoundary},
		{"/context", "ext", MatchSubstring},
		{"/compact", "xyz", MatchNone},
	}
	for _, c := range cases {
		if got := MatchScore(c.name, c.query); got != c.want {
			t.Fatalf("MatchScore(%q,%q)=%d want %d", c.name, c.query, got, c.want)
		}
	}
	// 子序列命中（/cmp → /compact）要落在子串命中之下、无效匹配之上
	if got := MatchScore("/compact", "cmp"); got <= MatchNone || got >= MatchSubstring {
		t.Fatalf("fuzzy score = %d, want (%d,%d)", got, MatchNone, MatchSubstring)
	}
	if MatchScore("/context", "co") != MatchPrefix {
		// 前缀命中时不得被降级成词首或子串
		t.Fatalf("prefix should win for /context vs co")
	}
}

func TestRankItemsOrdersByStrength(t *testing.T) {
	items := suggestFixture()
	ranked := RankItems(items, "co")
	if len(ranked) != 4 {
		t.Fatalf("want 4 prefix matches for co, got %d", len(ranked))
	}
	for _, it := range ranked {
		if MatchScore(it.Name, "co") < MatchPrefix {
			t.Fatalf("%s should be prefix match for co", it.Name)
		}
	}
	// 前缀命中里命令名更短者应当在前（/copy 与 /cost 同名长度，按字典序）
	if ranked[0].Name != "/copy" {
		t.Fatalf("first = %s, want /copy", ranked[0].Name)
	}

	// 词首命中（/add-dir 的 dir）应排在任何子序列命中之前
	if got := RankItems(items, "dir")[0].Name; got != "/add-dir" {
		t.Fatalf("for dir first = %s, want /add-dir", got)
	}
}

func TestSuggestFuzzyAndExact(t *testing.T) {
	th := DefaultTheme()
	sg := NewSuggest(60, suggestFixture(), th)

	sg = sg.Update("/cmp")
	if !sg.Visible() || len(sg.Matches()) != 1 || sg.Matches()[0].Name != "/compact" {
		t.Fatalf("子序列补全失败: %#v", sg.Matches())
	}
	if sg.ExactMatch() {
		t.Fatal("/cmp 不是完整指令，不应判定为 exact")
	}

	sg = sg.Update("/compact")
	if !sg.ExactMatch() {
		t.Fatal("/compact 应判定为完整指令")
	}

	sg = sg.Update("/")
	if !sg.Visible() || len(sg.Matches()) != len(suggestFixture()) {
		t.Fatalf("空查询应展示全部指令")
	}
	if sg.Matches()[0].Name != "/help" {
		t.Fatalf("空查询应保留原顺序，首个是 %s", sg.Matches()[0].Name)
	}
}

func TestSuggestRowsHideArgSyntax(t *testing.T) {
	th := DefaultTheme()
	items := []SuggestItem{
		{Name: "/skills", Args: "[reload|<技能名>]", Hint: "查看或激活技能"},
		{Name: "/migrate-to-skills", Args: "[任务说明]", Hint: "把 Codex 规则转成技能", Kind: "技能"},
	}
	sg := NewSuggest(90, items, th).Update("/sk")
	view := sg.View()
	// 列表行不该再堆参数方括号；参数只出现在底部“用法”行
	rows := strings.Split(view, "\n")
	for _, ln := range rows[1 : len(rows)-3] {
		if strings.Contains(ln, "[reload") || strings.Contains(ln, "[任务说明]") {
			t.Fatalf("列表行混入了参数签名：%q", ln)
		}
	}
	if !strings.Contains(view, "用法 /skills [reload|<技能名>]") {
		t.Fatalf("底部缺少用法行：\n%s", view)
	}
	if !strings.Contains(view, "技能") {
		t.Fatalf("技能来源标签缺失：\n%s", view)
	}
}

func TestSuggestHighlightsOnlyConfidentMatches(t *testing.T) {
	th := DefaultTheme()
	sg := NewSuggest(80, suggestFixture(), th)

	sg = sg.Update("/cmp") // 子序列命中，没有把握
	if _, ok := sg.Selected(); ok {
		t.Fatal("模糊命中不应存在高亮项")
	}
	if !strings.Contains(sg.View(), "Enter 提交") {
		t.Fatalf("无高亮时应提示 Enter 提交：\n%s", sg.View())
	}

	sg = sg.Update("/co") // 前缀命中
	if sel, ok := sg.Selected(); !ok || sel.Name != "/copy" {
		t.Fatalf("前缀命中应高亮首项，got %+v ok=%v", sel, ok)
	}
	if !strings.Contains(sg.View(), "Enter 补全") {
		t.Fatalf("有高亮时应提示 Enter 补全：\n%s", sg.View())
	}
}

func TestSuggestKindOrderingTies(t *testing.T) {
	th := DefaultTheme()
	items := []SuggestItem{
		{Name: "/git-check", Args: "[任务说明]", Hint: "技能版", Kind: "技能"},
		{Name: "/git", Hint: "内置"},
	}
	// 用非完整查询（/g），此时面板仍列出候选，比拼的是排序而不是“唯一项”
	sg := NewSuggest(80, items, th).Update("/g")
	if len(sg.Matches()) != 2 {
		t.Fatalf("应有两条候选，got %+v", sg.Matches())
	}
	if sg.Matches()[0].Name != "/git" {
		t.Fatalf("同分时内置指令应排在技能之前，got %+v", sg.Matches())
	}
}

func TestSuggestArgStage(t *testing.T) {
	th := DefaultTheme()
	argItems := map[string][]SuggestItem{
		"/skills": {
			{Name: "reload", Hint: "重新扫描", Kind: "子命令"},
			{Name: "window-memory", Hint: "内存", Kind: "技能"},
			{Name: "migrate-to-skills", Hint: "迁移", Kind: "技能"},
		},
	}
	sg := NewSuggest(100, suggestFixture(), th).SetArgItems(argItems)

	// 空参数：把整套子项铺开，方便浏览
	sg = sg.Update("/skills ")
	if sg.Mode() != ModeArg || !sg.Visible() || len(sg.Matches()) != 3 {
		t.Fatalf("二级空参数应展开全部子项，mode=%v matches=%d", sg.Mode(), len(sg.Matches()))
	}

	// 带前导斜杠的输入也要能命中
	sg = sg.Update("/skills /win")
	sel, ok := sg.Selected()
	if !ok || sel.Name != "window-memory" {
		t.Fatalf("二级匹配失败：%+v ok=%v", sel, ok)
	}
	if got := sg.Completion(sel); got != "/skills window-memory" {
		t.Fatalf("补全应保留命令前缀，got %q", got)
	}

	// 未登记二级候选、但确实存在的指令：进自由文本参数态，但**不占屏**
	// （不需要做选择的地方不弹面板），Enter 直接把参数带走执行。
	sg = sg.Update("/help me")
	if sg.Mode() != ModeArgFree {
		t.Fatalf("自由文本参数应进入 ModeArgFree，mode=%v", sg.Mode())
	}
	if sg.Visible() || sg.Height() != 0 || sg.View() != "" {
		t.Fatalf("自由文本参数不该占屏，visible=%v height=%d", sg.Visible(), sg.Height())
	}
	if !sg.ExecutesOnEnter() {
		t.Fatalf("自由文本参数态下 Enter 必须是执行")
	}

	// 命令名已经完整：同样不弹面板，Enter 直接执行
	sg = sg.Update("/help")
	if sg.Visible() {
		t.Fatalf("完整指令不该再弹面板（那是自我重复），visible=%v", sg.Visible())
	}
	if !sg.ExecutesOnEnter() {
		t.Fatalf("完整指令回车必须执行")
	}

	// 尾随空格：不许把面板搞没，也不许让 Enter 变成补全
	sg = sg.Update("/skills ")
	if !sg.ExecutesOnEnter() {
		t.Fatalf("空参数 /skills 后回车应执行（列出技能），不应补成 reload")
	}

	// 真正不存在的指令不该冒充参数态
	sg = sg.Update("/nope x")
	if sg.Mode() != ModeNone || sg.Visible() {
		t.Fatalf("未知指令不应进入参数态，mode=%v", sg.Mode())
	}
}

func TestSuggestPathStage(t *testing.T) {
	th := DefaultTheme()
	paths := func(string, int) []SuggestItem {
		return []SuggestItem{
			{Name: "internal/clitui/", Kind: "目录"},
			{Name: "internal/clitui/suggest.go", Kind: "文件"},
			{Name: "internal/clitui/suggest_test.go", Kind: "文件"},
			{Name: "docs/design.md", Kind: "文件"},
		}
	}
	sg := NewSuggest(100, suggestFixture(), th).SetPathLister(paths)

	sg = sg.Update("@sug")
	if sg.Mode() != ModePath || !sg.Visible() {
		t.Fatalf("@ 应进入路径模式，mode=%v", sg.Mode())
	}
	sel, ok := sg.Selected()
	if !ok || !strings.HasSuffix(sel.Name, "suggest.go") {
		t.Fatalf("路径命中不对：%+v", sel)
	}
	if got := sg.Completion(sel); got != "@internal/clitui/suggest.go" {
		t.Fatalf("路径补全应带上 @ 与完整路径，got %q", got)
	}

	// 出现在句中时，只替换 @ 之后的片段
	sg = sg.Update("帮我看看 @internal/clitui/sug")
	if got := sg.Completion(mustSelect(t, sg)); got != "帮我看看 @internal/clitui/suggest.go" {
		t.Fatalf("句中 @ 补全应保留正文，got %q", got)
	}

	// 目录候选保留结尾斜杠，便于继续下钻
	sg = sg.Update("@internal/clitui/")
	if anyDir(sg.Matches()) == false {
		t.Fatalf("目录候选应保留结尾斜杠：%#v", sg.Matches())
	}

	// 邮箱、普通文本里的 @ 不触发
	for _, in := range []string{"a@b.com", "@@@", "没有 at 符号"} {
		if sg2 := sg.Update(in); sg2.Mode() == ModePath {
			t.Fatalf("%q 不应进入路径模式", in)
		}
	}
}

func mustSelect(t *testing.T, sg Suggest) SuggestItem {
	t.Helper()
	sel, ok := sg.Selected()
	if !ok {
		t.Fatal("应有高亮项")
	}
	return sel
}

func anyDir(items []SuggestItem) bool {
	for _, it := range items {
		if it.Kind == "目录" && strings.HasSuffix(it.Name, "/") {
			return true
		}
	}
	return false
}

func TestBestMatchThreshold(t *testing.T) {
	items := suggestFixture()
	it, score, ok := BestMatch(items, "/compac")
	if !ok || it.Name != "/compact" || score != MatchPrefix {
		t.Fatalf("got %+v score=%d ok=%v", it, score, ok)
	}
	if _, _, ok := BestMatch(items, "/zzz"); ok {
		t.Fatal("无关查询不应给出候选")
	}
}

// 自由文本参数不占屏：面板只留给“需要人做选择”的场合，其余 Enter 直接执行。
func TestFreeArgDoesNotOccupyScreen(t *testing.T) {
	sg := NewSuggest(80, suggestFixture(), DefaultTheme())
	sg = sg.Update("/compact 保留接口改动")
	if sg.Mode() != ModeArgFree {
		t.Fatalf("应为自由文本参数态，got %v", sg.Mode())
	}
	if sg.Visible() || sg.View() != "" || sg.Height() != 0 {
		t.Fatalf("自由文本参数不该占屏：visible=%v height=%d", sg.Visible(), sg.Height())
	}
	if !sg.ExecutesOnEnter() {
		t.Fatal("自由文本参数下回车应是执行")
	}
	if got := sg.Completion(SuggestItem{}); got != "/compact 保留接口改动" {
		t.Fatalf("参数不应被改写，got %q", got)
	}
}
