package component_test

import (
	"encoding/json"
	"strings"
	"testing"

	"stickynote/internal/domain/component"
)

// j 把一段 JSON 源码包成 RawMessage，省得每处都写 json.RawMessage(...)。
func j(s string) json.RawMessage { return json.RawMessage(s) }

// 选项的两种写法都要能解析：字符串省事，对象能让"存的值"和"显示的字"分开。
func TestVarOptionAcceptsStringAndObject(t *testing.T) {
	var v component.Variable
	raw := `{
	  "key": "size",
	  "type": "select",
	  "options": ["小", {"value": "l", "label": "大"}]
	}`
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(v.Options) != 2 {
		t.Fatalf("应有 2 个选项，实际 %d", len(v.Options))
	}
	// 字符串写法：value 和 label 都是它自己。
	if v.Options[0].Value != "小" || v.Options[0].Display() != "小" {
		t.Errorf("字符串选项解析错: %+v", v.Options[0])
	}
	if v.Options[1].Value != "l" || v.Options[1].Display() != "大" {
		t.Errorf("对象选项解析错: %+v", v.Options[1])
	}
}

func TestVarOptionRejectsBadShape(t *testing.T) {
	var v component.Variable
	err := json.Unmarshal([]byte(`{"key":"x","type":"select","options":[42]}`), &v)
	if err == nil {
		t.Fatal("数字选项应该报错")
	}
}

// 类型留空按单行文本框；标题留空用 key；rows 补 4 并封顶 20，json 类型补 6。
func TestVariableNormalize(t *testing.T) {
	v := component.Variable{Key: "title"}.Normalize()
	if v.Type != component.VarText {
		t.Errorf("类型应补成 text，实际 %q", v.Type)
	}
	if v.Label != "title" {
		t.Errorf("标题应退回 key，实际 %q", v.Label)
	}

	v = component.Variable{Key: "body", Type: component.VarTextarea, Rows: 99}.Normalize()
	if v.Rows != 20 {
		t.Errorf("rows 应封顶 20，实际 %d", v.Rows)
	}
	if v.Title() != "body" {
		t.Errorf("Title 应退回 key，实际 %q", v.Title())
	}

	// 列表类型：默认 6 行，item 补成 text，默认值归一成空数组。
	lv := component.Variable{Key: "lines", Type: component.VarList}.Normalize()
	if lv.Rows != 6 {
		t.Errorf("list 变量默认 6 行，实际 %d", lv.Rows)
	}
	if lv.Item != "text" {
		t.Errorf("list 变量的 item 应补成 text，实际 %q", lv.Item)
	}
	if string(lv.Default) != `[]` {
		t.Errorf("list 变量没写默认值时应补成空数组，实际 %s", lv.Default)
	}
}

