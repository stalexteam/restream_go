package api

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"restream_go/internal/control"
	"restream_go/internal/media"
	"restream_go/internal/settings"
)

type resultReply struct {
	Type   string            `json:"type"`
	OK     bool              `json:"ok"`
	Errors map[string]string `json:"errors"`
}

type pongReply struct {
	Type string `json:"type"`
	T    any    `json:"t"`
}

type suggestionsReply struct {
	Type    string               `json:"type"`
	Field   any                  `json:"field"`
	Prefix  string               `json:"prefix"`
	Entries []media.SuggestEntry `json:"entries"`
	Status  any                  `json:"status"`
}

type uploadCheckReply struct {
	Type  string  `json:"type"`
	OK    bool    `json:"ok"`
	Error *string `json:"error"`
}

type settingsSavedReply struct {
	Type string `json:"type"`
	OK   bool   `json:"ok"`
}

type settingsFailedReply struct {
	Type   string            `json:"type"`
	OK     bool              `json:"ok"`
	Errors map[string]string `json:"errors"`
}

// handleCommand — розбір і диспатч однієї команди дашборда.
func (s *Server) handleCommand(c *wsConn, payload []byte) {
	if !json.Valid(payload) {
		log.Printf("dashboard: malformed /ws command payload")
		return
	}
	message, err := control.Loads(payload)
	if err != nil {
		message = control.NewDict()
	}
	raw, _ := message.Get("command")
	command, _ := raw.(string)

	switch command {
	case "ping":
		t, _ := message.Get("t")
		s.reply(c, pongReply{Type: "pong", T: t})
	case "register_source":
		obsID := stringOnly(message, "obs_id")
		s.d.Hub.MarkSource(c)
		s.d.Manager.ReportOBSSession(obsID)
		if s.d.Manager.IsSessionHalted(obsID) {
			s.reply(c, controlMessage{Type: "control", Action: "stop_streaming"})
		}
	case "stop_broadcast":
		s.d.Manager.OnManualStop()
	case "halt":
		s.d.Manager.OnDashboardHalt()
	case "obs_streaming_started":
		s.d.Manager.ReportOBSSession(stringOnly(message, "obs_id"))
		s.d.Manager.OnOBSStreamingStarted()
	case "get_settings":
		s.sendSettings(c)
	case "save_settings":
		s.saveSettings(c, message)
	case "set_obs_widget_bitrate":
		s.d.Manager.SetOBSWidgetShowBitrate(truthy(message.GetOr("value", nil)))
	case "enable_platform":
		if name, ok := isString(message, "name"); ok {
			s.d.Manager.EnablePlatform(name)
		}
	case "disable_platform":
		if name, ok := isString(message, "name"); ok {
			s.d.Manager.DisablePlatform(name)
		}
	case "add_platform":
		s.addPlatform(c, message)
	case "update_platform":
		s.updatePlatform(c, message)
	case "remove_platform":
		if name, ok := isString(message, "name"); ok {
			s.d.Manager.RemovePlatform(name)
		}
		s.sendSettings(c)
	case "add_source":
		s.addSource(c, message)
	case "update_source":
		s.updateSource(c, message)
	case "remove_source":
		s.removeSource(c, message)
	case "add_group":
		s.addGroup(c, message)
	case "update_group":
		gid, ok := isString(message, "id")
		if name := stripped(message, "name"); ok && name != "" {
			s.d.Manager.UpdateGroup(gid, name)
		}
		s.sendSettings(c)
	case "remove_group":
		if gid, ok := isString(message, "id"); ok {
			s.d.Manager.RemoveGroup(gid)
		}
		s.sendSettings(c)
	case "enable_group":
		if gid, ok := isString(message, "id"); ok {
			s.d.Manager.SetGroupEnabled(gid, true)
		}
	case "disable_group":
		if gid, ok := isString(message, "id"); ok {
			s.d.Manager.SetGroupEnabled(gid, false)
		}
	case "list_files":
		s.sendListing(c, orEmpty(message, "path"))
	case "make_dir":
		parent := orEmpty(message, "path")
		s.replyFiles(c, s.d.Media.CreateDir(parent, orEmpty(message, "name")))
		s.sendListing(c, parent)
	case "rename_path":
		s.renamePath(c, message)
	case "delete_path":
		s.deletePath(c, message)
	case "complete_path":
		s.completePath(c, message)
	case "check_upload":
		s.checkUpload(c, message)
	case "add_preset":
		s.addPreset(c, message)
	case "update_preset":
		s.updatePreset(c, message)
	case "remove_preset":
		s.removePreset(c, message)
	default:
		log.Printf("dashboard: unknown /ws command: %s", pyRepr(raw))
	}
}

