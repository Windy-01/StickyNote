package component

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

// VariableType 决定一个值在界面上用什么控件填，也决定它落盘成什么 JSON 类型。
//
// 分两类：
//
//   - **标量**：`text` / `textarea` / `select` 是字符串，`number` 是数字，
//     `bool` 是布尔。它们覆盖了 JSON 里最常用的标量——"数字要存成数字，
//     不要变成 \"3\"" 是这一组类型存在的唯一理由。
//   - **集合**：`list` 是一行一条的文本框，`group` 是一条一块的表单。
//     两者都落盘成数组，区别只在"一条里只有一个字段"还是"一条里有几个字段"。
//
// 没有任何一种类型要求用户写 JSON。`[{` 到 `[{"a":1}]` 之间必然有一瞬间是
// 非法 JSON，边打边报错没法用；结构化是内核这侧的事。
type VariableType string

const (
	// VarText 单行文本框。适合短参数：标题、路径、单个词。
	VarText VariableType = "text"
	// VarTextarea 长消息文本框。适合正文、模板、多行清单。
	VarTextarea VariableType = "textarea"
	// VarSelect 选项模式：只能从作者给的列表里挑一个。
	VarSelect VariableType = "select"
	// VarNumber 数字框：落盘是 JSON 数字，不是字符串。
	VarNumber VariableType = "number"
	// VarBool 开关：落盘是 JSON 的 true / false。
	VarBool VariableType = "bool"
	// VarList 列表：界面上仍是多行文本框，**一行一条**。
	//
	// 存下来的是 `[{"text":"第一条"},{"text":"第二条"}]`——组件拿到的
	// 是真正的数组，不用自己按换行拆字符串，也不必各自约定转义规则。
	// 字段名由 Item 决定，默认 `text`。
	VarList VariableType = "list"
	// VarGroup 记录表：一条记录是一块**纵向排列**的字段，可以增删。
	//
	// 一条里能有几个字段由 Fields 声明，落盘是
	// `[{"title":"…","context":"…"}]`——和 `list` 同构，只是每条更宽。
	// 界面上每条是一张卡片，字段自上而下排，底部一个「+ 添加一条」。
	VarGroup VariableType = "group"
)

// MaxVariables 是一个组件最多能声明多少个变量。
//
// 界面上要一条条排下来，太多就没法用了；这个数也顺带挡住畸形清单。
const MaxVariables = 32

// MaxVarValueLen 是单个变量值的上限（字节）。
//
// 变量值会随每次状态推送下发到管理界面，值太大整条推送都会变慢；
// 而且它是用户手填的，不设上限等于给了一个可以写爆磁盘的口子。
const MaxVarValueLen = 64 << 10

// MaxVarOptions 是一个 select 变量最多多少个选项。
const MaxVarOptions = 64

// MaxVarFields 是一条记录最多能声明多少个字段。
//
// 一条记录在界面上是一张卡片，字段一个个竖着排；太多就没法用了，
// 而且用户要滚很久才能看到「+ 添加一条」。这个数也顺带挡住畸形清单。
const MaxVarFields = 12

// varKeyPattern 限制变量名的字符集。
//
// 它会进 JSON 的键，也会被组件当作 `vars[key]` 取值，限制成标识符风格最省心：
// 带空格或引号的 key 在两边都要转义，迟早有一边漏掉。
var varKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)

// VarOption 是 select 的一个可选项。
//
// 清单里可以写两种形式：
//
//	"options": ["小", "中", "大"]
//	"options": [{"value": "s", "label": "小"}]
//
// 前者省事；后者能让"存进磁盘的值"和"显示给用户的字"分开——
// 存 "s"、显示「小」，组件代码就不用去认中文。
type VarOption struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
}

