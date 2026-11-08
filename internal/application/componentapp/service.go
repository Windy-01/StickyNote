package componentapp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"stickynote/internal/domain/component"
)

// DefaultGroupID 是"新组件落到哪里"的兜底组。
//
// 新装的组件必须有个家，否则它没有外观可套、也就显示不出来。
// 兜底组刻意是「常规层级 + 可交互」：用户装完一个组件，最先看到的应该是
// 一个能点能拖的正常窗口，而不是一个穿透明、点不到的影子。
const DefaultGroupID = "builtin.group.default"

// GroupPalette 是给组自动分配识别色用的调色板。
//
// 布局板和侧边栏靠颜色区分组，所以这组颜色要**彼此好分**：
// 色相拉开、明度接近，在浅色纸底和深色方块上都看得清。
var GroupPalette = []string{
	"#c08a1e", // 琥珀（主色）
	"#4a7fb5", // 钢蓝
	"#5b9c6b", // 苔绿
	"#b5626a", // 砖红
	"#8a6bb0", // 紫
	"#3f8f92", // 青
	"#a8763e", // 棕
	"#6b7280", // 灰
}

// pickColor 挑一个当前用得最少的调色板颜色。
//
// 用"用得最少"而不是"按顺序取下一个"：用户删过组之后，按顺序取会撞上
// 还在用的颜色；用得最少的那个一定是最不挤的。
func (s *Service) pickColor() string {
	count := map[string]int{}
	for _, c := range GroupPalette {
		count[c] = 0
	}
	for _, g := range s.groupList {
		if g.Color != "" {
			count[g.Color]++
		}
	}
	best := GroupPalette[0]
	for _, c := range GroupPalette {
		if count[c] < count[best] {
			best = c
		}
	}
	return best
}

// Catalog 是一次加载得到的全部静态定义。
//
// 它由基础设施层的加载器产出，应用层只负责挑选与编排。
// 只剩组件：窗口外观已经是内核里的「组」，不再从磁盘加载。
type Catalog struct {
	Components []component.Definition
}

// Service 是内核的应用服务：管组、管组件、管实例。
//
// 它不认识窗口，也不认识 WebView——表现层问它要 InstanceView，
// 自己决定怎么把窗口画出来。
type Service struct {
	layout   component.LayoutRepository
	registry component.RegistryRepository
	groups   component.GroupRepository
	vars     component.InstanceVars
	storage  component.Storage
	ui       component.UIRepository
	packages component.PackageRepository

	// loader 由组合根注入：重新扫描插件目录。
	// 应用层不知道插件存在哪、怎么解析，那是基础设施的事。
	loader func() (Catalog, []error)

	components map[string]component.Definition
	enabled    map[string]bool
	groupList  []component.Group
	instances  []component.Instance

	// varValues 是各实例的变量值（**只含用户改过的**，键是实例 id）。
	//
	// 启动时一次读进内存，而不是每次取快照都读文件：viewOf 每次状态推送
	// 都要对每个实例跑一遍，那里做 IO 会把整个界面拖慢。写的时候同时更新
	// 内存与磁盘，两边始终一致。
	//
	// 实例 id 全局唯一（"<组件id>-<序号>"），所以不需要复合键。
	varValues map[string]map[string]json.RawMessage

	// bootstrap 是"首次出现时默认启用"的组件 id，由组合根在 Boot 之前告知。
	bootstrap map[string]bool
	// seedGroups 与 seedMember 是首次启动时生成的组，以及"哪个组件默认进哪个组"。
	// 这是产品决策（时钟贴桌面、便签置顶），放在组合根里，应用层只负责执行。
	seedGroups []component.Group
	seedMember map[string]string
}

// NewService 组装应用服务。
func NewService(
	layout component.LayoutRepository,
	registry component.RegistryRepository,
	groups component.GroupRepository,
	vars component.InstanceVars,
	storage component.Storage,
	ui component.UIRepository,
	packages component.PackageRepository,
) *Service {
	return &Service{
		layout:     layout,
		registry:   registry,
		groups:     groups,
		vars:       vars,
		storage:    storage,
		ui:         ui,
		packages:   packages,
		components: map[string]component.Definition{},
		enabled:    map[string]bool{},
		bootstrap:  map[string]bool{},
		seedMember: map[string]string{},
		varValues:  map[string]map[string]json.RawMessage{},
	}
}

// SetBootstrapEnabled 声明"首次出现时默认启用"的组件 id。
//
// 只影响注册表里**没有记录**的情况：用户后来把它关掉，下次启动仍然是关的。
func (s *Service) SetBootstrapEnabled(ids ...string) {
	for _, id := range ids {
		if id != "" {
			s.bootstrap[id] = true
		}
	}
}

