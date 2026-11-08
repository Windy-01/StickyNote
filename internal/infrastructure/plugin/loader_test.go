package plugin_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"stickynote/internal/domain/component"
	"stickynote/internal/infrastructure/plugin"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
}

const noteManifest = `{
  "id": "builtin.note",
  "name": "便签",
  "entry": "index.html",
  "defaultBounds": { "width": 420, "height": 360 },
  "minBounds": { "width": 260, "height": 200 }
}`

func TestLoadComponents(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components", "note", "manifest.json"), noteManifest)
	write(t, filepath.Join(root, "components", "note", "index.html"), "<!doctype html>")

	cat, problems := plugin.Load(root, true)
	if len(problems) != 0 {
		t.Fatalf("不应有问题: %v", problems)
	}
	if len(cat.Components) != 1 {
		t.Fatalf("组件数量不对: %d", len(cat.Components))
	}

	d := cat.Components[0]
	if !d.Builtin {
		t.Fatal("应标记为内置")
	}
	if d.Entry != "index.html" {
		t.Fatalf("清单字段没解析对: %+v", d)
	}
	if d.MinBounds.Width != 260 {
		t.Fatalf("minBounds 不对: %+v", d.MinBounds)
	}
	if d.SourceDir == "" {
		t.Fatal("SourceDir 应被填入绝对路径")
	}
}