func TestVariableValidate(t *testing.T) {
	cases := []struct {
		name string
		v    component.Variable
		want string // 期望错误里包含的片段，空表示应当通过
	}{
		{"缺 key", component.Variable{Type: component.VarText}, "缺少 key"},
		{"key 以数字开头", component.Variable{Key: "1abc"}, "不合法"},
		{"key 带空格", component.Variable{Key: "a b"}, "不合法"},
		{"点与短横线可以", component.Variable{Key: "a.b-c_1"}, ""},
		{"类型不认识", component.Variable{Key: "x", Type: "color"}, "不认识"},
		{"text 不该有 options", component.Variable{
			Key: "x", Type: component.VarText,
			Options: []component.VarOption{{Value: "a"}},
		}, "不该有 options"},
		{"select 必须有 options", component.Variable{Key: "x", Type: component.VarSelect}, "必须给 options"},
		{"选项重复", component.Variable{
			Key: "x", Type: component.VarSelect,
			Options: []component.VarOption{{Value: "a"}, {Value: "a"}},
		}, "重复"},
		{"选项 value 为空", component.Variable{
			Key: "x", Type: component.VarSelect,
			Options: []component.VarOption{{Value: ""}},
		}, "value 是空的"},
		{"默认值不在选项里", component.Variable{
			Key: "x", Type: component.VarSelect, Default: j(`"c"`),
			Options: []component.VarOption{{Value: "a"}, {Value: "b"}},
		}, "不在选项里"},
		{"默认值在选项里", component.Variable{
			Key: "x", Type: component.VarSelect, Default: j(`"a"`),
			Options: []component.VarOption{{Value: "a"}, {Value: "b"}},
		}, ""},
		// 文本类的默认值必须是字符串：写 `1` 这种在界面上会显示成 "1"，
		// 但组件拿到的是数字，两边对不上，所以加载时就拒掉。
		{"text 的默认值不是字符串", component.Variable{
			Key: "x", Type: component.VarText, Default: j(`123`),
		}, "默认值要写成字符串"},
		// 列表类型的默认值必须是数组；两种写法都认（字符串数组 / 对象数组）。
		{"list 的默认值是字符串数组", component.Variable{
			Key: "x", Type: component.VarList, Default: j(`["第一条","第二条"]`),
		}, ""},
		{"list 的默认值是对象数组", component.Variable{
			Key: "x", Type: component.VarList, Default: j(`[{"text":"第一条"}]`),
		}, ""},
		{"list 的默认值是空数组", component.Variable{
			Key: "x", Type: component.VarList, Default: j(`[]`),
		}, ""},
		{"list 的默认值不是数组", component.Variable{
			Key: "x", Type: component.VarList, Default: j(`"第一条"`),
		}, "默认值要写成数组"},
		{"list 的默认值缺 item 字段", component.Variable{
			Key: "x", Type: component.VarList, Item: "title", Default: j(`[{"text":"a"}]`),
		}, "缺少 \"title\" 字段"},
		{"list 不该有 options", component.Variable{
			Key: "x", Type: component.VarList,
			Options: []component.VarOption{{Value: "a"}},
		}, "不该有 options"},
		{"list 的 item 不合法", component.Variable{
			Key: "x", Type: component.VarList, Item: "有 空格",
		}, "item"},
		// item 是列表专属：别的类型写了它一定是笔误（想给 text 加字段名）。
		{"text 不该有 item", component.Variable{
			Key: "x", Type: component.VarText, Item: "text",
		}, "不该有 item"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.v.Normalize().Validate()
			if c.want == "" {
				if err != nil {
					t.Fatalf("不该报错，实际: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("应该报错（含 %q），但通过了", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("错误信息应包含 %q，实际: %v", c.want, err)
			}
		})
	}
}

func TestVariableCoerce(t *testing.T) {
	sel := component.Variable{
		Key: "size", Type: component.VarSelect,
		Options: []component.VarOption{{Value: "s"}, {Value: "m"}},
	}
	if got, err := sel.Coerce("m"); err != nil || string(got) != `"m"` {
		t.Errorf("合法选项应包成 JSON 字符串，得到 %q / %v", got, err)
	}
	if _, err := sel.Coerce("xl"); err == nil {
		t.Error("不在选项里的值应该报错")
	}

	// 单行框去首尾空白：那是误触，不是内容。
	txt := component.Variable{Key: "t", Type: component.VarText}
	if got, _ := txt.Coerce("  hi \n"); string(got) != `"hi"` {
		t.Errorf("单行框应去掉首尾空白，得到 %q", got)
	}

	// 长消息原样保留：换行和缩进可能就是内容本身。
	area := component.Variable{Key: "a", Type: component.VarTextarea}
	if got, _ := area.Coerce("  第一行\n  第二行  "); string(got) != `"  第一行\n  第二行  "` {
		t.Errorf("长消息应原样保留，得到 %q", got)
	}

	// 超长值直接拒：它会被下发到界面，也会写进磁盘。
	long := strings.Repeat("x", component.MaxVarValueLen+1)
	if _, err := txt.Coerce(long); err == nil {
		t.Error("超长值应该报错")
	}
}

// 列表类型：界面上是"一行一条"的文本框，存下来是 `[{"text":"…"}]`。
func TestVarListCoerce(t *testing.T) {
	v := component.Variable{Key: "lines", Type: component.VarList}

	// 每行一条，包成对象数组。空行与首尾空白都不算内容。
	got, err := v.Coerce("  第一条 \n\n第二条\r\n  \n第三条  ")
	if err != nil {
		t.Fatalf("合法输入不该报错: %v", err)
	}
	want := `[{"text":"第一条"},{"text":"第二条"},{"text":"第三条"}]`
	if string(got) != want {
		t.Fatalf("应包成对象数组，得到 %s，期望 %s", got, want)
	}

	// 字段名跟着 item 走：作者声明 title，组件就拿到 title。
	titled := component.Variable{Key: "todo", Type: component.VarList, Item: "title"}
	if got, _ := titled.Coerce("买菜"); string(got) != `[{"title":"买菜"}]` {
		t.Errorf("item=title 时应包成 title 字段，得到 %s", got)
	}

	// 全空 = 清空这一项：返回 nil，让调用方把 key 删掉、默认值重新生效。
	if got, err := v.Coerce("  \n\t\n  "); err != nil || got != nil {
		t.Errorf("全空应返回 nil，得到 %q / %v", got, err)
	}

	// 只有空白的一行也算空行。
	if got, _ := v.Coerce("   \n   \n"); got != nil {
		t.Errorf("只有空白行应返回 nil，得到 %s", got)
	}

	// 一行就是一行，不做任何 JSON 解析——用户在文本框里敲什么都是合法输入。
	if got, err := v.Coerce(`[{"text":"a"}]`); err != nil {
		t.Errorf("用户敲的方括号应当被当成普通文字: %v", err)
	} else if string(got) != `[{"text":"[{\"text\":\"a\"}]"}]` {
		t.Errorf("方括号应原样当成一行文字，得到 %s", got)
	}
}

// 摊平：对象数组与字符串数组都认，界面只管"有几行、每行是什么"。
func TestVarListLines(t *testing.T) {
	v := component.Variable{Key: "lines", Type: component.VarList}
	if got := v.ListLines(j(`[{"text":"a"},{"text":"b"}]`)); len(got) != 2 || got[1] != "b" {
		t.Errorf("对象数组应摊平成 a/b，得到 %v", got)
	}
	if got := v.ListLines(j(`["a","b"]`)); len(got) != 2 || got[0] != "a" {
		t.Errorf("字符串数组应摊平成 a/b，得到 %v", got)
	}
	if got := v.ListLines(j(`[]`)); len(got) != 0 {
		t.Errorf("空数组应摊平成 0 行，得到 %v", got)
	}
	// 默认值写短形式时，归一之后要和 Coerce 的产物完全一致，
	// 否则"用户填的和默认值一样"永远比不出来，默认值会被白白落盘。
	short := component.Variable{
		Key: "lines", Type: component.VarList, Default: j(`["第一条"]`),
	}.Normalize()
	if string(short.Default) != `[{"text":"第一条"}]` {
		t.Errorf("默认值应归一成对象数组，得到 %s", short.Default)
	}
	if !component.SameJSON(short.Default, j(`[{"text":"第一条"}]`)) {
		t.Error("归一后的默认值应与 Coerce 产物等价")
	}
}

// 没填过就用默认值——这是"默认值不落盘"能成立的前提。
func TestVariableValueFallsBackToDefault(t *testing.T) {
	v := component.Variable{Key: "title", Type: component.VarText, Default: j(`"便签"`)}
	if got := v.Value(nil); string(got) != `"便签"` {
		t.Errorf("空表应返回默认值，得到 %s", got)
	}
	if got := v.Value(map[string]json.RawMessage{"title": j(`""`)}); string(got) != `""` {
		t.Errorf("显式空串应原样返回，得到 %s", got)
	}
	if got := v.Value(map[string]json.RawMessage{"title": j(`"工作"`)}); string(got) != `"工作"` {
		t.Errorf("有值应用有值，得到 %s", got)
	}
	// 既没填也没默认值 → nil，界面据此显示空控件。
	if got := (component.Variable{Key: "x"}).Value(nil); got != nil {
		t.Errorf("无默认值时应返回 nil，得到 %s", got)
	}
}

// 等价判断要忽略空白与键顺序：用户把默认值原样抄一遍不该被当成"改过"。
func TestSameJSON(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{`"a"`, `"a"`, true},
		{`"a"`, `"b"`, false},
		{`{"a":1,"b":2}`, `{"b":2,"a":1}`, true},
		{`[1, 2]`, `[1,2]`, true},
		{`[1,2]`, `[2,1]`, false},
		{`null`, `null`, true},
	}
	for _, c := range cases {
		if got := component.SameJSON(j(c.a), j(c.b)); got != c.want {
			t.Errorf("SameJSON(%s, %s) = %v，期望 %v", c.a, c.b, got, c.want)
		}
	}
	if !component.SameJSON(nil, nil) {
		t.Error("两个空值应等价")
	}
	if component.SameJSON(nil, j(`null`)) {
		t.Error("空值与 null 不该等价——前者是没默认值，后者是默认 null")
	}
}