// SetDefaultGroups 声明首次启动时生成哪些组，以及组件的默认归属。
//
// member 里没提到的组件一律进兜底组；兜底组不存在时由 ensureGroups 补出来。
func (s *Service) SetDefaultGroups(groups []component.Group, member map[string]string) {
	s.seedGroups = groups
	if member != nil {
		s.seedMember = member
	}
}

// defaultEnabled 判断一个注册表里没记录的组件该不该默认启用。
func (s *Service) defaultEnabled(def component.Definition) bool {
	return def.Builtin || s.bootstrap[def.ID]
}

// SetCatalogLoader 注入"重新扫描插件目录"的能力。
//
// 安装与卸载之后必须重扫：目录是插件唯一的真相来源，
// 缓存里那份只在启动时读一次，不重扫的话界面会一直显示旧状态。
func (s *Service) SetCatalogLoader(fn func() (Catalog, []error)) {
	s.loader = fn
}

// indexCatalog 把静态目录摊成按 id 索引的表。
func indexCatalog(cat Catalog) map[string]component.Definition {
	comps := make(map[string]component.Definition, len(cat.Components))
	for _, d := range cat.Components {
		comps[d.ID] = d
	}
	return comps
}

// fillEnabled 给注册表里没有记录的组件补上默认启用状态。
func (s *Service) fillEnabled() {
	for id, d := range s.components {
		if _, ok := s.enabled[id]; !ok {
			s.enabled[id] = s.defaultEnabled(d)
		}
	}
}

// Boot 载入静态目录与持久化状态，并保证至少有一个可运行的实例。
//
// 内置组件缺省启用；第三方组件缺省停用，等用户在设置里打开。
//
// 顺序有讲究：**先读实例，再整理分组**。归属落在实例上，而迁移老数据
// （旧模型把归属记在组上）必须先有实例清单才谈得上"把归属搬到实例上"。
func (s *Service) Boot(ctx context.Context, cat Catalog) error {
	s.components = indexCatalog(cat)

	enabled, err := s.registry.Load(ctx)
	if err != nil {
		return fmt.Errorf("读取组件注册表: %w", err)
	}
	if enabled == nil {
		enabled = map[string]bool{}
	}
	s.enabled = enabled
	s.fillEnabled()

	groups, err := s.groups.Load(ctx)
	if err != nil {
		return fmt.Errorf("读取组配置: %w", err)
	}
	s.groupList = groups

	instances, err := s.layout.Load(ctx)
	if err != nil {
		return fmt.Errorf("读取实例布局: %w", err)
	}
	s.instances = instances

	// 丢掉指向已消失组件的实例记录。
	kept := s.instances[:0]
	for _, inst := range s.instances {
		if _, ok := s.components[inst.ComponentID]; !ok {
			log.Printf("丢弃实例 %s：组件 %s 已不存在", inst.ID, inst.ComponentID)
			continue
		}
		kept = append(kept, inst)
	}
	s.instances = kept

	if err := s.ensureGroups(ctx); err != nil {
		return err
	}

	// 首次运行（或所有实例都被丢弃）时按预置归属各建一个实例：
	// 用户第一眼就该看到分组的效果，而不是一个空桌面。
	if len(s.instances) == 0 {
		s.seedInstances(ctx)
	}

	// 变量值一次读进内存。**放在补实例之后**：实例是这一轮才建出来的
	// （首次启动、或者 layout.json 被删过），磁盘上可能已经有它的变量文件，
	// 那就该读回来——而不是让用户重新填一遍。
	//
	// 读失败只记日志、按"没填过"处理：变量丢了组件还能用默认值跑，
	// 为它让整个程序起不来不值得。
	for _, inst := range s.instances {
		values, err := s.vars.Load(ctx, inst.ComponentID, inst.ID)
		if err != nil {
			log.Printf("读取实例 %s 的变量失败，按默认值处理: %v", inst.ID, err)
			continue
		}
		if len(values) > 0 {
			s.varValues[inst.ID] = values
		}
	}

	return s.SaveLayout(ctx)
}

// seedInstances 按预置归属建初始实例。
//
// 归属表（seedMember）由组合根给：时钟贴桌面、便签置顶是产品决策，
// 应用层只负责执行。表里没提到的组件不建实例——它们留空，
// 等用户自己在组里挑。
func (s *Service) seedInstances(ctx context.Context) {
	if len(s.seedMember) == 0 {
		// 没给归属表就退一步：默认启用的组件各开一个，都放兜底组。
		gid := s.ensureDefaultGroup()
		for _, cv := range s.Components() {
			if !cv.Enabled || len(cv.InstanceIDs) > 0 {
				continue
			}
			if _, err := s.CreateInstance(ctx, cv.ID, gid); err != nil {
				log.Printf("为组件 %s 创建初始实例失败: %v", cv.ID, err)
			}
		}
		return
	}

	// map 遍历无序，先排序再建：实例 id 带序号，落盘顺序要稳定。
	ids := make([]string, 0, len(s.seedMember))
	for cid := range s.seedMember {
		ids = append(ids, cid)
	}
	sort.Strings(ids)
	for _, cid := range ids {
		if _, ok := s.components[cid]; !ok {
			continue
		}
		gid := s.seedMember[cid]
		if s.groupIndex(gid) < 0 {
			// 归属表指向的组已经不在了（用户删过组），落兜底组，
			// 别为了一个默认偏好把组件变成"开不出窗口"。
			gid = s.ensureDefaultGroup()
		}
		if _, err := s.CreateInstance(ctx, cid, gid); err != nil {
			log.Printf("为组件 %s 创建初始实例失败: %v", cid, err)
		}
	}
}