// 旧版清单里的 "style" 字段现在没人认，但要能静默忽略——
// 老插件不改也能装，不该因为一个多余字段就起不来。
func TestLoadIgnoresLegacyStyleField(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components", "note", "manifest.json"), `{
	  "id": "vendor.old", "name": "老插件", "style": "builtin.style.topmost",
	  "entry": "index.html"
	}`)
	write(t, filepath.Join(root, "components", "note", "index.html"), "<!doctype html>")

	cat, problems := plugin.Load(root, false)
	if len(problems) != 0 {
		t.Fatalf("旧字段不应报错: %v", problems)
	}
	if len(cat.Components) != 1 {
		t.Fatalf("应正常加载，实际 %+v", cat.Components)
	}
}

// 坏包只记问题，不影响同目录下的好包。
func TestLoadSkipsBrokenPackages(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components", "note", "manifest.json"), noteManifest)
	write(t, filepath.Join(root, "components", "note", "index.html"), "<!doctype html>")

	// entry 指向不存在的文件
	write(t, filepath.Join(root, "components", "ghost", "manifest.json"), `{
	  "id": "ghost", "name": "幽灵", "entry": "missing.html"
	}`)
	// JSON 语法错误
	write(t, filepath.Join(root, "components", "broken", "manifest.json"), `{ "id": `)

	cat, problems := plugin.Load(root, false)
	if len(cat.Components) != 1 || cat.Components[0].ID != "builtin.note" {
		t.Fatalf("好包应留下，实际: %+v", cat.Components)
	}
	if len(problems) != 2 {
		t.Fatalf("应报告 2 个问题，实际 %d: %v", len(problems), problems)
	}
}

// 组件清单里出现窗口语义字段（想绕过组）必须显式拒绝，不能被 json 静默忽略。
func TestLoadRejectsSemanticFields(t *testing.T) {
	for _, key := range []string{"window", "type", "customLayer", "customFlags"} {
		t.Run(key, func(t *testing.T) {
			root := t.TempDir()
			write(t, filepath.Join(root, "components", "sneaky", "manifest.json"), `{
			  "id": "sneaky", "name": "偷偷改层级", "entry": "index.html",
			  "`+key+`": { "layer": "desktop" }
			}`)
			write(t, filepath.Join(root, "components", "sneaky", "index.html"), "<!doctype html>")

			cat, problems := plugin.Load(root, false)
			if len(cat.Components) != 0 {
				t.Fatalf("含 %s 字段的组件应被拒绝: %+v", key, cat.Components)
			}
			if len(problems) != 1 {
				t.Fatalf("应报告 1 个问题，实际 %d", len(problems))
			}
		})
	}
}

func TestMergeReportsConflicts(t *testing.T) {
	a := t.TempDir()
	write(t, filepath.Join(a, "components", "note", "manifest.json"), noteManifest)
	write(t, filepath.Join(a, "components", "note", "index.html"), "<!doctype html>")

	b := t.TempDir()
	write(t, filepath.Join(b, "components", "note", "manifest.json"), noteManifest)
	write(t, filepath.Join(b, "components", "note", "index.html"), "<!doctype html>")

	catA, _ := plugin.Load(a, true)
	catB, _ := plugin.Load(b, false)

	merged, conflicts := plugin.Merge(catA, catB)
	if len(merged.Components) != 1 {
		t.Fatalf("同 id 应只留一个，实际 %d", len(merged.Components))
	}
	if len(conflicts) != 1 {
		t.Fatalf("应报告 1 条冲突，实际 %d", len(conflicts))
	}
	if !merged.Components[0].Builtin {
		t.Fatal("先到者应保留（内置优先）")
	}
}

// 清单里的 variables 要能解析出来，并且类型/选项写错要在加载时就报问题，
// 而不是等用户打开管理界面才发现有个填不了的控件。
func TestLoadVariables(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components", "note", "manifest.json"), `{
	  "id": "note", "name": "便签", "entry": "index.html",
	  "variables": [
	    { "key": "title", "label": "标题", "type": "text", "default": "便签" },
	    { "key": "body", "type": "textarea", "rows": 6 },
	    { "key": "size", "type": "select", "default": "m",
	      "options": ["m", { "value": "l", "label": "大" }] }
	  ]
	}`)
	write(t, filepath.Join(root, "components", "note", "index.html"), "<!doctype html>")

	cat, problems := plugin.Load(root, false)
	if len(problems) != 0 {
		t.Fatalf("不应有问题: %v", problems)
	}
	if len(cat.Components) != 1 {
		t.Fatalf("组件数量不对: %d", len(cat.Components))
	}
	vars := cat.Components[0].Variables
	if len(vars) != 3 {
		t.Fatalf("应有 3 个变量，实际 %d", len(vars))
	}
	if vars[1].Type != component.VarTextarea || vars[1].Rows != 6 {
		t.Fatalf("长文本变量没解析对: %+v", vars[1])
	}
	if len(vars[2].Options) != 2 || vars[2].Options[1].Display() != "大" {
		t.Fatalf("选项没解析对: %+v", vars[2].Options)
	}
}

// 选项模式没给 options 是作者写错了，插件要被拒掉并给出原因。
func TestLoadRejectsBadVariable(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components", "bad", "manifest.json"), `{
	  "id": "bad", "name": "坏组件", "entry": "index.html",
	  "variables": [{ "key": "size", "type": "select" }]
	}`)
	write(t, filepath.Join(root, "components", "bad", "index.html"), "<!doctype html>")

	cat, problems := plugin.Load(root, false)
	if len(cat.Components) != 0 {
		t.Fatalf("声明有误的组件不该进目录，实际 %d 个", len(cat.Components))
	}
	if len(problems) == 0 {
		t.Fatal("应报出问题")
	}
}

// 列表式声明：作者可以写 `["第一条"]`（省事）或 `[{"text":"第一条"}]`
// （落盘形式），加载后统一归一成后者——组件拿到的永远是对象数组。
func TestLoadListVariable(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components", "list", "manifest.json"), `{
	  "id": "list", "name": "列表组件", "entry": "index.html",
	  "variables": [
	    { "key": "lines", "label": "条目", "type": "list",
	      "default": [ "第一条", { "text": "第二条" } ] },
	    { "key": "empty", "type": "list", "default": [] },
	    { "key": "todo", "type": "list", "item": "title", "default": [ { "title": "买菜" } ] }
	  ]
	}`)
	write(t, filepath.Join(root, "components", "list", "index.html"), "<!doctype html>")

	cat, problems := plugin.Load(root, false)
	if len(problems) != 0 {
		t.Fatalf("不应有问题: %v", problems)
	}
	vars := cat.Components[0].Variables
	if len(vars) != 3 {
		t.Fatalf("应有 3 个变量，实际 %d", len(vars))
	}
	if vars[0].Type != component.VarList {
		t.Fatalf("类型没解析对: %+v", vars[0])
	}
	var items []map[string]string
	if err := json.Unmarshal(vars[0].Default, &items); err != nil {
		t.Fatalf("默认值不是数组: %s / %v", vars[0].Default, err)
	}
	// 字符串写法也要归一成对象，否则"用户填的和默认值一样"比不出来。
	if len(items) != 2 || items[0]["text"] != "第一条" || items[1]["text"] != "第二条" {
		t.Fatalf("默认值内容不对: %v", items)
	}
	// 空数组也是合法默认值，不能因为"看着像空"就丢掉。
	if string(vars[1].Default) != "[]" {
		t.Fatalf("空数组默认值丢了: %s", vars[1].Default)
	}
	// item 声明成 title 时，落盘形式跟着换字段名。
	if string(vars[2].Default) != `[{"title":"买菜"}]` {
		t.Fatalf("item=title 时默认值应换字段名: %s", vars[2].Default)
	}
}

// 列表项只支持一个字段：多写的键会在用户一改就消失，所以加载时就报出来。
func TestLoadListVariableRejectsExtraFields(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components", "bad", "manifest.json"), `{
	  "id": "bad", "name": "错清单", "entry": "index.html",
	  "variables": [
	    { "key": "items", "type": "list", "default": [ { "text": "第一条", "color": "red" } ] }
	  ]
	}`)
	write(t, filepath.Join(root, "components", "bad", "index.html"), "<!doctype html>")

	cat, problems := plugin.Load(root, false)
	if len(problems) == 0 {
		t.Fatal("列表项多字段应报错")
	}
	if len(cat.Components) != 0 {
		t.Fatalf("声明有误的组件不该进目录，实际 %d 个", len(cat.Components))
	}
}

// 记录表声明：字段带类型，默认值里的数字要真是数字、布尔要真是布尔。
func TestLoadGroupVariable(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components", "cards", "manifest.json"), `{
	  "id": "cards", "name": "卡片组件", "entry": "index.html",
	  "variables": [
	    { "key": "cards", "label": "卡片", "type": "group",
	      "fields": [
	        { "key": "title", "label": "标题", "type": "text" },
	        { "key": "context", "type": "textarea", "rows": 4 },
	        { "key": "order", "type": "number", "default": 0 },
	        { "key": "pinned", "type": "bool", "default": false }
	      ],
	      "default": [ { "title": "第一张", "context": "内容", "order": 2 } ] }
	  ]
	}`)
	write(t, filepath.Join(root, "components", "cards", "index.html"), "<!doctype html>")

	cat, problems := plugin.Load(root, false)
	if len(problems) != 0 {
		t.Fatalf("不应有问题: %v", problems)
	}
	v := cat.Components[0].Variables[0]
	if v.Type != component.VarGroup {
		t.Fatalf("类型没解析对: %+v", v)
	}
	if len(v.Fields) != 4 || v.Fields[2].Type != component.VarNumber {
		t.Fatalf("字段没解析对: %+v", v.Fields)
	}
	if v.Fields[1].Rows != 4 {
		t.Fatalf("字段 rows 丢了: %+v", v.Fields[1])
	}
	// 默认值里 order 是数字 2，不是字符串 "2"。
	var recs []map[string]any
	if err := json.Unmarshal(v.Default, &recs); err != nil {
		t.Fatalf("默认值不是数组: %s / %v", v.Default, err)
	}
	if n, ok := recs[0]["order"].(float64); !ok || n != 2 {
		t.Fatalf("order 应是数字: %#v", recs[0]["order"])
	}
}

// 记录表里字段写错了要在加载时报出来，而不是等用户打开界面。
func TestLoadGroupVariableRejectsBadFields(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components", "bad", "manifest.json"), `{
	  "id": "bad", "name": "错清单", "entry": "index.html",
	  "variables": [
	    { "key": "cards", "type": "group",
	      "fields": [ { "key": "a", "type": "list" } ] }
	  ]
	}`)
	write(t, filepath.Join(root, "components", "bad", "index.html"), "<!doctype html>")

	cat, problems := plugin.Load(root, false)
	if len(problems) == 0 {
		t.Fatal("字段类型写成 list 应报错")
	}
	if len(cat.Components) != 0 {
		t.Fatalf("声明有误的组件不该进目录，实际 %d 个", len(cat.Components))
	}
}