func TestValidateVariables(t *testing.T) {
	if err := component.ValidateVariables(nil); err != nil {
		t.Fatalf("空清单应该通过: %v", err)
	}

	dup := []component.Variable{
		{Key: "a", Type: component.VarText},
		{Key: "a", Type: component.VarText},
	}
	if err := component.ValidateVariables(dup); err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("重复 key 应报错，实际: %v", err)
	}

	tooMany := make([]component.Variable, component.MaxVariables+1)
	for i := range tooMany {
		tooMany[i] = component.Variable{Key: "v" + string(rune('a'+i%26)) + string(rune('0'+i/26)), Type: component.VarText}
	}
	if err := component.ValidateVariables(tooMany); err == nil || !strings.Contains(err.Error(), "太多") {
		t.Fatalf("超过上限应报错，实际: %v", err)
	}
}

// 清单里的 variables 写错了，加载定义时就该失败——不能等用户打开界面
// 才发现有一个填不了的控件。
func TestDefinitionValidateCoversVariables(t *testing.T) {
	d := component.Definition{
		ID:    "demo",
		Name:  "演示",
		Entry: "index.html",
		Variables: []component.Variable{
			{Key: "size", Type: component.VarSelect}, // 选项模式没给 options
		},
	}
	err := d.Normalize().Validate()
	if err == nil || !strings.Contains(err.Error(), "variables") {
		t.Fatalf("变量声明错误应让定义校验失败，实际: %v", err)
	}
}