func (s *Server) reply(c *wsConn, message any) {
	// Мертвий сокет тут не діагностується — його зніме hub на наступній дельті.
	_ = c.sendText(pyMarshal(message))
}

func (s *Server) replyPlatform(c *wsConn, ok bool, errors map[string]string) {
	s.reply(c, resultReply{Type: "platform_result", OK: ok, Errors: orEmptyErrors(errors)})
}

func (s *Server) replySource(c *wsConn, ok bool, errors map[string]string) {
	s.reply(c, resultReply{Type: "source_result", OK: ok, Errors: orEmptyErrors(errors)})
}

func (s *Server) replyGroup(c *wsConn, ok bool, errors map[string]string) {
	s.reply(c, resultReply{Type: "group_result", OK: ok, Errors: orEmptyErrors(errors)})
}

func (s *Server) replyPreset(c *wsConn, ok bool, errors map[string]string) {
	s.reply(c, resultReply{Type: "preset_result", OK: ok, Errors: orEmptyErrors(errors)})
}

func (s *Server) replyFiles(c *wsConn, errMsg string) {
	errors := map[string]string{}
	if errMsg != "" {
		errors["_"] = errMsg
	}
	s.reply(c, resultReply{Type: "files_result", OK: errMsg == "", Errors: errors})
}

// --- налаштування ---

func (s *Server) sendSettings(c *wsConn) {
	data, err := settings.LoadEditable(s.d.ConfigPath)
	if err != nil {
		log.Printf("dashboard: could not read settings: %v", err)
		return
	}
	data.Set("sources", dictsToAny(s.d.Manager.SourcesForSettings()))
	data.Set("platforms", dictsToAny(s.d.Manager.PlatformsForSettings()))
	data.Set("platform_groups", dictsToAny(s.d.Manager.GroupsForSettings()))
	data.Set("fallback_presets", dictsToAny(s.d.Manager.PresetsForSettings()))
	s.reply(c, wireMessage{Type: "settings", Data: data})
}

func (s *Server) saveSettings(c *wsConn, message *control.Dict) {
	values, ok := message.GetOr("settings", nil).(*control.Dict)
	if !ok {
		s.reply(c, settingsFailedReply{Type: "settings_saved", OK: false,
			Errors: map[string]string{"_": "malformed settings payload"}})
		return
	}
	if errors := settings.ValidateSystem(values, s.d.BaseDir); len(errors) > 0 {
		s.reply(c, settingsFailedReply{Type: "settings_saved", OK: false, Errors: errors})
		return
	}

	// Тайминги MediaMTX фіксуються ДО apply_settings, яке оновлює in-memory config.
	oldConnect, _ := s.d.Manager.ConfigValue("connect_timeout_ms")
	oldRead, _ := s.d.Manager.ConfigValue("read_timeout_ms")

	s.d.Manager.ApplySettings(control.SystemSettings{
		ConnectTimeoutMS: pyIntOf(values.GetOr("connect_timeout_ms", nil)),
		ReadTimeoutMS:    pyIntOf(values.GetOr("read_timeout_ms", nil)),
		OfflineTimeoutS:  pyIntOf(values.GetOr("offline_timeout_sec", nil)),
		ICMPPing:         truthy(values.GetOr("icmp_ping", false)),
	})
	log.Printf("dashboard: system settings saved and applied")
	s.reply(c, settingsSavedReply{Type: "settings_saved", OK: true})

	newConnect := pyIntOf(values.GetOr("connect_timeout_ms", nil))
	newRead := pyIntOf(values.GetOr("read_timeout_ms", nil))
	if newConnect == pyIntOf(oldConnect) && newRead == pyIntOf(oldRead) {
		return
	}
	broadcasting := false
	for _, p := range s.d.Manager.Status().Platforms {
		if p.State != "OFFLINE" {
			broadcasting = true
		}
	}
	if broadcasting {
		log.Printf("dashboard: applying connect/read timeout while broadcasting -- " +
			"restarting MediaMTX ends the current broadcast")
	}
	if s.d.RestartMediaMTX == nil {
		return
	}
	if err := s.d.RestartMediaMTX(); err != nil {
		log.Printf("dashboard: failed to restart mediamtx after a timeout change: %v", err)
	}
}

// --- source / платформа / група ---

func (s *Server) addSource(c *wsConn, message *control.Dict) {
	name := stripped(message, "name")
	stype := orDefault(message, "type", "rtmp")
	if errors := settings.ValidateSourceName(name, stype, s.d.Manager.SourceNames()); len(errors) > 0 {
		s.replySource(c, false, errors)
		return
	}
	s.d.Manager.AddSource(name, stype, 1)
	s.replySource(c, true, nil)
	s.sendSettings(c)
}

