package control

import (
	"log"
	"strings"
)

// --- групи платформ (спільний AND-гейт) ---

// AddGroup — нова група; повертає її id.
func (m *Manager) AddGroup(name string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	taken := map[string]bool{}
	for _, g := range m.groups {
		taken[pyStr(g.GetOr("id", ""))] = true
	}
	gid := slug(name, taken, "group")
	m.groups = append(m.groups, D(
		"id", gid, "name", strings.TrimSpace(name), "is_default", false, "enabled", true))
	log.Printf("added platform group %s (id=%s)", name, gid)
	m.persistLocked()
	return gid
}

// UpdateGroup — перейменування групи.
func (m *Manager) UpdateGroup(groupID, name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, g := range m.groups {
		if pyStr(g.GetOr("id", "")) == groupID {
			g.Set("name", strings.TrimSpace(name))
			m.persistLocked()
			return
		}
	}
}

// RemoveGroup — видалення (дефолтна незнищенна); платформи переїжджають у
// дефолтну.
func (m *Manager) RemoveGroup(groupID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var group *Dict
	for _, g := range m.groups {
		if pyStr(g.GetOr("id", "")) == groupID {
			group = g
			break
		}
	}
	if group == nil || pyTruthy(group.GetOr("is_default", false)) {
		return
	}
	def := m.defaultGroupLocked()
	defID := pyStr(def.GetOr("id", ""))
	for _, e := range m.platforms.Values() {
		if e.groupID == groupID {
			e.groupID = defID
			e.cfg.Set("group", defID)
			e.gate = pyTruthy(def.GetOr("enabled", false))
			e.rt.SetGroup(defID, e.gate)
		}
	}
	kept := m.groups[:0]
	for _, g := range m.groups {
		if pyStr(g.GetOr("id", "")) != groupID {
			kept = append(kept, g)
		}
	}
	m.groups = kept
	log.Printf("removed platform group %s (its platforms moved to the default group)", groupID)
	m.persistLocked()
}

// SetGroupEnabled — гейт групи.
func (m *Manager) SetGroupEnabled(groupID string, active bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var group *Dict
	for _, g := range m.groups {
		if pyStr(g.GetOr("id", "")) == groupID {
			group = g
			break
		}
	}
	if group == nil || pyTruthy(group.GetOr("enabled", false)) == active {
		return
	}
	group.Set("enabled", active)
	for _, e := range m.platforms.Values() {
		if e.groupID == groupID {
			if active {
				m.gateFallbackLocked(e)
			}
			e.gate = active
			e.rt.SetGate(active)
		}
	}
	log.Printf("platform group %s %s", groupID, enabledWord(active))
	m.persistLocked()
}

func enabledWord(active bool) string {
	if active {
		return "enabled"
	}
	return "muted"
}

// --- fallback-пресети ---

func (m *Manager) defaultPresetLocked() *Dict {
	for _, p := range m.presets {
		if pyTruthy(p.GetOr("is_default", false)) {
			return p
		}
	}
	return m.presets[0]
}

func (m *Manager) defaultPresetIDLocked() string {
	return pyStrOr(m.defaultPresetLocked().GetOr("id", ""), defaultPresetID)
}

// DefaultPresetID — id дефолтного пресета.
func (m *Manager) DefaultPresetID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.defaultPresetIDLocked()
}

// presetByIDLocked — невідомий id -> дефолтний.
func (m *Manager) presetByIDLocked(presetID string) *Dict {
	for _, p := range m.presets {
		if pyStr(p.GetOr("id", "")) == presetID {
			return p
		}
	}
	return m.defaultPresetLocked()
}

func (m *Manager) resolvePresetIDLocked(cfg *Dict) string {
	pid := pyStr(cfg.GetOr("backup_preset", ""))
	return pyStrOr(m.presetByIDLocked(pid).GetOr("id", ""), m.defaultPresetIDLocked())
}

// presetText — значення поля пресета після strip (`(value or "").strip`).
func presetText(value any) string {
	return strings.TrimSpace(pyStr(pyOr(value, "")))
}

// resolvePresetPath — поле пресета як шлях усередині media/ ("" — не задано
// або веде назовні).
func (m *Manager) resolvePresetPath(value any) string {
	text := presetText(value)
	if text == "" {
		return ""
	}
	return resolveMediaPath(text, m.baseDir)
}

// segmentsForPreset — сегменти пресета з вирішеними шляхами.
func (m *Manager) segmentsForPreset(presetID string) Segments {
	p := m.presetByIDLocked(presetID)
	resolved := m.resolvePresetPath
	kind := pyStrOr(p.GetOr("type", "sequence"), "sequence")
	seg := Segments{
		Kind:  kind,
		Start: resolved(p.GetOr("start_file", nil)),
		End:   resolved(p.GetOr("end_file", nil)),
	}
	if kind == "folder" {
		folder := resolved(p.GetOr("folder", nil))
		seg.Separator = resolved(p.GetOr("separator_file", nil))
		if folder != "" && isDir(folder) {
			seg.FolderFiles = listFolderPaths(folder)
		}
	} else {
		seg.Loop = resolved(p.GetOr("loop_file", nil))
	}
	return seg
}

// SegmentsForPlatform — сегменти пресета, на який посилається платформа.
func (m *Manager) SegmentsForPlatform(cfg *Dict) Segments {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.segmentsForPreset(pyStr(cfg.GetOr("backup_preset", "")))
}