// ---- 标量：数字与开关 ------------------------------------------------------

func TestVarNumberCoerce(t *testing.T) {
	v := component.Variable{Key: "size", Type: component.VarNumber}
	if got, err := v.Coerce(" 13 "); err != nil || string(got) != "13" {
		t.Errorf("数字应存成 JSON 数字，得到 %q / %v", got, err)
	}
	if got, err := v.Coerce("1.5"); err != nil || string(got) != "1.5" {
		t.Errorf("小数也要能填，得到 %q / %v", got, err)
	}
	if got, err := v.Coerce("-0.25"); err != nil || string(got) != "-0.25" {
		t.Errorf("负数也要能填，得到 %q / %v", got, err)
	}
	// 空 = 清空这一项，回到默认值。
	if got, err := v.Coerce("   "); err != nil || got != nil {
		t.Errorf("空输入应返回 nil，得到 %q / %v", got, err)
	}
	// 非数字当场拒掉，不能存成字符串。
	if _, err := v.Coerce("大"); err == nil {
		t.Error("非数字应该报错")
	}
	if _, err := v.Coerce("13px"); err == nil {
		t.Error("带单位的应该报错")
	}
}

func TestVarBoolCoerce(t *testing.T) {
	v := component.Variable{Key: "on", Type: component.VarBool}
	for _, c := range []struct{ in, want string }{
		{"true", "true"}, {"1", "true"}, {"false", "false"}, {"0", "false"},
	} {
		if got, err := v.Coerce(c.in); err != nil || string(got) != c.want {
			t.Errorf("Coerce(%q) = %q / %v，期望 %q", c.in, got, err, c.want)
		}
	}
	if got, err := v.Coerce(""); err != nil || got != nil {
		t.Errorf("空输入应返回 nil，得到 %q / %v", got, err)
	}
	if _, err := v.Coerce("yes"); err == nil {
		t.Error("yes 不该被当成布尔")
	}
}

// ---- 记录表：一条记录 = 一组纵向字段 ---------------------------------------