// ensureGroups 保证组与归属自洽：
//
//   - 组列表为空（首次启动）时生成默认组；
//   - 老数据把归属存在组上（`Group.Components`），迁到实例的 `GroupID` 上；
//   - 每个实例都得有一个存在的组，指向已消失的组就落回兜底组；
//   - 组没有识别色就补一个。
//
// 这些事必须每次启动都做：用户可能在程序关着的时候删了插件目录、
// 手改了 groups.json，或者是从旧版本升级上来的。
func (s *Service) ensureGroups(ctx context.Context) error {
	changed := false

	if len(s.groupList) == 0 {
		s.groupList = seedOrDefault(s.seedGroups)
		changed = true
		log.Printf("首次启动：生成 %d 个默认组", len(s.groupList))
	}

	// 老模型的归属：组件 → 组。只在实例自己还没有归属时用一次。
	// 同一组件出现在多个组里时先出现的赢——顺序即优先级，结果确定。
	legacy := map[string]string{}
	for _, g := range s.groupList {
		for _, cid := range g.Components {
			if _, ok := s.components[cid]; !ok {
				continue
			}
			if _, dup := legacy[cid]; dup {
				continue
			}
			legacy[cid] = g.ID
		}
	}

	for i := range s.instances {
		inst := &s.instances[i]
		if inst.GroupID != "" && s.groupIndex(inst.GroupID) >= 0 {
			continue
		}
		if inst.GroupID != "" {
			log.Printf("实例 %s：组 %s 已不存在，改放兜底组", inst.ID, inst.GroupID)
		}
		gid := legacy[inst.ComponentID]
		if s.groupIndex(gid) < 0 {
			gid = s.ensureDefaultGroup()
		}
		inst.GroupID = gid
		changed = true
	}

	// 迁移完了就把组上的成员字段清空：归属从此只有实例这一个来源。
	// 两处都留着迟早会打架——改了组上的清单，实例却不跟着动。
	for i := range s.groupList {
		if len(s.groupList[i].Components) > 0 {
			s.groupList[i].Components = nil
			changed = true
		}
	}

	// 没颜色的组补一个：颜色是布局板的识别标记，缺了就没法按组区分。
	// 老数据（这个字段出现之前生成的组）走这条路补上。
	for i := range s.groupList {
		if s.groupList[i].Color != "" {
			continue
		}
		s.groupList[i].Color = s.pickColor()
		changed = true
	}

	if !changed {
		return nil
	}
	if err := s.saveGroups(ctx); err != nil {
		return err
	}
	// 归属刚补过，实例清单也要落盘，否则下次启动还得再迁一遍。
	return s.SaveLayout(ctx)
}

// ensureDefaultGroup 保证兜底组存在，返回它的 id。
//
// 兜底组是"没地方去"的落脚点：新装的组件、指向已消失组的实例都靠它。
func (s *Service) ensureDefaultGroup() string {
	if s.groupIndex(DefaultGroupID) >= 0 {
		return DefaultGroupID
	}
	s.groupList = append(s.groupList, defaultGroup())
	log.Printf("兜底组不存在，重新生成")
	return DefaultGroupID
}

// seedOrDefault 返回要生成的初始组。组合根没给就用一个兜底组。
func seedOrDefault(seed []component.Group) []component.Group {
	if len(seed) > 0 {
		out := make([]component.Group, len(seed))
		copy(out, seed)
		return out
	}
	return []component.Group{defaultGroup()}
}

func defaultGroup() component.Group {
	return component.Group{
		ID:     DefaultGroupID,
		Name:   "默认",
		Window: component.DefaultWindowSpec(),
	}
}

// groupIndex 返回组在列表里的下标，不存在为 -1。
func (s *Service) groupIndex(id string) int {
	for i := range s.groupList {
		if s.groupList[i].ID == id {
			return i
		}
	}
	return -1
}

// groupByID 按 id 取组。
func (s *Service) groupByID(id string) (component.Group, bool) {
	if idx := s.groupIndex(id); idx >= 0 {
		return s.groupList[idx], true
	}
	return component.Group{}, false
}