func (s *Server) updateSource(c *wsConn, message *control.Dict) {
	old, isStr := isString(message, "name")
	newName := stripped(message, "new_name")
	stype := orDefault(message, "type", "rtmp")
	audioTracks := message.GetOr("audio_tracks", int64(1))
	vodTrack := truthy(message.GetOr("vod_track", false))
	enhanced := truthy(message.GetOr("enhanced_broadcasting", false))
	videoTracks := message.GetOr("video_tracks", int64(0))

	names := s.d.Manager.SourceNames()
	if !isStr || !contains(names, old) {
		s.replySource(c, false, map[string]string{"_": "unknown source"})
		return
	}
	free := without(names, old)
	if errors := settings.ValidateSource(newName, stype, audioTracks, free, enhanced, videoTracks); len(errors) > 0 {
		s.replySource(c, false, errors)
		return
	}
	if errMsg := s.d.Manager.UpdateSource(old, newName, stype, pyIntOf(audioTracks),
		vodTrack, enhanced, pyIntOf(videoTracks)); errMsg != "" {
		s.replySource(c, false, map[string]string{"_": errMsg})
		return
	}
	s.replySource(c, true, nil)
	s.sendSettings(c)
}

func (s *Server) removeSource(c *wsConn, message *control.Dict) {
	if name, ok := isString(message, "name"); ok {
		if referencing := s.d.Manager.PlatformsReferencingSource(name); len(referencing) > 0 {
			s.replySource(c, false, map[string]string{
				"_": "cannot remove: used by platform(s): " + strings.Join(referencing, ", ")})
			return
		}
		s.d.Manager.RemoveSource(name)
	}
	s.replySource(c, true, nil)
	s.sendSettings(c)
}

func (s *Server) addPlatform(c *wsConn, message *control.Dict) {
	name := stripped(message, "name")
	ptype := orDefault(message, "type", "rtmp")
	if errors := settings.ValidatePlatformName(name, ptype, s.d.Manager.PlatformNames()); len(errors) > 0 {
		s.replyPlatform(c, false, errors)
		return
	}
	s.d.Manager.AddPlatform(name, ptype)
	s.replyPlatform(c, true, nil)
	s.sendSettings(c)
}

func (s *Server) updatePlatform(c *wsConn, message *control.Dict) {
	old, isStr := isString(message, "name")
	names := s.d.Manager.PlatformNames()
	if !isStr || !contains(names, old) {
		s.replyPlatform(c, false, map[string]string{"_": "unknown platform"})
		return
	}
	newName := strings.TrimSpace(orDefault(message, "new_name", old))
	if newName == "" {
		newName = old
	}
	ptype := orDefault(message, "type", "rtmp")
	vodTrack := truthy(message.GetOr("vod_track", false))
	server := stripped(message, "server")
	key := stripped(message, "key")
	streamID := stripped(message, "streamid")
	passphrase := stripped(message, "passphrase")
	source := stripped(message, "source")
	group := stripped(message, "group")
	backupPreset := stripped(message, "backup_preset")
	audio := pyIntDefault(message.GetOr("audio", int64(0)), 0)
	audioVOD := pyIntDefault(message.GetOr("audio_vod", int64(1)), 1)
	// -1 = уся драбина source-а (EB passthrough).
	video := pyIntDefault(message.GetOr("video", int64(0)), 0)
	audioMap, hasMap := message.GetOr("audio_map", nil).([]any)

	sourcesInfo := map[string]settings.SourceInfo{}
	for _, item := range s.d.Manager.SourcesForSettings() {
		info := settings.SourceInfo{
			Type:                 pyText(item.GetOr("type", "")),
			AudioTracks:          pyIntOf(item.GetOr("audio_tracks", nil)),
			EnhancedBroadcasting: truthy(item.GetOr("enhanced_broadcasting", false)),
			VideoTracks:          1,
		}
		if info.EnhancedBroadcasting {
			labels, _ := item.GetOr("video_track_labels", nil).([]any)
			info.VideoTracks = len(labels)
		}
		sourcesInfo[pyText(item.GetOr("name", ""))] = info
	}

	var mapArg any
	if hasMap {
		mapArg = audioMap
	}
	errors := settings.ValidatePlatform(newName, ptype, vodTrack, server, key, streamID, passphrase,
		source, int64(audio), int64(audioVOD), mapArg, backupPreset, without(names, old),
		sourcesInfo, s.d.Manager.PresetIDs(), int64(video))
	if len(errors) > 0 {
		s.replyPlatform(c, false, errors)
		return
	}

	fields := control.PlatformFields{
		NewName:      newName,
		Type:         &ptype,
		VODTrack:     &vodTrack,
		Server:       &server,
		Key:          &key,
		StreamID:     &streamID,
		Passphrase:   &passphrase,
		Source:       &source,
		BackupPreset: &backupPreset,
		Audio:        &audio,
		AudioVOD:     &audioVOD,
		Video:        &video,
	}
	if group != "" {
		fields.Group = &group
	}
	if hasMap {
		fields.AudioMap = &audioMap
	}
	s.d.Manager.UpdatePlatform(old, fields)
	// Відомі обмеження площадок — лише toast, збереження не блокують.
	for _, text := range settings.PlatformLimitationWarnings(newName, ptype, vodTrack, server, mapArg, int64(video)) {
		s.d.Hub.PushEvent("warning", text)
	}
	s.replyPlatform(c, true, nil)
	s.sendSettings(c)
}