// groupDef 是测试里反复用到的那张表：标题 + 内容 + 序号 + 置顶。
func groupDef() component.Variable {
	return component.Variable{
		Key: "cards", Type: component.VarGroup,
		Fields: []component.VarField{
			{Key: "title", Type: component.VarText},
			{Key: "context", Type: component.VarTextarea},
			{Key: "order", Type: component.VarNumber},
			{Key: "pinned", Type: component.VarBool},
		},
	}.Normalize()
}

func TestVarGroupCoerce(t *testing.T) {
	v := groupDef()

	// 界面把每张卡片拼成对象再序列化成一整个数组交上来。
	// 空记录、空字段都要被丢掉；字符串字段去首尾空白，长文本原样保留。
	got, err := v.Coerce(`[
	  {"title":" 第一张 ","context":"内容\n第二行","order":3,"pinned":true},
	  {},
	  {"title":"","context":"   "}
	]`)
	if err != nil {
		t.Fatalf("合法记录不该报错: %v", err)
	}
	want := `[{"context":"内容\n第二行","order":3,"pinned":true,"title":"第一张"}]`
	if !component.SameJSON(got, j(want)) {
		t.Fatalf("规整结果不对:\n得到 %s\n期望 %s", got, want)
	}

	// 布尔 false 是一个明确的取值，不能被当成"没填"丢掉。
	if got, _ := v.Coerce(`[{"title":"a","pinned":false}]`); !component.SameJSON(got,
		j(`[{"pinned":false,"title":"a"}]`)) {
		t.Errorf("false 应保留，得到 %s", got)
	}

	// 一条都没有 = 清空这一项。
	if got, err := v.Coerce("[]"); err != nil || got != nil {
		t.Errorf("空数组应返回 nil，得到 %q / %v", got, err)
	}
	if got, err := v.Coerce("  "); err != nil || got != nil {
		t.Errorf("空文本应返回 nil，得到 %q / %v", got, err)
	}

	// 未声明的字段当场拒掉：界面上根本没有它的控件，留着只会无声消失。
	if _, err := v.Coerce(`[{"nope":1}]`); err == nil || !strings.Contains(err.Error(), "未声明") {
		t.Errorf("未声明字段应报错，实际: %v", err)
	}
	// 类型对不上也要拒：数字字段收到字符串。
	if _, err := v.Coerce(`[{"order":"3"}]`); err == nil || !strings.Contains(err.Error(), "数字") {
		t.Errorf("数字字段收到字符串应报错，实际: %v", err)
	}
	if _, err := v.Coerce(`[{"pinned":"true"}]`); err == nil {
		t.Error("开关字段收到字符串应报错")
	}
	if _, err := v.Coerce(`[{"title":1}]`); err == nil {
		t.Error("文本字段收到数字应报错")
	}
	// select 字段只能在选项里取值。
	sel := component.Variable{
		Key: "rows", Type: component.VarGroup,
		Fields: []component.VarField{
			{Key: "kind", Type: component.VarSelect, Options: []component.VarOption{{Value: "a"}, {Value: "b"}}},
		},
	}.Normalize()
	if _, err := sel.Coerce(`[{"kind":"c"}]`); err == nil || !strings.Contains(err.Error(), "不在可选项里") {
		t.Errorf("非法选项应报错，实际: %v", err)
	}
	if got, err := sel.Coerce(`[{"kind":"b"}]`); err != nil || !component.SameJSON(got, j(`[{"kind":"b"}]`)) {
		t.Errorf("合法选项应通过，得到 %s / %v", got, err)
	}
}