// saveGroups 把组落盘。
func (s *Service) saveGroups(ctx context.Context) error {
	if err := s.groups.Save(ctx, s.groupList); err != nil {
		return fmt.Errorf("保存组配置: %w", err)
	}
	return nil
}

// Groups 返回组列表快照，顺序即侧边栏顺序。
func (s *Service) Groups() []GroupView {
	out := make([]GroupView, 0, len(s.groupList))
	for _, g := range s.groupList {
		out = append(out, GroupView{
			ID:      g.ID,
			Name:    g.Name,
			Color:   g.Color,
			Window:  g.Window.Normalize(),
			Visual:  g.Visual,
			CSSVars: g.CSSVars,
		})
	}
	return out
}

// Group 按 id 取组快照。
func (s *Service) Group(id string) (GroupView, bool) {
	for _, g := range s.Groups() {
		if g.ID == id {
			return g, true
		}
	}
	return GroupView{}, false
}

// CreateGroup 新建一个空组，名字重复时自动加序号。
func (s *Service) CreateGroup(ctx context.Context, name string) (GroupView, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "新组"
	}
	name = s.uniqueGroupName(name)

	g := component.Group{
		ID:     s.newGroupID(),
		Name:   name,
		Color:  s.pickColor(),
		Window: component.DefaultWindowSpec(),
	}
	if err := g.Validate(); err != nil {
		return GroupView{}, err
	}
	s.groupList = append(s.groupList, g)
	if err := s.saveGroups(ctx); err != nil {
		s.groupList = s.groupList[:len(s.groupList)-1]
		return GroupView{}, err
	}
	v, ok := s.Group(g.ID)
	if !ok {
		return GroupView{}, fmt.Errorf("%w: %s", component.ErrGroupNotFound, g.ID)
	}
	return v, nil
}

// uniqueGroupName 给重名加一个序号后缀，省得侧边栏里出现两个"新组"。
func (s *Service) uniqueGroupName(name string) string {
	used := map[string]bool{}
	for _, g := range s.groupList {
		used[g.Name] = true
	}
	if !used[name] {
		return name
	}
	for n := 2; ; n++ {
		cand := fmt.Sprintf("%s %d", name, n)
		if !used[cand] {
			return cand
		}
	}
}

// newGroupID 生成 "group-<n>"，跳过已占用的编号。
func (s *Service) newGroupID() string {
	used := map[string]bool{}
	for _, g := range s.groupList {
		used[g.ID] = true
	}
	for n := 1; ; n++ {
		id := fmt.Sprintf("group-%d", n)
		if !used[id] {
			return id
		}
	}
}

// UpdateGroup 覆盖一个组的名字、窗口开关与外观。
//
// 只有 id 不可改：它是实例归属的依据，改了就等于换了一个组。
func (s *Service) UpdateGroup(ctx context.Context, in component.Group) error {
	idx := s.groupIndex(in.ID)
	if idx < 0 {
		return fmt.Errorf("%w: %s", component.ErrGroupNotFound, in.ID)
	}
	prev := s.groupList[idx]

	// 归属不在这里改：那要动的是实例，走 SetInstanceGroup。
	in.Components = prev.Components
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		in.Name = prev.Name
	}
	// 颜色清空时保留原来的：它是布局板的识别标记，没有"不设颜色"这个状态，
	// 真留空会让那个组的方块在布局板上失去归属。
	if in.Color == "" {
		in.Color = prev.Color
	}
	in = in.Normalize()
	if err := in.Validate(); err != nil {
		return err
	}

	s.groupList[idx] = in
	if err := s.saveGroups(ctx); err != nil {
		s.groupList[idx] = prev
		return err
	}
	return nil
}

// DeleteGroup 删除一个空组。
//
// 里面有实例的组不能删：那些窗口会失去外观，等于把用户的东西弄丢了。
// 想删就先把里面的实例挪走。
func (s *Service) DeleteGroup(ctx context.Context, id string) error {
	idx := s.groupIndex(id)
	if idx < 0 {
		return fmt.Errorf("%w: %s", component.ErrGroupNotFound, id)
	}
	if n := len(s.InstanceIDsInGroup(id)); n > 0 {
		return fmt.Errorf("组「%s」里还有 %d 个实例，请先把它们移到别的组",
			s.groupList[idx].Name, n)
	}
	prev := s.groupList
	s.groupList = append(append([]component.Group(nil), s.groupList[:idx]...), s.groupList[idx+1:]...)
	if err := s.saveGroups(ctx); err != nil {
		s.groupList = prev
		return err
	}
	return nil
}