func (s *Server) addGroup(c *wsConn, message *control.Dict) {
	name := stripped(message, "name")
	existing := []string{}
	for _, g := range s.d.Manager.GroupsForSettings() {
		existing = append(existing, pyText(g.GetOr("name", "")))
	}
	if errors := settings.ValidateGroupName(name, existing); len(errors) > 0 {
		s.replyGroup(c, false, errors)
		return
	}
	s.d.Manager.AddGroup(name)
	s.replyGroup(c, true, nil)
	s.sendSettings(c)
}

// --- матеріал заглушки в backup/ ---

func (s *Server) sendListing(c *wsConn, rel string) {
	listing, ok := s.d.Media.ListDir(rel)
	if !ok && rel != "" {
		listing, ok = s.d.Media.ListDir("")
	}
	var data any
	if ok {
		data = listing
	}
	s.reply(c, wireMessage{Type: "files", Data: data})
}

// referencedBy — текст відмови, якщо шлях тримає якийсь пресет.
func (s *Server) referencedBy(rel string) string {
	used := s.d.Manager.PresetsReferencingPath(rel)
	if len(used) == 0 {
		return ""
	}
	return "used by fallback preset(s): " + strings.Join(used, ", ")
}

func (s *Server) renamePath(c *wsConn, message *control.Dict) {
	rel := orEmpty(message, "path")
	// Перейменування ламає посилання пресета так само, як видалення.
	errMsg := s.referencedBy(rel)
	if errMsg == "" {
		errMsg = s.d.Media.Rename(rel, orEmpty(message, "new_name"))
	}
	s.replyFiles(c, errMsg)
	s.sendListing(c, media.ParentOf(rel))
}

func (s *Server) deletePath(c *wsConn, message *control.Dict) {
	rel := orEmpty(message, "path")
	errMsg := s.referencedBy(rel)
	if errMsg == "" {
		errMsg = s.d.Media.Delete(rel)
	}
	s.replyFiles(c, errMsg)
	s.sendListing(c, media.ParentOf(rel))
}

func (s *Server) completePath(c *wsConn, message *control.Dict) {
	field, _ := message.Get("field")
	prefix := orEmpty(message, "prefix")
	result, ok := s.d.Media.Suggest(prefix, truthy(message.GetOr("dirs_only", nil)), 8)
	reply := suggestionsReply{Type: "path_suggestions", Field: field, Prefix: prefix,
		Entries: []media.SuggestEntry{}}
	if ok {
		reply.Entries = result.Entries
		reply.Status = result.Status
	}
	s.reply(c, reply)
}

func (s *Server) checkUpload(c *wsConn, message *control.Dict) {
	size := int64(0)
	switch n := message.GetOr("size", nil).(type) {
	case int64:
		size = n
	case float64:
		size = int64(n)
	}
	_, _, errMsg := s.d.Media.PrepareUpload(orEmpty(message, "path"), orEmpty(message, "name"), size)
	reply := uploadCheckReply{Type: "upload_check", OK: errMsg == ""}
	if errMsg != "" {
		reply.Error = &errMsg
	}
	s.reply(c, reply)
}

// --- fallback-пресети ---

func presetFiles(message *control.Dict) settings.PresetFiles {
	ptype := pyText(message.GetOr("type", nil))
	if ptype != "sequence" && ptype != "folder" {
		ptype = "sequence"
	}
	return settings.PresetFiles{
		Type:          ptype,
		StartFile:     stripped(message, "start_file"),
		LoopFile:      stripped(message, "loop_file"),
		EndFile:       stripped(message, "end_file"),
		Folder:        stripped(message, "folder"),
		SeparatorFile: stripped(message, "separator_file"),
	}
}