// 默认值归一：只留声明过的字段、按类型排好，否则"用户填的和默认值一样"
// 永远比不出来（默认值会被白白落盘一次）。
func TestVarGroupNormalizeDefault(t *testing.T) {
	v := component.Variable{
		Key: "cards", Type: component.VarGroup,
		Fields: []component.VarField{
			{Key: "title", Type: component.VarText},
			{Key: "order", Type: component.VarNumber},
		},
		Default: j(`[{"title":" 第一张 ","order":2}]`),
	}.Normalize()

	if !component.SameJSON(v.Default, j(`[{"order":2,"title":"第一张"}]`)) {
		t.Fatalf("默认值应归一成落盘形式，得到 %s", v.Default)
	}
	// 归一后的默认值与 Coerce 产物等价——这条成立，"默认值不落盘"才成立。
	got, err := v.Coerce(`[{"title":"第一张","order":2}]`)
	if err != nil || !component.SameJSON(got, v.Default) {
		t.Fatalf("Coerce 产物应与默认值等价: %s vs %s (%v)", got, v.Default, err)
	}
	// 没写默认值时补成空数组，界面据此画一张空表。
	if got := (component.Variable{Key: "x", Type: component.VarGroup,
		Fields: []component.VarField{{Key: "a"}}}).Normalize(); string(got.Default) != "[]" {
		t.Errorf("没写默认值应补成 []，得到 %s", got.Default)
	}
	// 字段的 label / rows 也要补齐。
	nf := (component.Variable{Key: "x", Type: component.VarGroup, Fields: []component.VarField{
		{Key: "a"}, {Key: "b", Type: component.VarTextarea},
	}}).Normalize()
	if nf.Fields[0].Label != "a" || nf.Fields[0].Type != component.VarText {
		t.Errorf("字段类型/标题没补齐: %+v", nf.Fields[0])
	}
	if nf.Fields[1].Rows != 3 {
		t.Errorf("textarea 字段默认 3 行，实际 %d", nf.Fields[1].Rows)
	}
}

func TestVarGroupValidate(t *testing.T) {
	cases := []struct {
		name string
		v    component.Variable
		want string
	}{
		{"没有 fields", component.Variable{Key: "x", Type: component.VarGroup}, "必须给 fields"},
		{"字段重名", component.Variable{Key: "x", Type: component.VarGroup,
			Fields: []component.VarField{{Key: "a"}, {Key: "a"}}}, "重复"},
		{"字段名不合法", component.Variable{Key: "x", Type: component.VarGroup,
			Fields: []component.VarField{{Key: "有 空格"}}}, "不合法"},
		{"字段不能是 group", component.Variable{Key: "x", Type: component.VarGroup,
			Fields: []component.VarField{{Key: "a", Type: component.VarGroup}}}, "不能再套列表"},
		{"字段不能是 list", component.Variable{Key: "x", Type: component.VarGroup,
			Fields: []component.VarField{{Key: "a", Type: component.VarList}}}, "不能再套列表"},
		{"select 字段没 options", component.Variable{Key: "x", Type: component.VarGroup,
			Fields: []component.VarField{{Key: "a", Type: component.VarSelect}}}, "必须给 options"},
		{"默认值不是数组", component.Variable{Key: "x", Type: component.VarGroup,
			Fields: []component.VarField{{Key: "a"}}, Default: j(`{"a":"1"}`)}, "默认值要写成数组"},
		{"默认值有未声明字段", component.Variable{Key: "x", Type: component.VarGroup,
			Fields: []component.VarField{{Key: "a"}}, Default: j(`[{"b":"1"}]`)}, "未声明"},
		{"默认值类型不对", component.Variable{Key: "x", Type: component.VarGroup,
			Fields: []component.VarField{{Key: "n", Type: component.VarNumber}}, Default: j(`[{"n":"1"}]`)}, "数字"},
		{"字段太多", func() component.Variable {
			fs := make([]component.VarField, component.MaxVarFields+1)
			for i := range fs {
				fs[i] = component.VarField{Key: "f" + string(rune('a'+i))}
			}
			return component.Variable{Key: "x", Type: component.VarGroup, Fields: fs}
		}(), "太多"},
		// 正常的一张表：三个字段、默认值一条。
		{"合法的表", component.Variable{Key: "x", Type: component.VarGroup,
			Fields: []component.VarField{
				{Key: "title"}, {Key: "context", Type: component.VarTextarea},
				{Key: "order", Type: component.VarNumber},
			},
			Default: j(`[{"title":"第一张","context":"内容","order":1}]`)}, ""},
		// fields 是 group 专属，别的类型写了它是笔误。
		{"text 不该有 fields", component.Variable{Key: "x", Type: component.VarText,
			Fields: []component.VarField{{Key: "a"}}}, "不该有 fields"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.v.Normalize().Validate()
			if c.want == "" {
				if err != nil {
					t.Fatalf("不该报错，实际: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("应该报错（含 %q），但通过了", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("错误信息应包含 %q，实际: %v", c.want, err)
			}
		})
	}
}