// SetInstanceGroup 把一个实例挪到另一个组。
//
// 挪动会改变它的窗口行为（层级、穿透…），表现层据此重建那一个窗口。
// 只动这一个实例：同一个组件的另一个实例可以待在自己的组里不动。
func (s *Service) SetInstanceGroup(ctx context.Context, instanceID, groupID string) error {
	inst := s.find(instanceID)
	if inst == nil {
		return fmt.Errorf("%w: %s", component.ErrInstanceNotFound, instanceID)
	}
	if s.groupIndex(groupID) < 0 {
		return fmt.Errorf("%w: %s", component.ErrGroupNotFound, groupID)
	}
	if inst.GroupID == groupID {
		return nil
	}
	prev := inst.GroupID
	inst.GroupID = groupID
	if err := s.SaveLayout(ctx); err != nil {
		inst.GroupID = prev
		return err
	}
	return nil
}

// InstanceIDsInGroup 列出组内实例 id，供表现层重建窗口。
func (s *Service) InstanceIDsInGroup(groupID string) []string {
	var out []string
	for _, inst := range s.instances {
		if inst.GroupID == groupID {
			out = append(out, inst.ID)
		}
	}
	return out
}

// ReloadCatalog 重新扫描插件目录并把结果并入当前目录。
func (s *Service) ReloadCatalog(ctx context.Context) error {
	if s.loader == nil {
		return fmt.Errorf("未注入插件目录加载器")
	}
	cat, problems := s.loader()
	for _, p := range problems {
		log.Printf("目录问题: %v", p)
	}
	return s.Reload(ctx, cat)
}

// InstallPackage 安装一个插件包并立刻生效。
//
// 文件复制成功但目录合并失败时必须回滚：否则插件已经躺在插件目录里、
// 却没进入内存中的目录，界面会显示"没装"，而下次启动又冒出来。
func (s *Service) InstallPackage(ctx context.Context, srcDir string) (component.Package, error) {
	pkg, err := s.packages.Install(srcDir)
	if err != nil {
		return pkg, err
	}
	if err := s.ReloadCatalog(ctx); err != nil {
		if undoErr := s.packages.Uninstall(pkg.Kind, pkg.ID); undoErr != nil {
			log.Printf("回滚插件 %s 失败: %v", pkg.ID, undoErr)
		}
		return pkg, err
	}
	return pkg, nil
}

// UninstallPackage 卸载一个插件包。
//
// 还有实例的组件不能卸载：实例带着用户的数据与几何，组件没了它们就成了孤儿。
func (s *Service) UninstallPackage(ctx context.Context, kind component.PackageKind, id string) error {
	if kind != component.PackageComponent {
		return fmt.Errorf("未知的插件类型 %q", kind)
	}
	if n := s.countInstances(id); n > 0 {
		return fmt.Errorf("组件 %s 还有 %d 个实例，请先关闭这些实例再卸载", id, n)
	}
	if err := s.packages.Uninstall(kind, id); err != nil {
		return err
	}
	return s.ReloadCatalog(ctx)
}

// PackageDir 返回插件的安装目录，用于"打开所在文件夹"。
func (s *Service) PackageDir(kind component.PackageKind, id string) (string, error) {
	return s.packages.Dir(kind, id)
}

// Reload 用新的静态目录替换当前目录，供"安装/卸载插件"之后调用。
//
// 有一条硬约束：**有实例的组件不能在目录里消失**。实例带着用户的几何与
// 私有数据，如果组件定义突然没了，这些实例就失去了归属——要么静默丢弃
// （数据还在但用户看不见），要么报错。两种都不好，所以直接拒绝，
// 由调用方先把实例关掉再卸载。
func (s *Service) Reload(ctx context.Context, cat Catalog) error {
	comps := indexCatalog(cat)

	seen := map[string]bool{}
	for _, inst := range s.instances {
		if seen[inst.ComponentID] {
			continue
		}
		seen[inst.ComponentID] = true
		if _, ok := comps[inst.ComponentID]; !ok {
			return fmt.Errorf("组件 %s 还有 %d 个实例，请先关闭这些实例再卸载",
				inst.ComponentID, s.countInstances(inst.ComponentID))
		}
	}

	s.components = comps

	// 消失的组件从注册表里清掉，新出现的按"内置缺省启用"补齐。
	for id := range s.enabled {
		if _, ok := comps[id]; !ok {
			delete(s.enabled, id)
		}
	}
	s.fillEnabled()
	if err := s.registry.Save(ctx, s.enabled); err != nil {
		return err
	}
	// 新装的组件要有个组，卸载掉的要从组里摘出来。
	return s.ensureGroups(ctx)
}

// instanceIDsOf 返回某组件名下的全部实例 id，按创建顺序。
func (s *Service) instanceIDsOf(componentID string) []string {
	var out []string
	for _, inst := range s.instances {
		if inst.ComponentID == componentID {
			out = append(out, inst.ID)
		}
	}
	return out
}

func (s *Service) countInstances(componentID string) int {
	return len(s.instanceIDsOf(componentID))
}

// UIState 读内核自身界面的状态（管理窗口几何）。
func (s *Service) UIState(ctx context.Context) (component.Rect, error) {
	if s.ui == nil {
		return component.Rect{}, nil
	}
	return s.ui.Load(ctx)
}