func controlPreset(files settings.PresetFiles) control.PresetFiles {
	return control.PresetFiles{
		Type:          files.Type,
		StartFile:     files.StartFile,
		LoopFile:      files.LoopFile,
		EndFile:       files.EndFile,
		Folder:        files.Folder,
		SeparatorFile: files.SeparatorFile,
	}
}

func (s *Server) addPreset(c *wsConn, message *control.Dict) {
	name := stripped(message, "name")
	files := presetFiles(message)
	if errors := settings.ValidatePreset(name, files, s.d.Manager.PresetNames(""), s.d.BaseDir); len(errors) > 0 {
		s.replyPreset(c, false, errors)
		return
	}
	s.d.Manager.AddPreset(name, controlPreset(files))
	s.replyPreset(c, true, nil)
	s.sendSettings(c)
}

func (s *Server) updatePreset(c *wsConn, message *control.Dict) {
	presetID, isStr := isString(message, "id")
	name := stripped(message, "name")
	files := presetFiles(message)
	if !isStr || !contains(s.d.Manager.PresetIDs(), presetID) {
		s.replyPreset(c, false, map[string]string{"_": "unknown fallback preset"})
		return
	}
	if errors := settings.ValidatePreset(name, files, s.d.Manager.PresetNames(presetID), s.d.BaseDir); len(errors) > 0 {
		s.replyPreset(c, false, errors)
		return
	}
	s.d.Manager.UpdatePreset(presetID, name, controlPreset(files))
	s.replyPreset(c, true, nil)
	s.sendSettings(c)
}

func (s *Server) removePreset(c *wsConn, message *control.Dict) {
	if presetID, ok := isString(message, "id"); ok {
		if referencing := s.d.Manager.PlatformsReferencingPreset(presetID); len(referencing) > 0 {
			s.replyPreset(c, false, map[string]string{
				"_": "cannot remove: used by platform(s): " + strings.Join(referencing, ", ")})
			return
		}
		s.d.Manager.RemovePreset(presetID)
	}
	s.replyPreset(c, true, nil)
	s.sendSettings(c)
}

// --- читання полів повідомлення ---

// isString — python `isinstance(value, str)`.
func isString(d *control.Dict, key string) (string, bool) {
	v, _ := d.Get(key)
	s, ok := v.(string)
	return s, ok
}

// stringOnly — рядкове значення або "" (нерядкові поля Go-контракт не приймає).
func stringOnly(d *control.Dict, key string) string {
	s, _ := isString(d, key)
	return s
}

// orEmpty — python `message.get(key) or ""`.
func orEmpty(d *control.Dict, key string) string { return orDefault(d, key, "") }

// orDefault — python `message.get(key) or def`.
func orDefault(d *control.Dict, key, def string) string {
	v, _ := d.Get(key)
	if !truthy(v) {
		return def
	}
	return pyText(v)
}

// stripped — python `(message.get(key) or "").strip`.
func stripped(d *control.Dict, key string) string { return strings.TrimSpace(orEmpty(d, key)) }

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case int64:
		return x != 0
	case float64:
		return x != 0
	case string:
		return x != ""
	case []any:
		return len(x) != 0
	case *control.Dict:
		return x.Len() != 0
	default:
		return true
	}
}

// pyIntOf — python int для значень, які дає control.Loads; інше = 0.
func pyIntOf(v any) int { return pyIntDefault(v, 0) }

// pyIntDefault — python `try: int(v) except (TypeError, ValueError): def`.
func pyIntDefault(v any, def int) int {
	switch n := v.(type) {
	case int64:
		return int(n)
	case float64:
		return int(n)
	case bool:
		if n {
			return 1
		}
		return 0
	}
	return def
}

// pyRepr — python %r для значення команди.
func pyRepr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return "'" + x + "'"
	default:
		return fmt.Sprint(x)
	}
}

func orEmptyErrors(errors map[string]string) map[string]string {
	if errors == nil {
		return map[string]string{}
	}
	return errors
}

func contains(list []string, target string) bool {
	for _, v := range list {
		if v == target {
			return true
		}
	}
	return false
}

func without(list []string, exclude string) []string {
	out := []string{}
	for _, v := range list {
		if v != exclude {
			out = append(out, v)
		}
	}
	return out
}

func dictsToAny(items []*control.Dict) []any {
	out := make([]any, len(items))
	for i, item := range items {
		out[i] = item
	}
	return out
}