// PresetsForSettings — форма вкладки Settings; дефолтний пресет перший.
func (m *Manager) PresetsForSettings() []*Dict {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*Dict{}
	for _, p := range defaultFirst(m.presets) {
		out = append(out, D(
			"id", p.GetOr("id", nil),
			"name", p.GetOr("name", ""),
			"is_default", pyTruthy(p.GetOr("is_default", false)),
			"type", p.GetOr("type", "sequence"),
			"start_file", p.GetOr("start_file", ""),
			"loop_file", p.GetOr("loop_file", ""),
			"end_file", p.GetOr("end_file", ""),
			"folder", p.GetOr("folder", ""),
			"separator_file", p.GetOr("separator_file", ""),
		))
	}
	return out
}

// PresetNames — імена пресетів (для валідації унікальності).
func (m *Manager) PresetNames(excludeID string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []string{}
	for _, p := range m.presets {
		if pyStr(p.GetOr("id", "")) != excludeID {
			out = append(out, pyStr(p.GetOr("name", "")))
		}
	}
	return out
}

// PresetIDs — усі id пресетів.
func (m *Manager) PresetIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []string{}
	for _, p := range m.presets {
		out = append(out, pyStr(p.GetOr("id", "")))
	}
	return out
}

var presetPathFields = []string{"start_file", "loop_file", "end_file", "folder", "separator_file"}

// PresetsReferencingPath — guard видалення/перейменування у файловому
// менеджері: папка «накриває» те, що в ній.
func (m *Manager) PresetsReferencingPath(rel string) []string {
	target := strings.Trim(strings.ReplaceAll(rel, "\\", "/"), "/")
	if target == "" {
		return []string{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	names := []string{}
	for _, preset := range m.presets {
		for _, field := range presetPathFields {
			value := strings.Trim(strings.TrimSpace(
				strings.ReplaceAll(pyStr(pyOr(preset.GetOr(field, nil), "")), "\\", "/")), "/")
			if value != "" && (value == target || strings.HasPrefix(value, target+"/")) {
				names = append(names, pyStr(preset.GetOr("name", "")))
				break
			}
		}
	}
	return names
}

// PlatformsReferencingPreset — платформи, у яких цей пресет активний.
func (m *Manager) PlatformsReferencingPreset(presetID string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []string{}
	for _, e := range m.platforms.Values() {
		if m.resolvePresetIDLocked(e.cfg) == presetID {
			out = append(out, e.name)
		}
	}
	return out
}

// PresetFiles — файлові поля пресета з форми Settings.
type PresetFiles struct {
	Type          string
	StartFile     string
	LoopFile      string
	EndFile       string
	Folder        string
	SeparatorFile string
}

// presetFields — нормалізація полів форми.
func presetFields(files PresetFiles) *Dict {
	ptype := files.Type
	if ptype != "sequence" && ptype != "folder" {
		ptype = "sequence"
	}
	return D(
		"type", ptype,
		"start_file", strings.TrimSpace(files.StartFile),
		"loop_file", strings.TrimSpace(files.LoopFile),
		"end_file", strings.TrimSpace(files.EndFile),
		"folder", strings.TrimSpace(files.Folder),
		"separator_file", strings.TrimSpace(files.SeparatorFile),
	)
}

// AddPreset — новий пресет; повертає його id.
func (m *Manager) AddPreset(name string, files PresetFiles) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	taken := map[string]bool{}
	for _, p := range m.presets {
		taken[pyStr(p.GetOr("id", ""))] = true
	}
	pid := slug(name, taken, "preset")
	preset := D("id", pid, "name", strings.TrimSpace(name), "is_default", false)
	fields := presetFields(files)
	for _, key := range fields.Keys() {
		preset.Set(key, fields.GetOr(key, nil))
	}
	m.presets = append(m.presets, preset)
	log.Printf("added fallback preset %s (id=%s, type=%s)", name, pid, files.Type)
	m.persistLocked()
	return pid
}

// UpdatePreset — нові файли пресета + перебудова препарерів залежних платформ
func (m *Manager) UpdatePreset(presetID, name string, files PresetFiles) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var preset *Dict
	for _, p := range m.presets {
		if pyStr(p.GetOr("id", "")) == presetID {
			preset = p
			break
		}
	}
	if preset == nil {
		return
	}
	preset.Set("name", strings.TrimSpace(name))
	fields := presetFields(files)
	for _, key := range fields.Keys() {
		preset.Set(key, fields.GetOr(key, nil))
	}
	log.Printf("updated fallback preset %s (id=%s, type=%s)", name, presetID,
		pyStr(preset.GetOr("type", "")))
	for _, e := range m.platforms.Values() {
		if m.resolvePresetIDLocked(e.cfg) == presetID {
			m.applyPresetLocked(e, m.resolvePresetIDLocked(e.cfg), m.segmentsForPreset(presetID))
		}
	}
	m.persistLocked()
}

// RemovePreset — видалення (дефолтний незнищенний).
func (m *Manager) RemovePreset(presetID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	preset := m.presetByIDLocked(presetID)
	if pyTruthy(preset.GetOr("is_default", false)) {
		return
	}
	kept := m.presets[:0]
	for _, p := range m.presets {
		if pyStr(p.GetOr("id", "")) != presetID {
			kept = append(kept, p)
		}
	}
	m.presets = kept
	log.Printf("removed fallback preset %s", presetID)
	m.persistLocked()
}

// defaultFirst — python `sorted(items, key=lambda x: not is_default)`: стабільне
// сортування, дефолтний елемент перший.
func defaultFirst(items []*Dict) []*Dict {
	out := make([]*Dict, 0, len(items))
	for _, item := range items {
		if pyTruthy(item.GetOr("is_default", false)) {
			out = append(out, item)
		}
	}
	for _, item := range items {
		if !pyTruthy(item.GetOr("is_default", false)) {
			out = append(out, item)
		}
	}
	return out
}