// SetUIState 写内核自身界面的状态。
func (s *Service) SetUIState(ctx context.Context, r component.Rect) error {
	if s.ui == nil {
		return nil
	}
	return s.ui.Save(ctx, r)
}

// ActiveInstances 返回当前可以真正显示窗口的实例（组件启用 + 已分组）。
func (s *Service) ActiveInstances() []InstanceView { return s.instanceViews(true) }

// AllInstances 返回全部实例，包括所属组件已停用的那些。
//
// 管理界面要用它：停用组件不等于删除实例，用户需要看到"有 3 个实例在等着"，
// 否则一停用就好像数据没了。
func (s *Service) AllInstances() []InstanceView { return s.instanceViews(false) }

// instanceViews 把实例摊成快照，requireEnabled 的含义见 viewOf。
func (s *Service) instanceViews(requireEnabled bool) []InstanceView {
	out := make([]InstanceView, 0, len(s.instances))
	for _, inst := range s.instances {
		if v, ok := s.viewOf(inst, requireEnabled); ok {
			out = append(out, v)
		}
	}
	return out
}

// Instance 按 id 取单个实例快照。
func (s *Service) Instance(id string) (InstanceView, bool) {
	for _, inst := range s.instances {
		if inst.ID == id {
			return s.viewOf(inst, false)
		}
	}
	return InstanceView{}, false
}

// CreateInstance 为组件新建一个实例（内存态，需 SaveLayout 落盘）。
//
// groupID 为空表示"放兜底组"。组必须在建实例之前就存在：外观与窗口行为
// 全靠它解析，先建实例再补组会让表现层拿到一个解析不出外观的快照。
func (s *Service) CreateInstance(ctx context.Context, componentID, groupID string) (InstanceView, error) {
	def, ok := s.components[componentID]
	if !ok {
		return InstanceView{}, fmt.Errorf("%w: %s", component.ErrComponentNotFound, componentID)
	}
	if groupID == "" {
		// 空表示"放兜底组"。兜底组万一被删了就先补一个：
		// 新建实例总得有个落脚点，报错让用户自己建组太不友好。
		had := s.groupIndex(DefaultGroupID) >= 0
		groupID = s.ensureDefaultGroup()
		if !had {
			if err := s.saveGroups(ctx); err != nil {
				return InstanceView{}, err
			}
		}
	}
	if s.groupIndex(groupID) < 0 {
		return InstanceView{}, fmt.Errorf("%w: %s", component.ErrGroupNotFound, groupID)
	}
	if def.Singleton && s.countInstances(componentID) > 0 {
		return InstanceView{}, fmt.Errorf("组件 %s 是单例，已经有一个实例了", def.Name)
	}

	inst := component.Instance{
		ID:          s.newInstanceID(componentID),
		ComponentID: componentID,
		GroupID:     groupID,
		Bounds: component.Rect{
			Width:  def.DefaultBounds.Width,
			Height: def.DefaultBounds.Height,
		},
		Visible:   true,
		CreatedAt: time.Now(),
	}
	s.instances = append(s.instances, inst)
	v, ok := s.viewOf(inst, false)
	if !ok {
		s.instances = s.instances[:len(s.instances)-1]
		return InstanceView{}, fmt.Errorf("实例 %s 无法解析外观", inst.ID)
	}
	return v, nil
}

