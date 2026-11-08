package component

import (
	"context"
	"encoding/json"
)

// LayoutRepository 保存实例清单与几何。
type LayoutRepository interface {
	Load(ctx context.Context) ([]Instance, error)
	Save(ctx context.Context, instances []Instance) error
}

// RegistryRepository 保存"哪些组件被启用"。
//
// 只存开关，不存组件定义本身：定义永远从磁盘上的插件目录现读，
// 这样用户手改 manifest.json 后重启即生效，不会出现"注册表与文件不一致"。
type RegistryRepository interface {
	Load(ctx context.Context) (map[string]bool, error)
	Save(ctx context.Context, enabled map[string]bool) error
}

// GroupRepository 保存"组"——一组组件的共享外观与窗口行为。
//
// 组是内核数据而不是插件：用户在界面上直接编辑它，所以必须持久化。
// 它和 layout.json 分开存，是因为两者的生命周期完全不同：
// 删一个实例不该动组，改一个组也不该重写所有实例的几何。
type GroupRepository interface {
	Load(ctx context.Context) ([]Group, error)
	Save(ctx context.Context, groups []Group) error
}

// PackageRepository 负责插件包在磁盘上的落位。
//
// 它只管"文件放哪、装没装"，不判断"该不该装"：id 冲突、
// 是否还有实例在跑，这些规则属于应用层。
type PackageRepository interface {
	// Inspect 识别并校验一个目录是什么插件包。
	Inspect(dir string) (Package, error)
	// Install 把 srcDir 复制进插件目录。
	Install(srcDir string) (Package, error)
	// Uninstall 删除一个已安装的插件目录。
	Uninstall(kind PackageKind, id string) error
	// Dir 返回某类插件的安装目录。
	Dir(kind PackageKind, id string) (string, error)
}

// UIRepository 保存内核自身界面的状态（目前只有管理窗口的几何）。
//
// 它不属于任何组件实例，所以不能塞进 LayoutRepository：
// layout.json 里的每一条都对应一个组件窗口，混进去会让
// "实例数" 这类语义变得含糊。
type UIRepository interface {
	Load(ctx context.Context) (Rect, error)
	Save(ctx context.Context, r Rect) error
}

// Storage 是组件的私有键值存储，按 (组件, 实例) 隔离。
//
// 值保持为 json.RawMessage：宿主不理解组件的数据结构，只负责存取。
type Storage interface {
	All(ctx context.Context, componentID, instanceID string) (map[string]json.RawMessage, error)
	Set(ctx context.Context, componentID, instanceID, key string, value json.RawMessage) error
	Remove(ctx context.Context, componentID, instanceID, key string) error
}

// InstanceVars 保存实例的**变量值**——作者声明、用户填的那份配置。
//
// 为什么不让它走 Storage：两者是不同性质的数据。
// 变量是用户在管理界面里填的配置，storage 是组件自己写的运行时数据。
// 混在一个文件里，组件一次 setStorage 就可能把用户填的东西盖掉；
// 而且"删实例时哪些该留"这个问题的答案会变得含糊——现在很清楚：
// 实例没了，它的变量和私有数据都跟着走。
//
// 值保持为 json.RawMessage：文本类变量是一个 JSON 字符串，`json` 类型的
// 变量可以是数组/对象（`[{},{}]` 这种）。宿主同样不理解内容，只负责存取。
type InstanceVars interface {
	// Load 读取一个实例的全部变量值。没填过的变量不会出现在结果里
	// （缺省值由定义里的 Default 兜底，不落盘）。
	Load(ctx context.Context, componentID, instanceID string) (map[string]json.RawMessage, error)
	// Save 覆盖一个实例的全部变量值。
	Save(ctx context.Context, componentID, instanceID string, values map[string]json.RawMessage) error
	// Remove 删除一个实例的变量文件。
	Remove(ctx context.Context, componentID, instanceID string) error
}
