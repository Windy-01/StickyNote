// Package plugin 负责把磁盘上的插件包读成领域对象，以及安装/卸载它们。
//
// 加载只做"解析 + 结构性校验"；安装（install.go）在此之上加了
// id 冲突与保留前缀这些规则。至于"该不该装"（还有实例在跑），
// 那是应用层的判断。
//
// 插件只剩 L2 组件一级：窗口外观已经变成内核里的「组」，不再是可安装的包，
// 所以这里不再有 style.json 的扫描路径。
package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"stickynote/internal/application/componentapp"
	"stickynote/internal/domain/component"
)

// 插件根目录下的子目录与清单文件名。
const (
	componentsDir = "components"
	manifestFile  = "manifest.json"
)

// Load 扫描一个插件根目录，返回其中所有合法的组件。
//
// 单个包解析失败不影响其他包：错误收集起来交给调用方记录日志，
// 用户装坏一个插件不该导致整个程序起不来。
func Load(root string, builtin bool) (componentapp.Catalog, []error) {
	var problems []error
	cat := componentapp.Catalog{
		Components: loadComponents(filepath.Join(root, componentsDir), builtin, &problems),
	}
	return cat, problems
}

// loadComponents 遍历目录下的每个子目录并解析成组件定义。
//
// 排序是刻意的：目录读出来的顺序不保证稳定，而"首次启动选哪个组件"要确定。
func loadComponents(dir string, builtin bool, problems *[]error) []component.Definition {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var out []component.Definition
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		abs, err := filepath.Abs(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		d, err := ReadComponent(abs, builtin)
		if err != nil {
			*problems = append(*problems, err)
			continue
		}
		out = append(out, d)
	}
	return out
}

// Merge 把多个目录的组件合并成一份。同 id 时后者覆盖前者，
// 但会记录一条冲突，由调用方决定怎么提示用户。
func Merge(catalogs ...componentapp.Catalog) (componentapp.Catalog, []error) {
	var out componentapp.Catalog
	var problems []error
	seen := map[string]int{} // id -> out.Components 下标

	for _, cat := range catalogs {
		for _, d := range cat.Components {
			if i, ok := seen[d.ID]; ok {
				problems = append(problems, fmt.Errorf("组件 id 冲突：%s（已用 %s，忽略 %s）",
					d.ID, out.Components[i].SourceDir, d.SourceDir))
				continue
			}
			seen[d.ID] = len(out.Components)
			out.Components = append(out.Components, d)
		}
	}
	return out, problems
}

// forbiddenComponentKeys 是清单里不允许出现的字段。
//
// 它们都是"窗口语义"或旧版字段：语义归组管，出现在组件清单里说明作者想绕过
// 分组，直接判为不兼容而不是静默忽略。
//
// 注意 `style` 不在这张表里：它是旧版（组件绑定 L1 样式）的字段，
// 现在的组件清单里已经没有它，json.Unmarshal 会自然忽略——
// 这样老插件不改也能继续加载，不会因为一个多余字段就装不上。
var forbiddenComponentKeys = []string{"window", "type", "customLayer", "customFlags"}

// ReadComponent 解析单个组件包目录。
//
// 安装流程也用它：待安装的目录和已装好的目录必须走同一套校验，
// 否则会出现"装的时候通过、启动的时候被拒"。
func ReadComponent(dir string, builtin bool) (component.Definition, error) {
	path := filepath.Join(dir, manifestFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return component.Definition{}, fmt.Errorf("读取 %s: %w", path, err)
	}
	var d component.Definition
	if err := json.Unmarshal(data, &d); err != nil {
		return component.Definition{}, fmt.Errorf("解析 %s: %w", path, err)
	}
	if err := checkForbiddenKeys(data); err != nil {
		return component.Definition{}, fmt.Errorf("%s: %w", path, err)
	}
	d.Builtin = builtin
	d.SourceDir = dir
	if err := d.Validate(); err != nil {
		return component.Definition{}, fmt.Errorf("%s: %w", path, err)
	}
	d = d.Normalize()

	// 入口页必须真实存在，否则装上了也是一片空白。
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(d.Entry))); err != nil {
		return component.Definition{}, fmt.Errorf("%s: entry %q 不存在", path, d.Entry)
	}
	return d, nil
}

// checkForbiddenKeys 拦住组件清单里的语义字段。
//
// json.Unmarshal 对未知字段是静默忽略的，所以"写了 window"不会报错、
// 也不会生效——用户会以为窗口被改了其实没有。宁可显式拒绝。
func checkForbiddenKeys(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for _, k := range forbiddenComponentKeys {
		if _, ok := raw[k]; ok {
			return fmt.Errorf("不允许出现字段 %q：窗口行为只能由组（组设置面板）决定", k)
		}
	}
	return nil
}
