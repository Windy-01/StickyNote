package persistence

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
)

// bundlesRecord 是 bundles.json 的形状。
type bundlesRecord struct {
	// Issued 是"已经发放过"的预置插件 id，按字母序保存，便于人工比对。
	Issued []string `json:"issued"`
}

// BundledIssued 返回已经发放过的预置插件 id。
//
// "发放过"和"装着"是两件不同的事。用户把便签卸载掉之后，
// 下次启动不该被自动装回来——用户删掉的东西不该自己长回来。
// 所以需要一个只增不减的记录，而不是靠"插件目录里有没有"来判断。
func BundledIssued() map[string]bool {
	out := map[string]bool{}
	path, err := under("bundles.json")
	if err != nil {
		return out
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var rec bundlesRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return out
	}
	for _, id := range rec.Issued {
		out[id] = true
	}
	return out
}

// MarkBundledIssued 记录一批预置插件已发放（与已有记录合并）。
func MarkBundledIssued(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	path, err := under("bundles.json")
	if err != nil {
		return err
	}

	merged := map[string]bool{}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("读取预置包记录: %w", err)
	}
	if err == nil {
		var rec bundlesRecord
		if json.Unmarshal(data, &rec) == nil {
			for _, id := range rec.Issued {
				merged[id] = true
			}
		}
	}
	for _, id := range ids {
		merged[id] = true
	}

	list := make([]string, 0, len(merged))
	for id := range merged {
		list = append(list, id)
	}
	sort.Strings(list)

	out, err := json.MarshalIndent(bundlesRecord{Issued: list}, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化预置包记录: %w", err)
	}
	return writeFileAtomic(path, out)
}