// UnmarshalJSON 同时接受字符串和对象两种写法。
func (o *VarOption) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		o.Value, o.Label = s, s
		return nil
	}
	var p struct {
		Value string `json:"value"`
		Label string `json:"label"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return fmt.Errorf(`选项只能写成字符串或 {"value":…,"label":…}: %w`, err)
	}
	o.Value = p.Value
	o.Label = p.Label
	if o.Label == "" {
		o.Label = p.Value
	}
	return nil
}

// Display 是界面上显示的文字。
func (o VarOption) Display() string {
	if o.Label != "" {
		return o.Label
	}
	return o.Value
}

// VarField 是 group 类型里的一条属性声明。
//
// 和 Variable 长得很像，但刻意是两套类型：字段不能是 `group` 也不能是
// `list`——列表里再套列表，界面上就没法用了，组件那边也会拿到一个
// 嵌套深度不定的值。真需要嵌套结构时，那已经不是一个"用户填的配置项"，
// 应该做成组件自己的参数（或者干脆两个变量）。
type VarField struct {
	// Key 是字段名，也就是落盘对象里的键。
	Key string `json:"key"`
	// Label 是界面上那行小标题，留空用 Key。
	Label string `json:"label,omitempty"`
	// Type 只能是标量：text / textarea / select / number / bool。
	Type VariableType `json:"type,omitempty"`
	// Default 是新建一条记录时预填的值（**不进落盘**，只是界面初值）。
	Default json.RawMessage `json:"default,omitempty"`
	// Placeholder 是输入框里的灰字提示。
	Placeholder string `json:"placeholder,omitempty"`
	// Options 只在 Type==select 时使用。
	Options []VarOption `json:"options,omitempty"`
	// Rows 是 textarea 的高度（行数），留空按 3 行。
	Rows int `json:"rows,omitempty"`
}

// Title 返回字段在界面上的小标题。
func (f VarField) Title() string {
	if f.Label != "" {
		return f.Label
	}
	return f.Key
}

// Normalize 补齐字段的缺省值。
func (f VarField) Normalize() VarField {
	if f.Type == "" {
		f.Type = VarText
	}
	if f.Label == "" {
		f.Label = f.Key
	}
	if f.Rows <= 0 {
		if f.Type == VarTextarea {
			f.Rows = 3
		} else {
			f.Rows = 0
		}
	}
	if f.Rows > 20 {
		f.Rows = 20
	}
	for i := range f.Options {
		if f.Options[i].Label == "" {
			f.Options[i].Label = f.Options[i].Value
		}
	}
	return f
}

// Validate 检查一条字段声明。
func (f VarField) Validate() error {
	if f.Key == "" {
		return fmt.Errorf("字段缺少 key")
	}
	if !varKeyPattern.MatchString(f.Key) {
		return fmt.Errorf("字段名 %q 不合法：只能用字母、数字、下划线、点、短横线，且不能以数字开头", f.Key)
	}
	switch f.Type {
	case VarText, VarTextarea, VarNumber, VarBool:
		if len(f.Options) > 0 {
			return fmt.Errorf("字段 %s 是 %s 类型，不该有 options", f.Key, f.Type)
		}
	case VarSelect:
		if len(f.Options) == 0 {
			return fmt.Errorf("字段 %s 是选项模式，必须给 options", f.Key)
		}
		seen := make(map[string]bool, len(f.Options))
		for _, o := range f.Options {
			if o.Value == "" {
				return fmt.Errorf("字段 %s 有一个选项的 value 是空的", f.Key)
			}
			if seen[o.Value] {
				return fmt.Errorf("字段 %s 的选项 %q 重复了", f.Key, o.Value)
			}
			seen[o.Value] = true
		}
		var s string
		if len(f.Default) > 0 && json.Unmarshal(f.Default, &s) == nil && s != "" && !seen[s] {
			return fmt.Errorf("字段 %s 的默认值 %q 不在选项里", f.Key, s)
		}
	case VarList, VarGroup:
		return fmt.Errorf("字段 %s 的类型不能是 %s：列表里不能再套列表", f.Key, f.Type)
	default:
		return fmt.Errorf("字段 %s 的类型 %q 不认识（可选：text / textarea / select / number / bool）", f.Key, f.Type)
	}
	return nil
}

// Variable 是组件清单里声明的一个变量。
//
// 变量是**用户填的配置**，不是组件自己写的运行时数据：作者只声明
// "我需要哪些变量、它们叫什么、怎么填"，值由用户在管理界面里按实例填。
// 所以它和 storage 是两套东西，存在两个地方（见 InstanceVars）。
type Variable struct {
	// Key 是变量的标识，组件代码用它取值。安装后不可改（改了等于换一个变量）。
	Key string `json:"key"`
	// Label 是界面上的标题。留空时用 Key。
	Label string `json:"label,omitempty"`
	// Description 是标题下面那行说明，写清楚"填什么、填了有什么用"。
	Description string `json:"description,omitempty"`
	// Type 决定用哪种控件，留空按单行文本框。
	Type VariableType `json:"type"`
	// Default 是没填过时的兜底值。**类型跟着 Type 走**：
	// 文本类是一个 JSON 字符串，`number` 是数字，`bool` 是布尔，
	// `list` / `group` 是数组（`["第一条"]` 与 `[{"text":"第一条"}]`
	// 两种写法都认，加载时会归一成后者）。
	//
	// **它不落盘**：作者改了清单里的默认值，用户没动过的实例会自动跟着变；
	// 落盘之后"用户没改"和"用户改成了和默认值一样"就分不出来了。
	Default json.RawMessage `json:"default,omitempty"`
	// Placeholder 是输入框里的灰字提示。
	Placeholder string `json:"placeholder,omitempty"`
	// Item 只在 Type==list 时使用：每一行落到对象里的字段名，留空按 text。
	//
	// 留这个口子是因为"列表项"不一定都叫 text——待办清单里那一行叫
	// `title` 更自然，组件代码读 `o.title` 也更清楚。
	Item string `json:"item,omitempty"`
	// Fields 只在 Type==group 时使用：一条记录由哪几个字段组成。
	Fields []VarField `json:"fields,omitempty"`
	// Options 只在 Type==select 时使用。
	Options []VarOption `json:"options,omitempty"`
	// Required 只影响界面上的标记（打一个"必填"角标），**不阻止保存**。
	//
	// 挡着不让存会让"填一半切走"变得很难受，而组件本来就得处理
	// "这个变量还是空的"——那是它的业务，不是宿主的。
	Required bool `json:"required,omitempty"`
	// Rows 是 textarea / list 编辑框的高度（行数），留空按 4 行（list 按 6 行）。
	Rows int `json:"rows,omitempty"`
}

// Title 返回界面上的标题。
func (v Variable) Title() string {
	if v.Label != "" {
		return v.Label
	}
	return v.Key
}

// IsList 报告这个变量是不是"一行一条"的列表类型。
func (v Variable) IsList() bool { return v.Type == VarList }

// IsGroup 报告这个变量是不是"一条一块"的记录表类型。
func (v Variable) IsGroup() bool { return v.Type == VarGroup }

// ItemName 返回列表项里存文本的字段名，默认 text。
func (v Variable) ItemName() string {
	if v.Item != "" {
		return v.Item
	}
	return "text"
}

// Normalize 补齐缺省值。
func (v Variable) Normalize() Variable {
	if v.Type == "" {
		v.Type = VarText
	}
	if v.Label == "" {
		v.Label = v.Key
	}
	if v.Rows <= 0 {
		if v.IsList() {
			v.Rows = 6
		} else {
			v.Rows = 4
		}
	}
	if v.Rows > 20 {
		v.Rows = 20
	}
	for i := range v.Options {
		if v.Options[i].Label == "" {
			v.Options[i].Label = v.Options[i].Value
		}
	}
	for i := range v.Fields {
		v.Fields[i] = v.Fields[i].Normalize()
	}
	if v.IsList() {
		v.Item = v.ItemName()
		// 把默认值归一成落盘形式。作者写 `["第一条"]` 或
		// `[{"text":"第一条"}]` 都行，但**不归一的话"用户填的和默认值一样"
		// 就比不出来**（见 SameJSON），默认值会被白白落盘一次。
		//
		// 解不出来就原样留着，让 Validate 去报错——这里悄悄改成 `[]`
		// 会把作者的笔误藏起来。
		if len(v.Default) == 0 {
			v.Default = json.RawMessage(`[]`)
		} else if lines, ok := v.listLinesStrict(v.Default); ok {
			v.Default = listValue(lines, v.Item)
		}
	}
	if v.IsGroup() {
		// 同理：`[{title:"a"}]` 归一成"只留声明过的字段、按类型排好"的形式。
		if len(v.Default) == 0 {
			v.Default = json.RawMessage(`[]`)
		} else if recs, ok := v.recordsStrict(v.Default); ok {
			v.Default = recordsValue(recs)
		}
	}
	return v
}

// DefaultText 把默认值解成文本（文本类变量用；解不出来就返回空串）。
func (v Variable) DefaultText() string {
	if v.IsList() {
		return strings.Join(v.ListLines(v.Default), "\n")
	}
	var s string
	if len(v.Default) > 0 && json.Unmarshal(v.Default, &s) == nil {
		return s
	}
	return ""
}

// hasField 报告有没有声明过这个字段。
func (v Variable) hasField(key string) bool {
	for _, f := range v.Fields {
		if f.Key == key {
			return true
		}
	}
	return false
}

// hasOption 报告 select 字段里有没有这个取值。
func (f VarField) hasOption(value string) bool {
	for _, o := range f.Options {
		if o.Value == value {
			return true
		}
	}
	return false
}

// coerceValue 把一个字段值规整成该字段类型对应的 JSON 值。
//
// 返回 nil 表示"这一项没填"，调用方应当把键丢掉——空串、`null`、
// 全是空白的长文本都算没填。
func (f VarField) coerceValue(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	switch f.Type {
	case VarNumber:
		n, ok := parseNumber(raw)
		if !ok {
			return nil, fmt.Errorf("要写成数字")
		}
		return json.RawMessage(n.String()), nil
	case VarBool:
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, fmt.Errorf("要写成 true 或 false")
		}
		return json.RawMessage(strconv.FormatBool(b)), nil
	default:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("要写成字符串")
		}
		if f.Type == VarText || f.Type == VarSelect {
			s = strings.TrimSpace(s)
		}
		if strings.TrimSpace(s) == "" {
			return nil, nil
		}
		if f.Type == VarSelect && !f.hasOption(s) {
			return nil, fmt.Errorf("的取值 %q 不在可选项里", s)
		}
		return marshalString(s), nil
	}
}

// coerceRecord 把一条记录规整成"只含声明过的字段、值类型对得上"的形式。
//
// 返回 nil 表示这条是空的（一个字段都没填），调用方应当把它丢掉：
// 用户点了「+ 添加一条」却没填，不该在磁盘上留一条空记录。
func (v Variable) coerceRecord(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("不是对象")
	}
	// 先查有没有未声明的字段：多写的键在界面上根本没有控件，
	// 留着只会在用户一改就消失，不如当场指出来。
	for k := range obj {
		if !v.hasField(k) {
			return nil, fmt.Errorf("有未声明的字段 %q", k)
		}
	}
	out := make(map[string]json.RawMessage, len(v.Fields))
	for _, f := range v.Fields {
		val, ok := obj[f.Key]
		if !ok {
			continue
		}
		c, err := f.coerceValue(val)
		if err != nil {
			return nil, fmt.Errorf("字段 %s %w", f.Key, err)
		}
		if c == nil {
			continue
		}
		out[f.Key] = c
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// recordsStrict 把一段记录值规整成落盘形式，失败就返回 false。
//
// 用它来决定"要不要把默认值归一"。归一不了就原样留着让 Validate 报错，
// 而不是把错的那条悄悄删掉。
func (v Variable) recordsStrict(raw json.RawMessage) ([]map[string]json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, true
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, false
	}
	out := make([]map[string]json.RawMessage, 0, len(arr))
	for _, it := range arr {
		rec, err := v.coerceRecord(it)
		if err != nil {
			return nil, false
		}
		if rec == nil {
			continue
		}
		out = append(out, rec)
	}
	return out, true
}

// recordsValue 把若干条记录编码成落盘形式。
func recordsValue(recs []map[string]json.RawMessage) json.RawMessage {
	if recs == nil {
		recs = []map[string]json.RawMessage{}
	}
	b, err := json.Marshal(recs)
	if err != nil {
		return json.RawMessage(`[]`)
	}
	return b
}

// ListLines 把一段列表值摊平成若干行文本。
//
// 两种写法都接受：`["第一行"]`（作者写清单时省事）与
// `[{"text":"第一行"}]`（落盘形式）。摊平之后两者没有区别，
// 界面只关心"有几行、每行写的是什么"。
func (v Variable) ListLines(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	field := v.ItemName()
	out := make([]string, 0, len(items))
	for _, it := range items {
		var s string
		if err := json.Unmarshal(it, &s); err == nil {
			out = append(out, s)
			continue
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(it, &obj); err != nil {
			continue
		}
		if err := json.Unmarshal(obj[field], &s); err != nil {
			continue
		}
		out = append(out, s)
	}
	return out
}

// listLinesStrict 摊平列表值，但要求每一项都"干净"：要么是字符串，
// 要么是**只含 item 字段**的对象。
//
// 用它来决定"要不要把默认值归一成落盘形式"。有一项不干净就返回 false，
// 让 Validate 去报错——归一的同时把多余字段丢掉，作者永远不知道自己
// 写错了（`{"text":"a","color":"red"}` 里的 color 会无声消失）。
func (v Variable) listLinesStrict(raw json.RawMessage) ([]string, bool) {
	if len(raw) == 0 {
		return nil, true
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, false
	}
	field := v.ItemName()
	out := make([]string, 0, len(arr))
	for _, it := range arr {
		var s string
		if json.Unmarshal(it, &s) == nil {
			out = append(out, s)
			continue
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(it, &obj); err != nil {
			return nil, false
		}
		if len(obj) != 1 {
			return nil, false
		}
		if err := json.Unmarshal(obj[field], &s); err != nil {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// Validate 检查一个变量声明能不能用。
//
// 这些错误都发生在加载清单时：作者写错了就装不上，而不是等到用户
// 打开管理界面才看到一个填不了的控件。
func (v Variable) Validate() error {
	if v.Key == "" {
		return fmt.Errorf("变量缺少 key")
	}
	if !varKeyPattern.MatchString(v.Key) {
		return fmt.Errorf("变量名 %q 不合法：只能用字母、数字、下划线、点、短横线，且不能以数字开头", v.Key)
	}
	if len(v.Default) > MaxVarValueLen {
		return fmt.Errorf("变量 %s 的默认值太长（%d 字节，上限 %d）", v.Key, len(v.Default), MaxVarValueLen)
	}
	if !v.IsList() && v.Item != "" {
		return fmt.Errorf("变量 %s 是 %s 类型，不该有 item", v.Key, v.Type)
	}
	if !v.IsGroup() && len(v.Fields) > 0 {
		return fmt.Errorf("变量 %s 是 %s 类型，不该有 fields", v.Key, v.Type)
	}
	// 标量类的默认值必须写成对应类型：字符串 / 数字 / 布尔。
	// `list` 与 `group` 的默认值是数组，各自单独校验。
	if !v.IsList() && !v.IsGroup() && len(v.Default) > 0 {
		switch v.Type {
		case VarNumber:
			if _, ok := parseNumber(v.Default); !ok {
				return fmt.Errorf("变量 %s 是数字类型，默认值要写成数字（不要加引号）", v.Key)
			}
		case VarBool:
			var b bool
			if err := json.Unmarshal(v.Default, &b); err != nil {
				return fmt.Errorf("变量 %s 是开关类型，默认值要写成 true 或 false", v.Key)
			}
		default:
			var s string
			if err := json.Unmarshal(v.Default, &s); err != nil {
				return fmt.Errorf("变量 %s 是 %s 类型，默认值要写成字符串", v.Key, v.Type)
			}
		}
	}

	switch v.Type {
	case VarText, VarTextarea, VarNumber, VarBool:
		if len(v.Options) > 0 {
			return fmt.Errorf("变量 %s 是 %s 类型，不该有 options", v.Key, v.Type)
		}
	case VarGroup:
		if len(v.Options) > 0 {
			return fmt.Errorf("变量 %s 是 %s 类型，不该有 options", v.Key, v.Type)
		}
		if len(v.Fields) == 0 {
			return fmt.Errorf("变量 %s 是记录表类型，必须给 fields", v.Key)
		}
		if len(v.Fields) > MaxVarFields {
			return fmt.Errorf("变量 %s 的字段太多（%d 个），最多 %d 个", v.Key, len(v.Fields), MaxVarFields)
		}
		seenField := make(map[string]bool, len(v.Fields))
		for _, f := range v.Fields {
			if seenField[f.Key] {
				return fmt.Errorf("变量 %s 的字段名 %q 重复了", v.Key, f.Key)
			}
			seenField[f.Key] = true
			if err := f.Validate(); err != nil {
				return fmt.Errorf("变量 %s：%w", v.Key, err)
			}
		}
		// 默认值必须是数组，每一项都是只含声明过字段的对象，且值类型对得上。
		if len(v.Default) > 0 {
			var arr []json.RawMessage
			if err := json.Unmarshal(v.Default, &arr); err != nil {
				return fmt.Errorf(`变量 %s 是记录表类型，默认值要写成数组，例如 [{"title":"第一条"}]`, v.Key)
			}
			for i, it := range arr {
				if _, err := v.coerceRecord(it); err != nil {
					return fmt.Errorf("变量 %s 的默认值第 %d 条：%w", v.Key, i+1, err)
				}
			}
		}
	case VarList:
		if len(v.Options) > 0 {
			return fmt.Errorf("变量 %s 是 %s 类型，不该有 options", v.Key, v.Type)
		}
		if !varKeyPattern.MatchString(v.ItemName()) {
			return fmt.Errorf("变量 %s 的 item %q 不合法：只能用字母、数字、下划线、点、短横线，且不能以数字开头", v.Key, v.ItemName())
		}
		// 默认值必须是数组，且每一项要么是字符串，要么是带 item 字段的对象。
		if len(v.Default) > 0 {
			var arr []json.RawMessage
			if err := json.Unmarshal(v.Default, &arr); err != nil {
				return fmt.Errorf(`变量 %s 是列表类型，默认值要写成数组，例如 ["第一条","第二条"]`, v.Key)
			}
			field := v.ItemName()
			for i, it := range arr {
				var s string
				if json.Unmarshal(it, &s) == nil {
					continue
				}
				var obj map[string]json.RawMessage
				if err := json.Unmarshal(it, &obj); err != nil {
					return fmt.Errorf("变量 %s 的默认值第 %d 项既不是字符串也不是对象", v.Key, i+1)
				}
				if err := json.Unmarshal(obj[field], &s); err != nil {
					return fmt.Errorf("变量 %s 的默认值第 %d 项缺少 %q 字段", v.Key, i+1, field)
				}
				// 列表项只有一个字段：用户在界面上填的是"一行文字"，
				// 编辑之后也只能产出一个字段。默认值里多写的键会在用户
				// 一改就消失，与其让它悄悄丢，不如加载时就指出来。
				for k := range obj {
					if k != field {
						return fmt.Errorf("变量 %s 的默认值第 %d 项多了字段 %q：列表项只支持一个字段（%s）",
							v.Key, i+1, k, field)
					}
				}
			}
		}
	case VarSelect:
		if len(v.Options) == 0 {
			return fmt.Errorf("变量 %s 是选项模式，必须给 options", v.Key)
		}
		if len(v.Options) > MaxVarOptions {
			return fmt.Errorf("变量 %s 的选项太多（%d 个），最多 %d 个", v.Key, len(v.Options), MaxVarOptions)
		}
		seen := make(map[string]bool, len(v.Options))
		for _, o := range v.Options {
			if o.Value == "" {
				return fmt.Errorf("变量 %s 有一个选项的 value 是空的", v.Key)
			}
			if seen[o.Value] {
				return fmt.Errorf("变量 %s 的选项 %q 重复了", v.Key, o.Value)
			}
			seen[o.Value] = true
		}
		// 默认值必须是合法选项之一，否则"没填过"的实例会拿到一个
		// 界面上下拉里根本没有的值。
		if d := v.DefaultText(); d != "" && !seen[d] {
			return fmt.Errorf("变量 %s 的默认值 %q 不在选项里", v.Key, d)
		}
	default:
		return fmt.Errorf("变量 %s 的类型 %q 不认识（可选：text / textarea / select / number / bool / list / group）", v.Key, v.Type)
	}
	return nil
}

// Coerce 校验并规整一个用户填的值，产出**真正存下来的 JSON 值**。
//
// 入参是界面上那个控件里的原始文本：标量是框里的字，`list` 是若干行文字，
// `group` 是控件自己序列化好的 JSON 数组（**用户不写 JSON**，是那些
// 输入框拼出来的）。返回值通常和入参不同，调用方要用返回值。
//
// **返回 nil 表示"清空这一项"**：调用方应当把这条从取值表里删掉，
// 让默认值重新生效。
func (v Variable) Coerce(raw string) (json.RawMessage, error) {
	if len(raw) > MaxVarValueLen {
		return nil, fmt.Errorf("变量 %s 的值太长（%d 字节，上限 %d）", v.Key, len(raw), MaxVarValueLen)
	}
	switch v.Type {
	case VarSelect:
		for _, o := range v.Options {
			if o.Value == raw {
				return marshalString(raw), nil
			}
		}
		return nil, fmt.Errorf("变量 %s 的取值 %q 不在可选项里", v.Key, raw)
	case VarText:
		// 单行框的首尾空白一定是误触，去掉。
		return marshalString(strings.TrimSpace(raw)), nil
	case VarNumber:
		text := strings.TrimSpace(raw)
		if text == "" {
			return nil, nil
		}
		n, ok := parseNumber(json.RawMessage(text))
		if !ok {
			return nil, fmt.Errorf("变量 %s 要填数字，%q 不是", v.Key, text)
		}
		return json.RawMessage(n.String()), nil
	case VarBool:
		switch strings.TrimSpace(raw) {
		case "":
			return nil, nil
		case "true", "1":
			return json.RawMessage("true"), nil
		case "false", "0":
			return json.RawMessage("false"), nil
		}
		return nil, fmt.Errorf("变量 %s 只能是 true 或 false", v.Key)
	case VarList:
		lines := splitLines(raw)
		if len(lines) == 0 {
			// 一条都没有 = 用户清空了这块，回到默认值。
			// 返回 nil 而不是 `[]`：`[]` 是一个明确的取值（"就是要空列表"），
			// 而清空输入框的意思是"我不要这个自定义值了"，两者不该混为一谈。
			return nil, nil
		}
		return listValue(lines, v.ItemName()), nil
	case VarGroup:
		text := strings.TrimSpace(raw)
		if text == "" {
			return nil, nil
		}
		var arr []json.RawMessage
		if err := json.Unmarshal([]byte(text), &arr); err != nil {
			return nil, fmt.Errorf("变量 %s 的记录读不出来：%w", v.Key, err)
		}
		recs := make([]map[string]json.RawMessage, 0, len(arr))
		for i, it := range arr {
			rec, err := v.coerceRecord(it)
			if err != nil {
				return nil, fmt.Errorf("变量 %s 第 %d 条：%w", v.Key, i+1, err)
			}
			if rec == nil {
				// 点了「+ 添加一条」却没填，不该在磁盘上留一条空记录。
				continue
			}
			recs = append(recs, rec)
		}
		if len(recs) == 0 {
			return nil, nil
		}
		return recordsValue(recs), nil
	default:
		// 长消息原样保留：换行和缩进可能就是内容本身。
		return marshalString(raw), nil
	}
}

// Value 从一份取值表里读出这个变量的最终值：没填过就用默认值。
//
// 返回 nil 表示"既没填过、也没默认值"——界面据此显示空控件，
// 组件拿到的就是 null。
func (v Variable) Value(values map[string]json.RawMessage) json.RawMessage {
	if raw, ok := values[v.Key]; ok {
		return raw
	}
	return v.Default
}

// ValidateVariables 检查一组变量声明：每个都合法，且 key 不重复。
func ValidateVariables(list []Variable) error {
	if len(list) > MaxVariables {
		return fmt.Errorf("变量太多（%d 个），最多 %d 个", len(list), MaxVariables)
	}
	seen := make(map[string]bool, len(list))
	for _, v := range list {
		v = v.Normalize()
		if seen[v.Key] {
			return fmt.Errorf("变量名 %q 重复了", v.Key)
		}
		seen[v.Key] = true
		if err := v.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// parseNumber 只接受 JSON 数字字面量。
//
// 不能直接 `json.Unmarshal(raw, &json.Number)`：`json.Number` 的底层是
// 字符串，把 `"3"` 解进它会**悄悄成功**（得到 "3"），于是"数字字段收到
// 字符串"这种错就漏过去了。先解成 any 再断言类型才靠得住。
func parseNumber(raw json.RawMessage) (json.Number, bool) {
	// 先验整体是不是一个合法的 JSON 值：`13px` 这种会被 Decode 读成 13
	// 然后把 px 留在流里，不报错。
	if !json.Valid(raw) {
		return "", false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", false
	}
	n, ok := v.(json.Number)
	return n, ok
}

// marshalString 把一个字符串包成 JSON 值。字符串一定能编码成功，不会失败。
func marshalString(s string) json.RawMessage {
	b, err := json.Marshal(s)
	if err != nil {
		return json.RawMessage(`""`)
	}
	return b
}

// listValue 把若干行文本包成落盘形式 `[{"text":"…"}]`。
//
// 落盘统一用对象数组而不是字符串数组：以后要给每条加字段（颜色、图标、
// 延迟秒数）时，加一个键就行，不用换形状、不用迁移旧数据。
func listValue(lines []string, field string) json.RawMessage {
	items := make([]map[string]string, 0, len(lines))
	for _, s := range lines {
		items = append(items, map[string]string{field: s})
	}
	b, err := json.Marshal(items)
	if err != nil {
		return json.RawMessage(`[]`)
	}
	return b
}

// splitLines 把界面上的原始文本切成若干条：一行一条，去掉首尾空白与空行。
//
// 空行不算一条：用户在两条之间多敲了一个回车，本意是留白，不是"加一条空的"。
// 首尾空白也去掉——一行弹幕末尾多两个空格没有任何意义。
func splitLines(raw string) []string {
	out := make([]string, 0, 8)
	for _, line := range strings.Split(raw, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// SameJSON 判断两个 JSON 值是否等价（忽略空白与对象键的顺序）。
//
// 用来回答"用户填的正好就是默认值吗"——是的话就不落盘，
// 这样作者日后调默认值，用户没动过的实例才会跟着变。
func SameJSON(a, b json.RawMessage) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == 0 && len(b) == 0
	}
	var av, bv any
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return bytes.Equal(a, b)
	}
	return reflect.DeepEqual(av, bv)
}