// RemoveInstance 删除一个实例记录。
//
// 它的变量值跟着一起删：变量是"这个窗口怎么显示"的配置，窗口都没了，
// 留着那份文件只会在下次装回来时给一个同名新实例塞上旧的配置。
func (s *Service) RemoveInstance(ctx context.Context, id string) error {
	idx := -1
	for i := range s.instances {
		if s.instances[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("%w: %s", component.ErrInstanceNotFound, id)
	}
	inst := s.instances[idx]
	s.instances = append(s.instances[:idx], s.instances[idx+1:]...)
	delete(s.varValues, id)
	if err := s.vars.Remove(ctx, inst.ComponentID, inst.ID); err != nil {
		// 删不掉也不该让"删实例"失败：实例记录已经没了，留个孤零零的
		// 变量文件顶多占几十字节，而报错会让界面以为没删掉。
		log.Printf("删除实例 %s 的变量文件失败: %v", id, err)
	}
	return nil
}

// Vars 返回一个实例的变量**最终值**：声明过的每个变量都有一项，没填过的是默认值。
//
// 只返回声明过的变量：文件里可能留着作者后来删掉的旧 key（从旧版本升级上来的
// 数据），那些不该出现在界面上，也不该下发给组件。
//
// 值保持为 json.RawMessage：文本类是一个 JSON 字符串，`json` 类型的变量
// 可以是数组或对象——组件拿到的就是它当初声明的那种形状。
func (s *Service) Vars(ctx context.Context, instanceID string) (map[string]json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	inst := s.find(instanceID)
	if inst == nil {
		return nil, fmt.Errorf("%w: %s", component.ErrInstanceNotFound, instanceID)
	}
	return s.varSnapshot(*inst), nil
}

// SetVars 覆盖一个实例的变量值。
//
// values 里**没提到的变量保持原样**（表单可以只提交改过的那几个）；提到的
// 一律先过 Coerce——选项必须是选项之一，JSON 变量必须是合法 JSON，单行框
// 去首尾空白。与默认值相同的值不落盘，这样作者日后调整默认值时，用户没动过
// 的变量会自动跟着变。
func (s *Service) SetVars(ctx context.Context, instanceID string, values map[string]string) error {
	inst := s.find(instanceID)
	if inst == nil {
		return fmt.Errorf("%w: %s", component.ErrInstanceNotFound, instanceID)
	}
	def, ok := s.components[inst.ComponentID]
	if !ok {
		return fmt.Errorf("%w: %s", component.ErrComponentNotFound, inst.ComponentID)
	}

	next := make(map[string]json.RawMessage, len(s.varValues[instanceID])+len(values))
	for k, v := range s.varValues[instanceID] {
		next[k] = v
	}
	declared := make(map[string]bool, len(def.Variables))
	for _, v := range def.Variables {
		declared[v.Key] = true
		raw, ok := values[v.Key]
		if !ok {
			continue
		}
		coerced, err := v.Coerce(raw)
		if err != nil {
			return err
		}
		// nil 表示"清空这一项"（JSON 变量被清空输入框），让它回到默认值。
		if len(coerced) == 0 || component.SameJSON(coerced, v.Default) {
			delete(next, v.Key)
			continue
		}
		next[v.Key] = coerced
	}
	// 声明里已经没有的 key 直接丢掉：留着它们既没人读，又会在下次
	// 作者重新用同一个 key 时冒出一个"用户从没填过"的值。
	for k := range next {
		if !declared[k] {
			delete(next, k)
		}
	}

	if err := s.vars.Save(ctx, inst.ComponentID, inst.ID, next); err != nil {
		return fmt.Errorf("保存实例变量: %w", err)
	}
	if len(next) == 0 {
		delete(s.varValues, inst.ID)
	} else {
		s.varValues[inst.ID] = next
	}
	return nil
}

// varSnapshot 把"存下来的值"和"声明里的默认值"合成一份完整取值表。
func (s *Service) varSnapshot(inst component.Instance) map[string]json.RawMessage {
	def, ok := s.components[inst.ComponentID]
	if !ok || len(def.Variables) == 0 {
		return nil
	}
	stored := s.varValues[inst.ID]
	out := make(map[string]json.RawMessage, len(def.Variables))
	for _, v := range def.Variables {
		out[v.Key] = v.Value(stored)
	}
	return out
}

// SetBounds 记录实例几何（只改内存，落盘由 SaveLayout 统一做）。
func (s *Service) SetBounds(_ context.Context, id string, r component.Rect) error {
	inst := s.find(id)
	if inst == nil {
		return fmt.Errorf("%w: %s", component.ErrInstanceNotFound, id)
	}
	inst.Bounds = r
	inst.Positioned = true
	return nil
}

// SetZoom 记录实例的内容缩放倍数（只改内存，落盘由 SaveLayout 统一做）。
//
// 夹取交给 component.Instance.EffectiveZoom：调用方给什么都不会把
// 数据写坏，读回来永远是 [MinZoom, MaxZoom] 里的值。
func (s *Service) SetZoom(_ context.Context, id string, zoom float64) error {
	inst := s.find(id)
	if inst == nil {
		return fmt.Errorf("%w: %s", component.ErrInstanceNotFound, id)
	}
	inst.Zoom = component.Instance{Zoom: zoom}.EffectiveZoom()
	return nil
}

// SetVisible 记录实例的显隐期望。
func (s *Service) SetVisible(_ context.Context, id string, visible bool) error {
	inst := s.find(id)
	if inst == nil {
		return fmt.Errorf("%w: %s", component.ErrInstanceNotFound, id)
	}
	inst.Visible = visible
	return nil
}

// SetEnabled 启用/停用组件。
//
// 停用不删实例：几何与私有数据都留着，重新启用时原样回来。
func (s *Service) SetEnabled(ctx context.Context, componentID string, on bool) error {
	if _, ok := s.components[componentID]; !ok {
		return fmt.Errorf("%w: %s", component.ErrComponentNotFound, componentID)
	}
	s.enabled[componentID] = on
	return s.registry.Save(ctx, s.enabled)
}

// Enabled 报告组件是否启用。
func (s *Service) Enabled(componentID string) bool { return s.enabled[componentID] }

// SaveLayout 把实例清单落盘。
func (s *Service) SaveLayout(ctx context.Context) error {
	if err := s.layout.Save(ctx, s.instances); err != nil {
		return fmt.Errorf("保存实例布局: %w", err)
	}
	return nil
}

// Storage 取实例的全部私有数据。
func (s *Service) Storage(ctx context.Context, instanceID string) (map[string]json.RawMessage, error) {
	inst := s.find(instanceID)
	if inst == nil {
		return nil, fmt.Errorf("%w: %s", component.ErrInstanceNotFound, instanceID)
	}
	return s.storage.All(ctx, inst.ComponentID, inst.ID)
}

// SetStorage 写入一个键。值由组件自己决定形状，宿主不解释。
func (s *Service) SetStorage(ctx context.Context, instanceID, key string, raw json.RawMessage) error {
	inst := s.find(instanceID)
	if inst == nil {
		return fmt.Errorf("%w: %s", component.ErrInstanceNotFound, instanceID)
	}
	if key == "" {
		return fmt.Errorf("storage key 不能为空")
	}
	return s.storage.Set(ctx, inst.ComponentID, inst.ID, key, raw)
}

// RemoveStorage 删除一个键。
func (s *Service) RemoveStorage(ctx context.Context, instanceID, key string) error {
	inst := s.find(instanceID)
	if inst == nil {
		return fmt.Errorf("%w: %s", component.ErrInstanceNotFound, instanceID)
	}
	return s.storage.Remove(ctx, inst.ComponentID, inst.ID, key)
}

// Components 返回组件列表快照，按 id 排序。
func (s *Service) Components() []ComponentView {
	out := make([]ComponentView, 0, len(s.components))
	for id, d := range s.components {
		out = append(out, s.componentView(id, d))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Component 按 id 取组件快照。
func (s *Service) Component(id string) (ComponentView, bool) {
	d, ok := s.components[id]
	if !ok {
		return ComponentView{}, false
	}
	return s.componentView(id, d), true
}

// componentView 把一个组件定义摊成界面要的快照。
//
// 组件本身不再有组：它可能开着三个实例，分别待在不同的组里。
// 想知道"它现在长什么样"，得看实例。
func (s *Service) componentView(id string, d component.Definition) ComponentView {
	d = d.Normalize()
	return ComponentView{
		ID:          id,
		Name:        d.Name,
		Author:      d.Author,
		Builtin:     d.Builtin,
		Enabled:     s.enabled[id],
		Description: d.Description,
		Singleton:   d.Singleton,
		Variables:   d.Variables,
		InstanceIDs: s.instanceIDsOf(id),
	}
}

// viewOf 把实例与它的定义、组合成快照。
//
// requireEnabled 为 true 时，所属组件被停用就返回 false（不可显示）；
// 管理界面传 false，这样停用组件后仍能看到它的实例还在。
func (s *Service) viewOf(inst component.Instance, requireEnabled bool) (InstanceView, bool) {
	def, ok := s.components[inst.ComponentID]
	if !ok {
		return InstanceView{}, false
	}
	if requireEnabled && !s.enabled[inst.ComponentID] {
		return InstanceView{}, false
	}
	// 组解析不出来就没有外观，等于不可显示。ensureGroups 保证每个实例
	// 都有归属，走到兜底说明数据被手改坏了——宁可按兜底组显示，
	// 也别让一个还存在的实例凭空消失。
	g, ok := s.groupByID(inst.GroupID)
	if !ok {
		if g, ok = s.groupByID(DefaultGroupID); !ok {
			return InstanceView{}, false
		}
		log.Printf("实例 %s 的组 %q 不存在，按兜底组显示", inst.ID, inst.GroupID)
	}
	def = def.Normalize()
	return InstanceView{
		ID:            inst.ID,
		ComponentID:   inst.ComponentID,
		ComponentName: def.Name,
		Entry:         def.Entry,
		AssetsDir:     def.SourceDir,
		GroupID:       g.ID,
		GroupName:     g.Name,
		Style:         component.Resolve(g, def.Visual, def.CSSVars),
		Vars:          s.varSnapshot(inst),
		Bounds:        inst.Bounds,
		Zoom:          inst.EffectiveZoom(),
		Positioned:    inst.Positioned,
		MinBounds:     def.MinBounds,
		Visible:       inst.Visible,
	}, true
}

func (s *Service) find(id string) *component.Instance {
	for i := range s.instances {
		if s.instances[i].ID == id {
			return &s.instances[i]
		}
	}
	return nil
}

// newInstanceID 生成 "<组件id>-<序号>"，跳过已占用的编号。
func (s *Service) newInstanceID(componentID string) string {
	used := make(map[string]bool, len(s.instances))
	for _, inst := range s.instances {
		used[inst.ID] = true
	}
	for n := 1; ; n++ {
		id := fmt.Sprintf("%s-%d", componentID, n)
		if !used[id] {
			return id
		}
	}
}
