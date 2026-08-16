/*
 * Сторінка налаштувань (/config) -- усе, що раніше було вкладкою Settings:
 * sources/platforms/групи/fallback-пресети, System-блок і файловий
 * менеджер backup/. Свого стану ефіру не малює; знімок тримає лише тому,
 * що Apply має спитати підтвердження, якщо рестарт MediaMTX обірве ефір.
 */
(() => {
  "use strict";

  const send = RS.send;
  const wsLive = RS.wsLive;
  const formatDuration = RS.formatDuration;
  const showErrorsInto = RS.showErrorsInto;
  const setNameWithStar = RS.setNameWithStar;
  const typeLabel = RS.typeLabel;
  const enqueueToast = RS.toast;
  const MAX_TRACK_SLOTS = RS.MAX_TRACK_SLOTS;

  let settingsData = null;     // get_settings payload (system fields + sources/platforms/platform_groups/fallback_presets)
  let loadedSettings = null;   // to detect timeout changes on Apply
  // after a successful create, the edit widget auto-opens once the refreshed
  // settings arrive.
  let pendingEdit = null;      // {kind: "source"|"platform", name}
  // create modal context
  let createKind = null;       // "source" | "platform" | null
  // source modal context
  let smodalEditingName = null;
  let smodalIngestShown = false;
  // platform modal context
  let pmodalEditingName = null;
  let pmodalSecretsShown = false;
  // group modal context
  let gmodalMode = null;       // "add" | "edit" | null
  let gmodalEditingId = null;
  // fallback preset modal context
  let presetMode = null;       // "add" | "edit" | null
  let presetEditingId = null;

  // Знімок стану приходить сюди так само, як у док (hub розсилає всім),
  // але ця сторінка його НЕ малює -- потрібні рівно два поля, і лише
  // щоб попередити про обрив ефіру при зміні таймаутів MediaMTX.
  const state = {};
  function platforms() { return state.platforms || []; }
  function session() { return state.session || { state: "OFFLINE", fallback_deadline: null }; }

  const els = {
    sourcesList: document.getElementById("sources-list"),
    platformsList: document.getElementById("platforms-list"),
    groupsList: document.getElementById("groups-list"),
    btnAddSource: document.getElementById("btn-add-source"),
    btnAddPlatform: document.getElementById("btn-add-platform"),
    btnAddGroup: document.getElementById("btn-add-group"),
    fieldConnectTimeout: document.getElementById("field-connect-timeout"),
    fieldReadTimeout: document.getElementById("field-read-timeout"),
    fieldOfflineTimeout: document.getElementById("field-offline-timeout"),
    fieldIcmpPing: document.getElementById("field-icmp-ping"),
    fieldObsWidgetBitrate: document.getElementById("field-obs-widget-bitrate"),
    settingsErrors: document.getElementById("settings-errors"),
    btnApply: document.getElementById("btn-apply"),
    createModal: document.getElementById("create-modal"),
    createTitle: document.getElementById("create-title"),
    createName: document.getElementById("create-name"),
    createType: document.getElementById("create-type"),
    createErrors: document.getElementById("create-errors"),
    createOk: document.getElementById("create-ok"),
    createCancel: document.getElementById("create-cancel"),
    smodal: document.getElementById("source-modal"),
    smodalTitle: document.getElementById("smodal-title"),
    smodalName: document.getElementById("smodal-name"),
    smodalType: document.getElementById("smodal-type"),
    smodalVodTrackField: document.getElementById("smodal-vod-track-field"),
    smodalVodTrack: document.getElementById("smodal-vod-track"),
    smodalEbField: document.getElementById("smodal-eb-field"),
    smodalEb: document.getElementById("smodal-eb"),
    smodalVideoTracksField: document.getElementById("smodal-video-tracks-field"),
    smodalVideoTracks: document.getElementById("smodal-video-tracks"),
    smodalTracksField: document.getElementById("smodal-tracks-field"),
    smodalTracks: document.getElementById("smodal-tracks"),
    smodalTracksValue: document.getElementById("smodal-tracks-value"),
    smodalIngestRtmp: document.getElementById("smodal-ingest-rtmp"),
    smodalIngestSrt: document.getElementById("smodal-ingest-srt"),
    smodalIngestServer: document.getElementById("smodal-ingest-server"),
    smodalIngestKey: document.getElementById("smodal-ingest-key"),
    smodalIngestUrl: document.getElementById("smodal-ingest-url"),
    smodalIngestShowRtmp: document.getElementById("smodal-ingest-show-rtmp"),
    smodalIngestShowSrt: document.getElementById("smodal-ingest-show-srt"),
    smodalErrors: document.getElementById("smodal-errors"),
    smodalOk: document.getElementById("smodal-ok"),
    smodalCancel: document.getElementById("smodal-cancel"),
    pmodal: document.getElementById("platform-modal"),
    pmodalTitle: document.getElementById("pmodal-title"),
    pmodalName: document.getElementById("pmodal-name"),
    pmodalType: document.getElementById("pmodal-type"),
    pmodalVodTrackField: document.getElementById("pmodal-vod-track-field"),
    pmodalVodTrack: document.getElementById("pmodal-vod-track"),
    pmodalGroup: document.getElementById("pmodal-group"),
    pmodalPreset: document.getElementById("pmodal-preset"),
    pmodalSource: document.getElementById("pmodal-source"),
    pmodalAudio: document.getElementById("pmodal-audio"),
    pmodalAudioLabel: document.getElementById("pmodal-audio-label"),
    pmodalAudioVod: document.getElementById("pmodal-audio-vod"),
    pmodalAudioVodField: document.getElementById("pmodal-audio-vod-field"),
    pmodalStreamGrid: document.getElementById("pmodal-stream-grid"),
    pmodalAudioField: document.getElementById("pmodal-audio-field"),
    pmodalTrackMap: document.getElementById("pmodal-track-map"),
    pmodalCloneFill: document.getElementById("pmodal-clone-fill"),
    pmodalCredsRtmp: document.getElementById("pmodal-creds-rtmp"),
    pmodalCredsSrt: document.getElementById("pmodal-creds-srt"),
    pmodalServer: document.getElementById("pmodal-server"),
    pmodalKey: document.getElementById("pmodal-key"),
    pmodalSrtServer: document.getElementById("pmodal-srt-server"),
    pmodalStreamid: document.getElementById("pmodal-streamid"),
    pmodalPassphrase: document.getElementById("pmodal-passphrase"),
    pmodalShowRtmp: document.getElementById("pmodal-show-rtmp"),
    pmodalShowSrt: document.getElementById("pmodal-show-srt"),
    pmodalUrlRtmp: document.getElementById("pmodal-url-rtmp"),
    pmodalUrlSrt: document.getElementById("pmodal-url-srt"),
    pmodalErrors: document.getElementById("pmodal-errors"),
    pmodalOk: document.getElementById("pmodal-ok"),
    pmodalCancel: document.getElementById("pmodal-cancel"),
    gmodal: document.getElementById("group-modal"),
    gmodalTitle: document.getElementById("gmodal-title"),
    gmodalName: document.getElementById("gmodal-name"),
    gmodalErrors: document.getElementById("gmodal-errors"),
    gmodalOk: document.getElementById("gmodal-ok"),
    gmodalCancel: document.getElementById("gmodal-cancel"),
    btnAddPreset: document.getElementById("btn-add-preset"),
    presetsList: document.getElementById("presets-list"),
    presetModal: document.getElementById("preset-modal"),
    presetModalTitle: document.getElementById("preset-modal-title"),
    presetName: document.getElementById("preset-name"),
    presetType: document.getElementById("preset-type"),
    presetStart: document.getElementById("preset-start"),
    presetLoop: document.getElementById("preset-loop"),
    presetLoopField: document.getElementById("preset-loop-field"),
    presetFolder: document.getElementById("preset-folder"),
    presetFolderField: document.getElementById("preset-folder-field"),
    presetSeparator: document.getElementById("preset-separator"),
    presetSeparatorField: document.getElementById("preset-separator-field"),
    presetEnd: document.getElementById("preset-end"),
    presetErrors: document.getElementById("preset-errors"),
    presetDelete: document.getElementById("preset-delete"),
    presetOk: document.getElementById("preset-ok"),
    presetCancel: document.getElementById("preset-cancel"),
    presetFiles: document.getElementById("preset-files"),
    previewModal: document.getElementById("preview-modal"),
    previewTitle: document.getElementById("preview-title"),
    previewVideo: document.getElementById("preview-video"),
    previewNote: document.getElementById("preview-note"),
    previewMeta: document.getElementById("preview-meta"),
    previewClose: document.getElementById("preview-close"),
    textModal: document.getElementById("text-modal"),
    textModalTitle: document.getElementById("text-modal-title"),
    textModalLabel: document.getElementById("text-modal-label"),
    textModalInput: document.getElementById("text-modal-input"),
    textModalOk: document.getElementById("text-modal-ok"),
    textModalCancel: document.getElementById("text-modal-cancel"),
    btnFiles: document.getElementById("btn-files"),
    filesModal: document.getElementById("files-modal"),
    fmUp: document.getElementById("fm-up"),
    fmPath: document.getElementById("fm-path"),
    fmNewDir: document.getElementById("fm-newdir"),
    fmRefresh: document.getElementById("fm-refresh"),
    fmDrop: document.getElementById("fm-drop"),
    fmBrowse: document.getElementById("fm-browse"),
    fmFileInput: document.getElementById("fm-file-input"),
    fmRows: document.getElementById("fm-rows"),
    filesErrors: document.getElementById("files-errors"),
    fmClose: document.getElementById("fm-close"),
    pickerModal: document.getElementById("picker-modal"),
    pickerTitle: document.getElementById("picker-title"),
    pickerRows: document.getElementById("picker-rows"),
    pickerUp: document.getElementById("picker-up"),
    pickerPath: document.getElementById("picker-path"),
    pickerOk: document.getElementById("picker-ok"),
    pickerCancel: document.getElementById("picker-cancel"),
    uploadModal: document.getElementById("upload-modal"),
    uploadTitle: document.getElementById("upload-title"),
    uploadName: document.getElementById("upload-name"),
    uploadFill: document.getElementById("upload-fill"),
    uploadPercent: document.getElementById("upload-percent"),
    uploadDetail: document.getElementById("upload-detail"),
    uploadErrors: document.getElementById("upload-errors"),
    uploadAbort: document.getElementById("upload-abort"),
    uploadClose: document.getElementById("upload-close"),
    uploadRetry: document.getElementById("upload-retry"),
    tplItemRow: document.getElementById("item-row-template"),
  };

  // --- settings: system form ---

  function populateSystemForm(data) {
    els.fieldConnectTimeout.value = data.connect_timeout_ms ?? "";
    els.fieldReadTimeout.value = data.read_timeout_ms ?? "";
    els.fieldOfflineTimeout.value = data.offline_timeout_sec ?? "";
    els.fieldIcmpPing.checked = !!data.icmp_ping;
    els.fieldObsWidgetBitrate.checked = !!data.obs_widget_show_bitrate;
  }
  function collectSystem() {
    return {
      connect_timeout_ms: Number(els.fieldConnectTimeout.value),
      read_timeout_ms: Number(els.fieldReadTimeout.value),
      offline_timeout_sec: Number(els.fieldOfflineTimeout.value),
      icmp_ping: els.fieldIcmpPing.checked,
    };
  }
  function hideSettingsMessages() { els.settingsErrors.hidden = true; }
  function handleSettingsSaved(message) {
    if (message.ok) hideSettingsMessages();
    else showErrorsInto(els.settingsErrors, message.errors || {});
  }
  function saveSettings() {
    hideSettingsMessages();
    const values = collectSystem();
    const timeoutsChanged = loadedSettings && (
      Number(loadedSettings.connect_timeout_ms) !== values.connect_timeout_ms ||
      Number(loadedSettings.read_timeout_ms) !== values.read_timeout_ms
    );
    const broadcasting = session().state !== "OFFLINE"
      || platforms().some((p) => p.state && p.state !== "OFFLINE");
    if (timeoutsChanged && broadcasting) {
      if (!confirm("Changing the connect/read timeout restarts MediaMTX and ends the current broadcast. Continue?")) return;
    }
    send({ command: "save_settings", settings: values });
  }

  // --- settings: sources / platforms / groups lists ---

  function makeItemRow(name, starHint, tag, onDelete, onEdit, live) {
    const frag = els.tplItemRow.content.cloneNode(true);
    const row = frag.querySelector(".item-row");
    setNameWithStar(row.querySelector(".item-name"), name, starHint);
    row.querySelector(".item-tag").textContent = tag || "";
    const delBtn = row.querySelector(".item-delete");
    if (onDelete) {
      delBtn.disabled = !live;
      delBtn.addEventListener("click", onDelete);
    } else {
      delBtn.remove();
    }
    const editBtn = row.querySelector(".item-edit");
    editBtn.disabled = !live;
    editBtn.addEventListener("click", onEdit);
    return frag;
  }

  function groupName(gid) {
    const g = ((settingsData && settingsData.platform_groups) || []).find((x) => x.id === gid);
    return g ? g.name : gid;
  }

  function renderSettingsLists() {
    if (!settingsData) return;
    const live = wsLive();

    els.sourcesList.innerHTML = "";
    for (const s of settingsData.sources || []) {
      let tag = typeLabel(s);
      if (s.type === "srt") tag += ` · ${s.audio_tracks} audio track${s.audio_tracks === 1 ? "" : "s"}`;
      els.sourcesList.appendChild(makeItemRow(
        s.name,
        s.is_default ? "Default source" : null,
        tag,
        s.is_default ? null : () => {
          if (confirm(`Delete source "${s.name}"?`)) send({ command: "remove_source", name: s.name });
        },
        () => openSourceModal(s),
        live,
      ));
    }

    els.platformsList.innerHTML = "";
    for (const p of settingsData.platforms || []) {
      const ladder = p.video < 0;
      const from = ladder
        ? `${p.source} (all video tracks)`
        : (p.video ? `${p.source} #${p.video + 1}` : p.source);
      const tag = `${typeLabel(p)}${ladder ? "+EB" : ""} · ${from} · ${groupName(p.group)}`;
      els.platformsList.appendChild(makeItemRow(
        p.name,
        null,
        tag,
        () => {
          if (confirm(`Delete platform "${p.name}"? This stops its stream immediately.`)) {
            send({ command: "remove_platform", name: p.name });
          }
        },
        () => openPlatformModal(p),
        live,
      ));
    }

    els.groupsList.innerHTML = "";
    for (const g of settingsData.platform_groups || []) {
      const members = (settingsData.platforms || []).filter((p) => p.group === g.id).length;
      els.groupsList.appendChild(makeItemRow(
        g.name,
        g.is_default ? "Default group" : null,
        `${members} platform${members === 1 ? "" : "s"}`,
        g.is_default ? null : () => {
          if (confirm(`Delete group "${g.name}"? Its platforms move to the default group.`)) {
            send({ command: "remove_group", id: g.id });
          }
        },
        () => openGroupModal("edit", g),
        live,
      ));
    }
  }

  // --- create modal (source / platform) ---

  const CREATE_TYPES = {
    source: [
      ["rtmp", "RTMP"],
      ["srt", "SRT (multitrack)"],
    ],
    platform: [
      ["rtmp", "RTMP"],
      ["srt", "SRT"],
    ],
  };

  function openCreateModal(kind) {
    createKind = kind;
    els.createTitle.textContent = kind === "source" ? "Add source" : "Add platform";
    els.createName.value = "";
    els.createName.placeholder = kind === "source" ? "e.g. Vertical" : "e.g. Kick";
    els.createType.innerHTML = "";
    for (const [value, label, disabled] of CREATE_TYPES[kind]) {
      const opt = document.createElement("option");
      opt.value = value;
      opt.textContent = label;
      if (disabled) opt.disabled = true;
      els.createType.appendChild(opt);
    }
    els.createErrors.hidden = true;
    els.createModal.hidden = false;
    els.createName.focus();
  }
  function closeCreateModal() {
    els.createModal.hidden = true;
    createKind = null;
  }
  function submitCreate() {
    els.createErrors.hidden = true;
    const name = els.createName.value.trim();
    if (!name) { showErrorsInto(els.createErrors, { name: "name is required" }); return; }
    const type = els.createType.value;
    // Remember the name so its edit widget opens automatically on success.
    pendingEdit = { kind: createKind, name };
    send({ command: createKind === "source" ? "add_source" : "add_platform", name, type });
  }

  // Once the settings refresh that follows a successful create arrives, open
  // the freshly created item's edit widget so it can be configured.
  function maybeOpenPendingEdit() {
    if (!pendingEdit || !settingsData) return;
    const { kind, name } = pendingEdit;
    pendingEdit = null;
    if (kind === "source") {
      const s = (settingsData.sources || []).find((x) => x.name === name);
      if (s) openSourceModal(s);
    } else {
      const p = (settingsData.platforms || []).find((x) => x.name === name);
      if (p) openPlatformModal(p);
    }
  }

  // Routes a source_result/platform_result to whichever widget is waiting:
  // the create modal, the edit modal, or (list-triggered delete) a toast.
  function handleResult(kind, message) {
    if (!els.createModal.hidden && createKind === kind) {
      if (message.ok) closeCreateModal();
      else {
        pendingEdit = null;
        showErrorsInto(els.createErrors, message.errors || {});
      }
      return;
    }
    const modal = kind === "source" ? els.smodal : els.pmodal;
    const errBox = kind === "source" ? els.smodalErrors : els.pmodalErrors;
    if (!modal.hidden) {
      if (message.ok) {
        if (kind === "source") closeSourceModal();
        else closePlatformModal();
      } else {
        showErrorsInto(errBox, message.errors || {});
      }
      return;
    }
    if (!message.ok) {
      const errs = message.errors || {};
      enqueueToast("error", errs._ || Object.values(errs)[0] || "operation failed");
    }
  }

  // --- source modal ---

  function syncSourceModalFields() {
    // Поля йдуть за ВИБРАНИМ типом (усі три ingest-поля завжди приходять у
    // settings, тож перемикання типу показує актуальні значення одразу).
    const isSrt = els.smodalType.value === "srt";
    els.smodalTracksField.hidden = !isSrt;
    els.smodalTracksValue.textContent = els.smodalTracks.value;
    els.smodalVodTrackField.hidden = isSrt;
    els.smodalEbField.hidden = isSrt;
    // Кількість сходинок питаємо лише коли драбина справді ввімкнена.
    els.smodalVideoTracksField.hidden = isSrt || !els.smodalEb.checked;
    els.smodalIngestRtmp.hidden = isSrt;
    els.smodalIngestSrt.hidden = !isSrt;
    renderSourceIngest();
  }
  function renderSourceIngest() {
    const src = currentEditingSource() || {};
    const mask = (v) => (v ? (smodalIngestShown ? v : "••••••••••••") : "–");
    els.smodalIngestServer.textContent = mask(src.ingest_server);
    els.smodalIngestKey.textContent = mask(src.ingest_key);
    els.smodalIngestUrl.textContent = mask(src.ingest_url);
    const label = smodalIngestShown ? "Hide" : "Show";
    els.smodalIngestShowRtmp.textContent = label;
    els.smodalIngestShowSrt.textContent = label;
  }
  function openSourceModal(src) {
    smodalEditingName = src.name;
    smodalIngestShown = false;
    els.smodalTitle.textContent = `Modify source: ${src.name}`;
    els.smodalName.value = src.name;
    els.smodalType.value = src.type;
    els.smodalTracks.value = Math.min(6, Math.max(1, src.audio_tracks || 1));
    els.smodalVodTrack.checked = Boolean(src.vod_track);
    els.smodalEb.checked = Boolean(src.enhanced_broadcasting);
    els.smodalVideoTracks.value = String(src.video_tracks || 0);
    syncSourceModalFields();
    els.smodalErrors.hidden = true;
    els.smodal.hidden = false;
    els.smodalName.focus();
  }
  function closeSourceModal() {
    els.smodal.hidden = true;
    smodalEditingName = null;
  }
  function submitSourceModal() {
    els.smodalErrors.hidden = true;
    const type = els.smodalType.value;
    const eb = type === "rtmp" && els.smodalEb.checked;
    send({
      command: "update_source",
      name: smodalEditingName,
      new_name: els.smodalName.value.trim(),
      type,
      audio_tracks: Number(els.smodalTracks.value) || 1,
      vod_track: type === "rtmp" && els.smodalVodTrack.checked,
      enhanced_broadcasting: eb,
      video_tracks: eb ? Number(els.smodalVideoTracks.value) || 0 : 0,
    });
  }
  function currentEditingSource() {
    return ((settingsData && settingsData.sources) || []).find((x) => x.name === smodalEditingName);
  }

  // --- platform modal ---

  function settingsSources() { return (settingsData && settingsData.sources) || []; }

  function normalizeIvs(url) {
    if (!url.startsWith("rtmps://")) return url;
    const after = url.slice("rtmps://".length);
    const cut = after.search(/[/?#]/);
    const authority = cut === -1 ? after : after.slice(0, cut);
    const rest = cut === -1 ? "" : after.slice(cut);
    let hostpart = authority;
    const at = hostpart.lastIndexOf("@");
    if (at >= 0) hostpart = hostpart.slice(at + 1);
    const host = hostpart.replace(/:\d+$/, "");
    if (!(host === "live-video.net" || host.endsWith(".live-video.net"))) return url;
    const authWithPort = /:\d+$/.test(authority) ? authority : authority + ":443";
    let path = rest, query = "";
    const q = rest.search(/[?#]/);
    if (q !== -1) { path = rest.slice(0, q); query = rest.slice(q); }
    if (path !== "/app" && !path.startsWith("/app/")) {
      path = "/app" + (path.startsWith("/") ? path : "/" + path);
    }
    return "rtmps://" + authWithPort + path + query;
  }
  function buildPushUrl(server, key) {
    server = (server || "").trim();
    key = (key || "").trim();
    if (!server) return "";
    const url = key ? server.replace(/\/+$/, "") + "/" + key.replace(/^\/+/, "") : server;
    return normalizeIvs(url);
  }
  // Mirrors output_url.build_srt_url: streamid/passphrase as query params,
  // values NOT url-encoded (the ingest compares them verbatim).
  function buildSrtUrl(server, streamid, passphrase) {
    server = (server || "").trim();
    if (!server) return "";
    const params = [];
    if ((streamid || "").trim()) params.push("streamid=" + streamid.trim());
    if ((passphrase || "").trim()) params.push("passphrase=" + passphrase.trim());
    if (!params.length) return server;
    return server + (server.includes("?") ? "&" : "?") + params.join("&");
  }

  function populateGroupSelect(select, selectedId) {
    select.innerHTML = "";
    const gs = (settingsData && settingsData.platform_groups) || [];
    const def = gs.find((g) => g.is_default);
    const target = selectedId || (def && def.id);
    for (const g of gs) {
      const opt = document.createElement("option");
      opt.value = g.id;
      opt.textContent = g.is_default ? `${g.name} (default)` : g.name;
      if (g.id === target) opt.selected = true;
      select.appendChild(opt);
    }
  }
  function populatePresetSelect(select, selectedId) {
    select.innerHTML = "";
    const presets = (settingsData && settingsData.fallback_presets) || [];
    const def = presets.find((p) => p.is_default);
    const target = selectedId || (def && def.id);
    for (const p of presets) {
      const opt = document.createElement("option");
      opt.value = p.id;
      opt.textContent = p.is_default ? `${p.name} (default)` : p.name;
      if (p.id === target) opt.selected = true;
      select.appendChild(opt);
    }
  }
  // Video source = source + which video it takes from it. An Enhanced
  // Broadcasting source offers its WHOLE ladder as one entry (video = -1,
  // passthrough to Twitch) plus every rung separately; a plain source is a
  // single entry, as before. The pick rides on the option's dataset --
  // source names may contain anything, so it can't be packed into the value.
  function populateSourceSelect(selectedName, selectedVideo) {
    const allowLadder = els.pmodalType.value === "rtmp";
    els.pmodalSource.innerHTML = "";
    let index = 0;
    const addOption = (source, video, label) => {
      const opt = document.createElement("option");
      opt.value = String(index++);
      opt.dataset.source = source.name;
      opt.dataset.video = String(video);
      opt.textContent = label;
      if (source.name === selectedName && video === selectedVideo) opt.selected = true;
      els.pmodalSource.appendChild(opt);
    };
    for (const s of settingsSources()) {
      if (s.enhanced_broadcasting) {
        if (allowLadder) addOption(s, -1, `${s.name} — all video tracks`);
        (s.video_track_labels || ["#1"]).forEach((label, i) => addOption(s, i, `${s.name} ${label}`));
      } else {
        addOption(s, 0, `${s.name} (${typeLabel(s)})`);
      }
    }
    // Selection no longer available (source removed, ladder shrank, type
    // switched to SRT) -> fall back to the first entry.
    if (els.pmodalSource.selectedIndex < 0) els.pmodalSource.selectedIndex = 0;
  }
  function selectedSourcePick() {
    const opt = els.pmodalSource.selectedOptions[0];
    if (!opt) return { name: "", video: 0 };
    return { name: opt.dataset.source, video: Number(opt.dataset.video) };
  }
  function selectedSourceName() { return selectedSourcePick().name; }
  // Audio comboboxes are filtered by the chosen source (single-source rule:
  // video and audio always come from the same source). Label is prefixed
  // with the source name (e.g. "Main #Live") -- with multiple sources
  // configured it's otherwise unclear whose track "#1"/"#Live" refers to.
  function fillTrackSelect(select, selectedIndex) {
    const src = settingsSources().find((s) => s.name === selectedSourceName());
    const labels = (src && src.track_labels) || ["#1"];
    // Одна доріжка -- вибору немає, дисейблимо.
    const single = labels.length === 1;
    select.innerHTML = "";
    labels.forEach((label, i) => {
      const opt = document.createElement("option");
      opt.value = String(i);
      opt.textContent = src ? `${src.name} ${label}` : label;
      if (i === (single ? 0 : selectedIndex)) opt.selected = true;
      select.appendChild(opt);
    });
    select.disabled = single;
  }
  function populateAudioSelect(selectedIndex, selectedVodIndex) {
    fillTrackSelect(els.pmodalAudio, selectedIndex);
    fillTrackSelect(els.pmodalAudioVod, selectedVodIndex === undefined ? 1 : selectedVodIndex);
  }

  // SRT-custom output: MAX_TRACK_SLOTS "Track #N" selects, one per output
  // slot -- each can pick ANY track of the chosen source (or none),
  // duplicates allowed. Built once, then just repopulated/shown-hidden.
  // Only a *contiguous* prefix is ever exposed: Track #1 is always shown;
  // Track #(k+1) is shown only once Track #k has a value, one slot ahead
  // of the current selection, up to MAX_TRACK_SLOTS. Clearing a slot in
  // the middle compacts the list (later values shift up) rather than
  // leaving a gap -- `compactAudioMap` implements both "fill" and "clear"
  // as the same stable partition (non-null values keep their relative
  // order, nulls move to the end).
  function compactAudioMap(map) {
    const filled = (Array.isArray(map) ? map : []).filter((v) => v !== null && v !== undefined);
    const result = filled.slice(0, MAX_TRACK_SLOTS);
    while (result.length < MAX_TRACK_SLOTS) result.push(null);
    return result;
  }
  function visibleTrackSlotCount(map) {
    const filled = map.filter((v) => v !== null && v !== undefined).length;
    return Math.min(filled + 1, MAX_TRACK_SLOTS);
  }
  function ensureTrackMapSelects() {
    if (els.pmodalTrackMap.children.length) return;
    for (let i = 0; i < MAX_TRACK_SLOTS; i++) {
      const field = document.createElement("div");
      field.className = "field";
      const label = document.createElement("label");
      label.textContent = `Track #${i + 1}`;
      const select = document.createElement("select");
      select.addEventListener("change", () => {
        fillTrackMapSelects(trackMapSelects().map((s) => (s.value === "" ? null : Number(s.value))));
      });
      field.appendChild(label);
      field.appendChild(select);
      els.pmodalTrackMap.appendChild(field);
    }
  }
  function trackMapSelects() {
    return Array.from(els.pmodalTrackMap.querySelectorAll("select"));
  }
  function fillTrackMapSelects(selectedMap) {
    ensureTrackMapSelects();
    const src = settingsSources().find((s) => s.name === selectedSourceName());
    const labels = (src && src.track_labels) || [];
    const map = compactAudioMap(selectedMap);
    const visibleCount = visibleTrackSlotCount(map);
    trackMapSelects().forEach((select, i) => {
      select.innerHTML = "";
      const none = document.createElement("option");
      none.value = "";
      none.textContent = "None";
      select.appendChild(none);
      labels.forEach((label, ti) => {
        const opt = document.createElement("option");
        opt.value = String(ti);
        opt.textContent = src ? `${src.name} ${label}` : label;
        select.appendChild(opt);
      });
      const current = map[i];
      select.value = current === null || current === undefined ? "" : String(current);
      select.disabled = labels.length === 0;
      const field = select.closest(".field");
      if (field) field.hidden = i >= visibleCount;
    });
  }
  function currentAudioMap() {
    return compactAudioMap(trackMapSelects().map((select) => (select.value === "" ? null : Number(select.value))));
  }
  function identityAudioMap(trackCount) {
    return Array.from({ length: MAX_TRACK_SLOTS }, (_, i) => (i < trackCount ? i : null));
  }

  function syncPlatformModalFields() {
    const type = els.pmodalType.value;
    const isSrt = type === "srt";
    els.pmodalVodTrackField.hidden = isSrt;
    // Окрема доріжка для VOD є лише в мультитрек/EB-джерела.
    const vodSource = settingsSources().find((s) => s.name === selectedSourceName());
    const canVod = Boolean(vodSource) &&
      ((vodSource.audio_tracks || 1) > 1 || Boolean(vodSource.enhanced_broadcasting));
    els.pmodalVodTrack.disabled = !canVod;
    els.pmodalVodTrack.title = canVod ? "" : "needs a source with 2 audio tracks";
    if (!canVod) els.pmodalVodTrack.checked = false;
    const isVod = !isSrt && els.pmodalVodTrack.checked;
    const ladder = selectedSourcePick().video < 0;
    // SRT-вихід бере доріжки з track-map, тож обидві аудіо-клітинки
    // сітки ховаються, а Video source розтягується на весь рядок.
    els.pmodalAudioField.hidden = isSrt;
    els.pmodalStreamGrid.classList.toggle("no-audio", isSrt);
    els.pmodalTrackMap.hidden = !isSrt;
    els.pmodalAudio.disabled = els.pmodalAudio.options.length <= 1;
    els.pmodalAudioVodField.hidden = isSrt || !isVod;
    els.pmodalAudioLabel.textContent = isVod ? "Live audio track" : "Audio track";
    // Whole-ladder pick: Twitch assigns the ingest at broadcast start, so the
    // Server field plays no part -- show that as state, not as a paragraph.
    els.pmodalServer.disabled = ladder;
    els.pmodalCredsRtmp.hidden = isSrt;
    els.pmodalCredsSrt.hidden = !isSrt;
    syncCloneFill(type);
    updatePlatformUrlPreview();
  }
  // Кнопка Clone: заповнити конструктор із source того самого типу --
  // сам source, його VOD Track-статус і драбину (для srt -- ще й
  // identity-заповнення track-map).
  function syncCloneFill(type) {
    const candidates = settingsSources().filter((s) => s.type === type);
    els.pmodalCloneFill.hidden = candidates.length === 0;
    els.pmodalCloneFill.innerHTML = "";
    const placeholder = document.createElement("option");
    placeholder.value = "";
    placeholder.textContent = "Clone";
    placeholder.disabled = true;
    placeholder.hidden = true;
    placeholder.selected = true;
    els.pmodalCloneFill.appendChild(placeholder);
    for (const s of candidates) {
      const opt = document.createElement("option");
      opt.value = s.name;
      opt.textContent = s.name;
      els.pmodalCloneFill.appendChild(opt);
    }
  }
  function syncPlatformMask() {
    const t = pmodalSecretsShown ? "text" : "password";
    els.pmodalServer.type = t;
    els.pmodalKey.type = t;
    els.pmodalSrtServer.type = t;
    els.pmodalStreamid.type = t;
    els.pmodalPassphrase.type = t;
    const label = pmodalSecretsShown ? "Hide" : "Show";
    els.pmodalShowRtmp.textContent = label;
    els.pmodalShowSrt.textContent = label;
    updatePlatformUrlPreview();
  }
  function updatePlatformUrlPreview() {
    // Драбина: URL видає Twitch на старті ефіру, з конфіга йде лише ключ.
    if (selectedSourcePick().video < 0) {
      els.pmodalUrlRtmp.textContent = "assigned by Twitch when the broadcast starts";
      els.pmodalUrlSrt.textContent = "–";
      return;
    }
    const rtmpUrl = buildPushUrl(els.pmodalServer.value, els.pmodalKey.value) || "–";
    els.pmodalUrlRtmp.textContent = (pmodalSecretsShown || rtmpUrl === "–") ? rtmpUrl : "••••••••••••";
    const srtUrl = buildSrtUrl(els.pmodalSrtServer.value, els.pmodalStreamid.value, els.pmodalPassphrase.value) || "–";
    els.pmodalUrlSrt.textContent = (pmodalSecretsShown || srtUrl === "–") ? srtUrl : "••••••••••••";
  }
  function openPlatformModal(p) {
    pmodalEditingName = p.name;
    els.pmodalTitle.textContent = `Modify platform: ${p.name}`;
    els.pmodalName.value = p.name;
    els.pmodalType.value = p.type;
    els.pmodalVodTrack.checked = Boolean(p.vod_track);
    populateGroupSelect(els.pmodalGroup, p.group);
    populatePresetSelect(els.pmodalPreset, p.backup_preset);
    populateSourceSelect(p.source, p.video === undefined ? 0 : p.video);
    populateAudioSelect(p.audio || 0, p.audio_vod === undefined ? 1 : p.audio_vod);
    fillTrackMapSelects(p.audio_map || [0]);
    els.pmodalServer.value = p.type === "srt" ? "" : (p.server || "");
    els.pmodalKey.value = p.key || "";
    els.pmodalSrtServer.value = p.type === "srt" ? (p.server || "") : "";
    els.pmodalStreamid.value = p.streamid || "";
    els.pmodalPassphrase.value = p.passphrase || "";
    pmodalSecretsShown = false;  // sensitive -> masked by default
    syncPlatformMask();
    syncPlatformModalFields();
    els.pmodalErrors.hidden = true;
    els.pmodal.hidden = false;
    els.pmodalName.focus();
  }
  function closePlatformModal() {
    els.pmodal.hidden = true;
    pmodalEditingName = null;
  }
  function submitPlatformModal() {
    els.pmodalErrors.hidden = true;
    const type = els.pmodalType.value;
    const isSrt = type === "srt";
    const payload = {
      command: "update_platform",
      name: pmodalEditingName,
      new_name: els.pmodalName.value.trim(),
      type,
      vod_track: !isSrt && els.pmodalVodTrack.checked,
      server: (isSrt ? els.pmodalSrtServer.value : els.pmodalServer.value).trim(),
      key: els.pmodalKey.value.trim(),
      streamid: els.pmodalStreamid.value.trim(),
      passphrase: els.pmodalPassphrase.value.trim(),
      source: selectedSourcePick().name,
      video: selectedSourcePick().video,
      audio: Number(els.pmodalAudio.value) || 0,
      audio_vod: Number(els.pmodalAudioVod.value) || 0,
      group: els.pmodalGroup.value,
      backup_preset: els.pmodalPreset.value,
    };
    if (isSrt) payload.audio_map = currentAudioMap();
    send(payload);
  }

  // --- group modal ---

  function openGroupModal(mode, group) {
    gmodalMode = mode;
    gmodalEditingId = mode === "edit" ? group.id : null;
    els.gmodalTitle.textContent = mode === "edit" ? `Rename group: ${group.name}` : "Add group";
    els.gmodalName.value = mode === "edit" ? group.name : "";
    els.gmodalErrors.hidden = true;
    els.gmodal.hidden = false;
    els.gmodalName.focus();
  }
  function closeGroupModal() {
    els.gmodal.hidden = true;
    gmodalMode = null;
    gmodalEditingId = null;
  }
  function submitGroupModal() {
    els.gmodalErrors.hidden = true;
    const name = els.gmodalName.value.trim();
    if (!name) { showErrorsInto(els.gmodalErrors, { name: "name is required" }); return; }
    if (gmodalMode === "add") {
      send({ command: "add_group", name });  // group_result closes on success
    } else {
      // rename has no result reply -- the settings refresh re-renders the list
      send({ command: "update_group", id: gmodalEditingId, name });
      closeGroupModal();
    }
  }
  function handleGroupResult(message) {
    if (els.gmodal.hidden) {
      if (!message.ok) {
        const errs = message.errors || {};
        enqueueToast("error", errs._ || Object.values(errs)[0] || "operation failed");
      }
      return;
    }
    if (message.ok) closeGroupModal();
    else showErrorsInto(els.gmodalErrors, message.errors || {});
  }

  // --- fallback presets (panel + modal) ---

  function presetSegSummary(p) {
    const parts = [];
    if (p.start_file) parts.push("Start");
    if (p.type === "folder") {
      parts.push(p.separator_file ? "Folder+sep" : "Folder");
    } else {
      parts.push("Loop");
    }
    if (p.end_file) parts.push("End");
    return parts.join(" → ");
  }

  function applyPresetType(type) {
    const isFolder = type === "folder";
    els.presetLoopField.hidden = isFolder;
    els.presetFolderField.hidden = !isFolder;
    els.presetSeparatorField.hidden = !isFolder;
  }

  function renderPresetsSettings() {
    if (!settingsData) return;
    const live = wsLive();
    els.presetsList.innerHTML = "";
    for (const p of settingsData.fallback_presets || []) {
      els.presetsList.appendChild(makeItemRow(
        p.name,
        p.is_default ? "Default preset" : null,
        presetSegSummary(p),
        p.is_default ? null : () => {
          if (confirm(`Delete fallback preset "${p.name}"?`)) send({ command: "remove_preset", id: p.id });
        },
        () => openPresetModal("edit", p),
        live,
      ));
    }
  }

  function openPresetModal(mode, preset) {
    presetMode = mode;
    presetEditingId = mode === "edit" ? preset.id : null;
    const isDefault = mode === "edit" && preset.is_default;
    els.presetModalTitle.textContent = mode === "edit" ? `Modify fallback preset: ${preset.name}` : "Add fallback preset";
    els.presetName.value = mode === "edit" ? preset.name : "";
    const type = mode === "edit" ? (preset.type || "sequence") : "sequence";
    els.presetType.value = type;
    els.presetStart.value = mode === "edit" ? (preset.start_file || "") : "";
    els.presetLoop.value = mode === "edit" ? (preset.loop_file || "") : "";
    els.presetFolder.value = mode === "edit" ? (preset.folder || "") : "";
    els.presetSeparator.value = mode === "edit" ? (preset.separator_file || "") : "";
    els.presetEnd.value = mode === "edit" ? (preset.end_file || "") : "";
    applyPresetType(type);
    // The default preset is editable but not deletable.
    els.presetDelete.hidden = !(mode === "edit" && !isDefault);
    els.presetErrors.hidden = true;
    els.presetModal.hidden = false;
    revalidatePathFields();
    els.presetName.focus();
  }
  function closePresetModal() {
    els.presetModal.hidden = true;
    presetMode = null;
    presetEditingId = null;
  }
  function submitPresetModal() {
    els.presetErrors.hidden = true;
    const payload = {
      name: els.presetName.value.trim(),
      type: els.presetType.value,
      start_file: els.presetStart.value.trim(),
      loop_file: els.presetLoop.value.trim(),
      folder: els.presetFolder.value.trim(),
      separator_file: els.presetSeparator.value.trim(),
      end_file: els.presetEnd.value.trim(),
    };
    if (presetMode === "add") send({ command: "add_preset", ...payload });
    else send({ command: "update_preset", id: presetEditingId, ...payload });
  }
  function handlePresetResult(message) {
    // Delete triggered from the list (modal closed) -> surface a blocking
    // error (e.g. preset in use) as a toast; success is silent (list refreshes).
    if (els.presetModal.hidden) {
      if (!message.ok) {
        const errs = message.errors || {};
        enqueueToast("error", errs._ || Object.values(errs)[0] || "operation failed");
      }
      return;
    }
    if (message.ok) closePresetModal();
    else showErrorsInto(els.presetErrors, message.errors || {});
  }

  // --- backup/ material: file manager, pickers, upload ---

  // Один стан на менеджер і пікери: відкритий одночасно лише один із
  // них, і обидва показують ту саму теку.
  const browser = {
    mode: null,        // "manager" | "file" | "dir"
    path: "",          // поточна тека, відносно backup/
    pending: null,     // тека, лістинг якої ми щойно попросили
    entries: [],
    selection: null,   // обраний запис пікера (відносний шлях)
    opening: false,    // перший лістинг пікера -- підсвітку вже виставлено
    field: null,       // input модалки пресета, який відкрив пікер
  };

  const upload = { xhr: null, file: null, pending: null, lastLoaded: 0, lastAt: 0, rate: 0 };

  function joinPath(dir, name) { return dir ? `${dir}/${name}` : name; }
  function parentPath(path) {
    const cut = String(path || "").lastIndexOf("/");
    return cut < 0 ? "" : path.slice(0, cut);
  }
  function displayPath(path) { return `backup/${path ? path + "/" : ""}`; }

  function formatBytes(size) {
    if (typeof size !== "number" || !isFinite(size)) return "";
    if (size >= 1024 ** 3) return `${(size / 1024 ** 3).toFixed(1)} GB`;
    if (size >= 1024 ** 2) return `${Math.round(size / 1024 ** 2)} MB`;
    return `${Math.max(1, Math.round(size / 1024))} KB`;
  }

  function listFiles(path) {
    browser.pending = path;
    send({ command: "list_files", path });
  }

  function handleFilesListing(data) {
    if (!data || !browser.mode) return;
    // Без запиту в польоті це фонова розсилка добраних тривалостей --
    // приймаємо її, лише якщо вона про ту саму теку, що ми показуємо.
    if (browser.pending === null && data.path !== browser.path) return;
    browser.pending = null;
    const moved = data.path !== browser.path;
    browser.path = data.path;
    browser.entries = data.entries || [];
    if (browser.opening) {
      browser.opening = false;  // підсвітку поточного значення вже виставив openPicker
    } else if (moved) {
      browser.selection = null;
      // Зайти в теку в режимі вибору теки = обрати саме її.
      if (browser.mode === "dir") els.pickerPath.value = data.path ? `${data.path}/` : "";
    }
    renderBrowser();
  }

  function handleFilesResult(message) {
    if (message.ok) {
      els.filesErrors.hidden = true;
      return;
    }
    const errors = message.errors || {};
    const text = errors._ || Object.values(errors)[0] || "operation failed";
    if (browser.mode === "manager") showErrorsInto(els.filesErrors, { _: text });
    else enqueueToast("error", text);
  }

  function renderBrowser() {
    if (browser.mode === "manager") renderManagerRows();
    else if (browser.mode) renderPickerRows();
  }

  function entryMeta(entry) {
    if (entry.is_dir) {
      const count = entry.video_count || 0;
      return `${count} video${count === 1 ? "" : "s"}`;
    }
    const parts = [formatBytes(entry.size)];
    // Тривалість добирається у фоні (ffprobe на файл) -- поки її немає,
    // рядок просто не має цієї частини, а не показує нуль.
    if (typeof entry.duration === "number") parts.push(formatDuration(entry.duration));
    return parts.join(" · ");
  }

  function makeEntryRow(entry, withRadio) {
    const row = document.createElement("div");
    row.className = entry.is_dir ? "fm-row" : "fm-row is-file";

    const name = document.createElement("div");
    name.className = "fm-name";
    if (withRadio) {
      const radio = document.createElement("span");
      radio.className = "fm-radio";
      name.appendChild(radio);
    }
    const glyph = document.createElement("span");
    glyph.className = entry.is_dir ? "fm-glyph" : "fm-glyph file";
    name.appendChild(glyph);
    const label = document.createElement("span");
    label.className = "fm-label";
    label.textContent = entry.is_dir ? `${entry.name}/` : entry.name;
    name.appendChild(label);
    row.appendChild(name);

    const meta = document.createElement("div");
    meta.className = "fm-meta";
    meta.textContent = entryMeta(entry);
    row.appendChild(meta);
    return row;
  }

  // --- превʼю (стрім із сервера, Range) ---

  function previewUrl(rel) {
    return `/files/raw?token=${encodeURIComponent(RS.token)}&path=${encodeURIComponent(rel)}`;
  }

  function openPreview(rel, entry) {
    els.previewTitle.textContent = `backup/${rel}`;
    els.previewNote.hidden = true;
    const meta = [formatBytes(entry && entry.size)];
    if (entry && typeof entry.duration === "number") meta.push(formatDuration(entry.duration));
    els.previewMeta.textContent = meta.filter(Boolean).join(" · ");
    els.previewVideo.src = previewUrl(rel);
    els.previewModal.hidden = false;
    els.previewVideo.play().catch(() => { /* автоплей може бути заборонений -- не біда */ });
  }

  function closePreview() {
    els.previewModal.hidden = true;
    // Знімаємо src, інакше вкладка й далі тягне файл із сервера.
    els.previewVideo.pause();
    els.previewVideo.removeAttribute("src");
    els.previewVideo.load();
  }

  function makeRowButton(text, onClick, extraClass) {
    const button = document.createElement("button");
    button.type = "button";
    button.textContent = text;
    if (extraClass) button.className = extraClass;
    button.addEventListener("click", (event) => {
      event.stopPropagation();  // рядок теки сам по собі -- навігація
      onClick();
    });
    return button;
  }

  function emptyRow(text) {
    const empty = document.createElement("div");
    empty.className = "fm-empty";
    empty.textContent = text;
    return empty;
  }

  function renderManagerRows() {
    els.fmPath.textContent = displayPath(browser.path);
    els.fmUp.disabled = !browser.path;
    els.fmRows.innerHTML = "";
    if (!browser.entries.length) {
      els.fmRows.appendChild(emptyRow("no folders or video files here"));
      return;
    }
    for (const entry of browser.entries) {
      const rel = joinPath(browser.path, entry.name);
      const row = makeEntryRow(entry, false);
      if (entry.is_dir) row.addEventListener("click", () => listFiles(rel));
      else row.style.cursor = "default";

      const actions = document.createElement("div");
      actions.className = "fm-actions";
      if (!entry.is_dir) actions.appendChild(makeRowButton("Preview", () => openPreview(rel, entry)));
      actions.appendChild(makeRowButton("Rename", () => askRename(rel, entry.name)));
      actions.appendChild(makeRowButton("Delete", () => askDelete(rel, entry), "danger"));
      row.appendChild(actions);
      els.fmRows.appendChild(row);
    }
  }

  function renderPickerRows() {
    const dirsOnly = browser.mode === "dir";
    els.pickerUp.disabled = !browser.path;
    els.pickerRows.innerHTML = "";
    const entries = browser.entries.filter((entry) => (dirsOnly ? entry.is_dir : true));
    if (!entries.length) {
      els.pickerRows.appendChild(emptyRow(dirsOnly ? "no folders here" : "no folders or video files here"));
      return;
    }
    for (const entry of entries) {
      const rel = joinPath(browser.path, entry.name);
      // Теку в режимі вибору ФАЙЛУ лише відкривають -- радіо їй ні до чого.
      const selectable = dirsOnly || !entry.is_dir;
      const row = makeEntryRow(entry, selectable);
      if (browser.selection === rel) row.classList.add("selected");
      row.addEventListener("click", () => {
        if (selectable) selectInPicker(rel, entry.is_dir);
        else listFiles(rel);
      });
      if (entry.is_dir) row.addEventListener("dblclick", () => listFiles(rel));
      if (!entry.is_dir) {
        const actions = document.createElement("div");
        actions.className = "fm-actions";
        actions.appendChild(makeRowButton("Preview", () => openPreview(rel, entry)));
        row.appendChild(actions);
      }
      els.pickerRows.appendChild(row);
    }
  }

  function selectInPicker(rel, isDir) {
    browser.selection = rel;
    els.pickerPath.value = isDir ? `${rel}/` : rel;
    renderPickerRows();
  }

  function openFileManager() {
    browser.mode = "manager";
    browser.selection = null;
    els.filesErrors.hidden = true;
    els.filesModal.hidden = false;
    listFiles(browser.path);
  }
  function closeFileManager() {
    els.filesModal.hidden = true;
    browser.mode = null;
  }

  function openPicker(input, mode, title) {
    const current = (input.value || "").trim().replace(/\/+$/, "");
    browser.mode = mode;
    browser.field = input;
    // Відкриваємось у теці-БАТЬКУ поточного значення й підсвічуємо саме
    // його. Заходити всередину вже обраної теки не можна: пікер теки
    // показував би її нутрощі (типово "no folders here"), а не її саму
    // серед сусідів, тобто вибір виглядав би порожнім.
    browser.selection = current || null;
    browser.opening = true;
    els.pickerTitle.textContent = title || (mode === "dir" ? "Select folder" : "Select video");
    els.pickerOk.textContent = mode === "dir" ? "Select folder" : "Select file";
    els.pickerPath.value = input.value || "";
    els.pickerModal.hidden = false;
    listFiles(parentPath(current));
  }
  function closePicker() {
    els.pickerModal.hidden = true;
    browser.mode = null;
    browser.field = null;
  }
  function confirmPicker() {
    if (browser.field) browser.field.value = els.pickerPath.value.trim();
    closePicker();
  }

  // --- одно-рядковий текстовий запит (нова тека / перейменування) ---

  let textModalOnOk = null;

  function askText(title, label, value, onOk) {
    textModalOnOk = onOk;
    els.textModalTitle.textContent = title;
    els.textModalLabel.textContent = label;
    els.textModalInput.value = value || "";
    els.textModal.hidden = false;
    els.textModalInput.focus();
    els.textModalInput.select();
  }
  function closeTextModal() {
    els.textModal.hidden = true;
    textModalOnOk = null;
  }
  function submitTextModal() {
    const value = els.textModalInput.value.trim();
    const callback = textModalOnOk;
    closeTextModal();
    if (value && callback) callback(value);
  }

  function askNewFolder() {
    askText("New folder", `Create inside ${displayPath(browser.path)}`, "", (name) => {
      send({ command: "make_dir", path: browser.path, name });
    });
  }
  function askRename(rel, currentName) {
    askText("Rename", `New name for "${currentName}"`, currentName, (name) => {
      send({ command: "rename_path", path: rel, new_name: name });
    });
  }
  function askDelete(rel, entry) {
    const what = entry.is_dir
      ? `the folder "${entry.name}" and everything inside it`
      : `"${entry.name}"`;
    if (confirm(`Delete ${what}?\n\nThis cannot be undone.`)) {
      send({ command: "delete_path", path: rel });
    }
  }

  // --- автодоповнення й жива перевірка полів шляху ---

  // Один запит дає і підказки, і стан поля. Таймер у кожного поля свій.
  const SUGGEST_DEBOUNCE_MS = 150;
  const pathFields = new Map();   // id поля -> {input, box, error, dirsOnly, timer, items, active}

  function pathFieldConfig(input) {
    const button = document.querySelector(`[data-browse="${input.id}"]`);
    return { dirsOnly: button ? button.dataset.browseMode === "dir" : false };
  }

  function registerPathField(input) {
    const row = input.closest(".path-row");
    const box = document.createElement("div");
    box.className = "path-suggest";
    box.hidden = true;
    row.appendChild(box);
    const error = document.createElement("div");
    error.className = "path-error";
    error.hidden = true;
    row.parentNode.appendChild(error);

    const entry = {
      input, box, error, dirsOnly: pathFieldConfig(input).dirsOnly,
      timer: null, items: [], active: -1, required: false,
    };
    pathFields.set(input.id, entry);

    input.addEventListener("input", () => requestSuggestions(entry));
    input.addEventListener("focus", () => requestSuggestions(entry));
    input.addEventListener("blur", () => {
      // Клік по підказці приходить ПІСЛЯ blur -- даємо йому спрацювати.
      setTimeout(() => { box.hidden = true; }, 150);
    });
    input.addEventListener("keydown", (event) => onPathKeydown(entry, event));
    return entry;
  }

  function requestSuggestions(entry) {
    clearTimeout(entry.timer);
    entry.timer = setTimeout(() => {
      send({
        command: "complete_path", field: entry.input.id,
        prefix: entry.input.value.trim(), dirs_only: entry.dirsOnly,
      });
    }, SUGGEST_DEBOUNCE_MS);
  }

  function handlePathSuggestions(message) {
    const entry = pathFields.get(message.field);
    if (!entry) return;
    // Відповідь на застарілий запит (встигли додрукувати) -- ігноруємо.
    if (message.prefix !== entry.input.value.trim()) return;

    entry.items = message.entries || [];
    entry.active = -1;
    renderSuggestions(entry);
    applyPathStatus(entry, message.status);
  }

  function renderSuggestions(entry) {
    const value = entry.input.value.trim();
    const head = value.includes("/") ? value.slice(0, value.lastIndexOf("/") + 1) : "";
    entry.box.innerHTML = "";
    // Єдиний точний збіг -- підказувати нічого, поле вже заповнене.
    const useful = entry.items.filter(
      (item) => head + item.name + (item.is_dir ? "/" : "") !== value);
    if (!useful.length || document.activeElement !== entry.input) {
      entry.box.hidden = true;
      return;
    }
    for (const item of useful) {
      const row = document.createElement("div");
      row.className = "path-suggest-item";
      const name = document.createElement("span");
      name.textContent = item.name + (item.is_dir ? "/" : "");
      row.appendChild(name);
      if (item.is_dir) {
        const count = document.createElement("span");
        count.className = "path-suggest-count";
        count.textContent = `${item.video_count} video${item.video_count === 1 ? "" : "s"}`;
        row.appendChild(count);
      }
      row.addEventListener("mousedown", (event) => {
        event.preventDefault();  // не віддавати фокус до того, як ми підставили значення
        completeWith(entry, head, item);
      });
      entry.box.appendChild(row);
    }
    entry.items = useful;
    entry.box.hidden = false;
  }

  function completeWith(entry, head, item) {
    entry.input.value = head + item.name + (item.is_dir ? "/" : "");
    entry.box.hidden = true;
    entry.input.focus();
    requestSuggestions(entry);  // зайшли в теку -> показати її вміст
  }

  function onPathKeydown(entry, event) {
    const rows = [...entry.box.children];
    if (event.key === "Escape") {
      entry.box.hidden = true;
      return;
    }
    if (!rows.length || entry.box.hidden) return;
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      entry.active = (entry.active + (event.key === "ArrowDown" ? 1 : rows.length - 1)) % rows.length;
      rows.forEach((row, i) => row.classList.toggle("active", i === entry.active));
      return;
    }
    if (event.key === "Tab" || event.key === "Enter") {
      const index = entry.active >= 0 ? entry.active : (rows.length === 1 ? 0 : -1);
      if (index < 0) return;
      event.preventDefault();
      const value = entry.input.value.trim();
      const head = value.includes("/") ? value.slice(0, value.lastIndexOf("/") + 1) : "";
      completeWith(entry, head, entry.items[index]);
    }
  }

  // Порожнє необовʼязкове поле -- не помилка. Придатність файлу міряє Save.
  function applyPathStatus(entry, status) {
    const value = entry.input.value.trim();
    let problem = "";
    if (value && status) {
      if (!status.exists) problem = "not found in backup/";
      else if (entry.dirsOnly && !status.is_dir) problem = "this is a file, pick a folder";
      else if (!entry.dirsOnly && status.is_dir) problem = "this is a folder, pick a file";
      else if (entry.dirsOnly && !status.video_count) problem = "this folder has no video files";
    } else if (!value && entry.required) {
      problem = "required";
    }
    entry.input.classList.toggle("invalid", Boolean(problem));
    entry.error.textContent = problem;
    entry.error.hidden = !problem;
    updatePresetOkState();
  }

  function visiblePathFields() {
    return [...pathFields.values()].filter((entry) => {
      const field = entry.input.closest(".field");
      return field && !field.hidden;
    });
  }

  function updatePresetOkState() {
    const bad = visiblePathFields().some((entry) => entry.input.classList.contains("invalid"));
    els.presetOk.disabled = bad;
  }

  // Обовʼязковість залежить від типу пресета.
  function revalidatePathFields() {
    const isFolder = els.presetType.value === "folder";
    for (const [id, entry] of pathFields) {
      entry.required = (id === "preset-folder" && isFolder) || (id === "preset-loop" && !isFolder);
      entry.input.classList.remove("invalid");
      entry.error.hidden = true;
      if (entry.input.value.trim() || entry.required) requestSuggestions(entry);
    }
    updatePresetOkState();
  }

  // --- аплоад (один файл за раз) ---

  function uploadUrl(name) {
    return `/files/upload?token=${encodeURIComponent(RS.token)}` +
      `&path=${encodeURIComponent(browser.path)}&name=${encodeURIComponent(name)}`;
  }

  // Питаємо сервер ДО передачі байтів: рання відмова на запиті з тілом
  // доходить до клієнта обривом, а не причиною (див. _reject_before_body).
  function offerUpload(files) {
    const file = files && files[0];
    if (!file) return;
    if (upload.xhr) {
      enqueueToast("error", "an upload is already running");
      return;
    }
    upload.pending = file;
    send({ command: "check_upload", path: browser.path, name: file.name, size: file.size });
  }

  function handleUploadCheck(message) {
    const file = upload.pending;
    upload.pending = null;
    if (!file) return;
    if (message.ok) startUpload(file);
    else showErrorsInto(els.filesErrors, { _: message.error || "this file cannot be uploaded" });
  }

  function startUpload(file) {
    if (upload.xhr) {
      enqueueToast("error", "an upload is already running");
      return;
    }
    upload.file = file;
    upload.lastLoaded = 0;
    upload.lastAt = Date.now();
    upload.rate = 0;

    els.uploadTitle.textContent = `Uploading to ${displayPath(browser.path)}`;
    els.uploadName.textContent = file.name;
    els.uploadErrors.hidden = true;
    els.uploadAbort.hidden = false;
    els.uploadClose.hidden = true;
    els.uploadRetry.hidden = true;
    setUploadProgress(0, file.size);
    els.uploadModal.hidden = false;

    const xhr = new XMLHttpRequest();
    upload.xhr = xhr;
    xhr.open("POST", uploadUrl(file.name));
    xhr.setRequestHeader("Content-Type", "application/octet-stream");
    xhr.upload.addEventListener("progress", (event) => {
      if (event.lengthComputable) setUploadProgress(event.loaded, event.total);
    });
    xhr.addEventListener("load", () => {
      upload.xhr = null;
      let body = {};
      try { body = JSON.parse(xhr.responseText || "{}"); } catch (e) { /* нижче стане помилкою */ }
      if (xhr.status === 200 && body.ok) {
        els.uploadModal.hidden = true;
        enqueueToast("info", `uploaded backup/${body.path}`);
        listFiles(browser.path);
      } else {
        failUpload(body.error || `upload failed (HTTP ${xhr.status})`);
      }
    });
    xhr.addEventListener("error", () => { upload.xhr = null; failUpload("network error"); });
    xhr.addEventListener("abort", () => { upload.xhr = null; els.uploadModal.hidden = true; });
    xhr.send(file);
  }

  function setUploadProgress(loaded, total) {
    const percent = total ? Math.floor((loaded / total) * 100) : 0;
    els.uploadFill.style.width = `${percent}%`;
    els.uploadPercent.textContent = `${percent}%`;

    const now = Date.now();
    const seconds = (now - upload.lastAt) / 1000;
    if (seconds >= 0.5) {
      // Згладжений темп: миттєвий стрибав би на кожному семплі.
      const instant = (loaded - upload.lastLoaded) / seconds;
      upload.rate = upload.rate ? upload.rate * 0.7 + instant * 0.3 : instant;
      upload.lastLoaded = loaded;
      upload.lastAt = now;
    }

    if (total && loaded >= total) {
      // Байти пішли, але сервер ще перевіряє доріжки -- це ще не "готово".
      els.uploadDetail.textContent = "checking video and audio tracks…";
      return;
    }
    const parts = [`${formatBytes(loaded)} of ${formatBytes(total)}`];
    if (upload.rate > 0) {
      parts.push(`${formatBytes(upload.rate)}/s`);
      parts.push(`${formatDuration((total - loaded) / upload.rate)} left`);
    }
    els.uploadDetail.textContent = parts.join(" · ");
  }

  function failUpload(text) {
    showErrorsInto(els.uploadErrors, { _: text });
    els.uploadAbort.hidden = true;
    els.uploadClose.hidden = false;
    els.uploadRetry.hidden = false;
  }

  function abortUpload() {
    if (upload.xhr) upload.xhr.abort();
    else els.uploadModal.hidden = true;
  }


  // --- listeners ---

  els.btnAddSource.addEventListener("click", () => openCreateModal("source"));
  els.btnAddPlatform.addEventListener("click", () => openCreateModal("platform"));
  els.btnAddGroup.addEventListener("click", () => openGroupModal("add", null));
  els.createOk.addEventListener("click", submitCreate);
  els.createCancel.addEventListener("click", closeCreateModal);
  els.createModal.addEventListener("click", (e) => { if (e.target === els.createModal) closeCreateModal(); });
  els.createName.addEventListener("keydown", (e) => { if (e.key === "Enter") submitCreate(); });

  els.smodalType.addEventListener("change", syncSourceModalFields);
  els.smodalVodTrack.addEventListener("change", syncSourceModalFields);
  els.smodalEb.addEventListener("change", syncSourceModalFields);
  els.smodalTracks.addEventListener("input", () => {
    els.smodalTracksValue.textContent = els.smodalTracks.value;
  });
  els.smodalOk.addEventListener("click", submitSourceModal);
  els.smodalCancel.addEventListener("click", closeSourceModal);
  els.smodal.addEventListener("click", (e) => { if (e.target === els.smodal) closeSourceModal(); });
  const toggleSourceIngest = () => {
    smodalIngestShown = !smodalIngestShown;
    renderSourceIngest();
  };
  els.smodalIngestShowRtmp.addEventListener("click", toggleSourceIngest);
  els.smodalIngestShowSrt.addEventListener("click", toggleSourceIngest);

  els.pmodalType.addEventListener("change", () => {
    const pick = selectedSourcePick();
    populateSourceSelect(pick.name, pick.video);
    populateAudioSelect(Number(els.pmodalAudio.value) || 0, Number(els.pmodalAudioVod.value) || 0);
    fillTrackMapSelects(currentAudioMap());
    syncPlatformModalFields();
  });
  els.pmodalSource.addEventListener("change", () => {
    populateAudioSelect(0, 1);
    fillTrackMapSelects([0]);
    syncPlatformModalFields();
  });
  els.pmodalVodTrack.addEventListener("change", () => {
    populateAudioSelect(Number(els.pmodalAudio.value) || 0, Number(els.pmodalAudioVod.value) || 0);
    syncPlatformModalFields();
  });
  els.pmodalCloneFill.addEventListener("change", () => {
    const name = els.pmodalCloneFill.value;
    if (!name) return;
    const src = settingsSources().find((s) => s.name === name);
    if (!src) return;
    // "Клонувати" = віддавати те саме, що несе source: його VOD Track,
    // усю його драбину (якщо це EB-source і вихід RTMP) і доріжки 1:1.
    const isRtmp = els.pmodalType.value === "rtmp";
    els.pmodalVodTrack.checked = isRtmp && Boolean(src.vod_track);
    const ladder = isRtmp && Boolean(src.enhanced_broadcasting);
    populateSourceSelect(name, ladder ? -1 : 0);
    populateAudioSelect(0, 1);
    fillTrackMapSelects(identityAudioMap(src.audio_tracks || 1));
    syncPlatformModalFields();
  });
  for (const input of [els.pmodalServer, els.pmodalKey, els.pmodalSrtServer, els.pmodalStreamid, els.pmodalPassphrase]) {
    input.addEventListener("input", updatePlatformUrlPreview);
  }
  els.pmodalShowRtmp.addEventListener("click", () => { pmodalSecretsShown = !pmodalSecretsShown; syncPlatformMask(); });
  els.pmodalShowSrt.addEventListener("click", () => { pmodalSecretsShown = !pmodalSecretsShown; syncPlatformMask(); });
  els.pmodalOk.addEventListener("click", submitPlatformModal);
  els.pmodalCancel.addEventListener("click", closePlatformModal);
  els.pmodal.addEventListener("click", (e) => { if (e.target === els.pmodal) closePlatformModal(); });

  els.gmodalOk.addEventListener("click", submitGroupModal);
  els.gmodalCancel.addEventListener("click", closeGroupModal);
  els.gmodal.addEventListener("click", (e) => { if (e.target === els.gmodal) closeGroupModal(); });
  els.gmodalName.addEventListener("keydown", (e) => { if (e.key === "Enter") submitGroupModal(); });

  els.btnAddPreset.addEventListener("click", () => openPresetModal("add", null));
  els.presetType.addEventListener("change", () => applyPresetType(els.presetType.value));
  els.presetOk.addEventListener("click", submitPresetModal);
  els.presetCancel.addEventListener("click", closePresetModal);
  els.presetModal.addEventListener("click", (e) => { if (e.target === els.presetModal) closePresetModal(); });
  els.presetDelete.addEventListener("click", () => {
    if (presetEditingId && confirm("Delete this fallback preset?")) send({ command: "remove_preset", id: presetEditingId });
  });

  // Files: із панелі Fallback presets і з самої модалки пресета.
  els.btnFiles.addEventListener("click", openFileManager);
  els.presetFiles.addEventListener("click", openFileManager);
  els.fmClose.addEventListener("click", closeFileManager);
  els.filesModal.addEventListener("click", (e) => { if (e.target === els.filesModal) closeFileManager(); });
  els.fmUp.addEventListener("click", () => listFiles(parentPath(browser.path)));
  els.fmRefresh.addEventListener("click", () => listFiles(browser.path));
  els.fmNewDir.addEventListener("click", askNewFolder);
  els.fmBrowse.addEventListener("click", () => els.fmFileInput.click());
  els.fmFileInput.addEventListener("change", () => {
    offerUpload(els.fmFileInput.files);
    els.fmFileInput.value = "";  // щоб той самий файл можна було обрати знову
  });
  for (const type of ["dragenter", "dragover"]) {
    els.fmDrop.addEventListener(type, (e) => {
      e.preventDefault();
      els.fmDrop.classList.add("dragging");
    });
  }
  for (const type of ["dragleave", "drop"]) {
    els.fmDrop.addEventListener(type, () => els.fmDrop.classList.remove("dragging"));
  }
  els.fmDrop.addEventListener("drop", (e) => {
    e.preventDefault();
    offerUpload(e.dataTransfer && e.dataTransfer.files);
  });
  // Промах повз drop-зону не має підмінити сторінку самим відеофайлом.
  for (const type of ["dragover", "drop"]) {
    document.addEventListener(type, (e) => e.preventDefault());
  }

  els.pickerUp.addEventListener("click", () => listFiles(parentPath(browser.path)));
  els.pickerOk.addEventListener("click", confirmPicker);
  els.pickerCancel.addEventListener("click", closePicker);
  els.pickerModal.addEventListener("click", (e) => { if (e.target === els.pickerModal) closePicker(); });
  els.pickerPath.addEventListener("keydown", (e) => { if (e.key === "Enter") confirmPicker(); });

  els.textModalOk.addEventListener("click", submitTextModal);
  els.textModalCancel.addEventListener("click", closeTextModal);
  els.textModal.addEventListener("click", (e) => { if (e.target === els.textModal) closeTextModal(); });
  els.textModalInput.addEventListener("keydown", (e) => { if (e.key === "Enter") submitTextModal(); });

  els.previewClose.addEventListener("click", closePreview);
  els.previewModal.addEventListener("click", (e) => { if (e.target === els.previewModal) closePreview(); });
  els.previewVideo.addEventListener("error", () => {
    // Формат, який рушій не декодує (типово mkv/ts/avi у Chromium/CEF).
    els.previewNote.textContent =
      "This browser cannot decode this file. It is still fine as fallback material -- "
      + "the controller transcodes it with ffmpeg, which is not limited the same way.";
    els.previewNote.hidden = false;
  });

  // Кожне поле шляху в модалці пресета отримує підказки + живу перевірку.
  for (const button of document.querySelectorAll("[data-browse]")) {
    const input = document.getElementById(button.dataset.browse);
    if (input) registerPathField(input);
  }
  els.presetType.addEventListener("change", revalidatePathFields);

  els.uploadAbort.addEventListener("click", abortUpload);
  els.uploadClose.addEventListener("click", () => { els.uploadModal.hidden = true; });
  els.uploadRetry.addEventListener("click", () => {
    const file = upload.file;
    els.uploadModal.hidden = true;
    if (file) offerUpload([file]);
  });

  // Browse біля кожного поля шляху в модалці пресета.
  document.addEventListener("click", (event) => {
    const button = event.target.closest("[data-browse]");
    if (!button) return;
    const input = document.getElementById(button.dataset.browse);
    if (input) openPicker(input, button.dataset.browseMode, button.dataset.browseTitle);
  });

  els.btnApply.addEventListener("click", saveSettings);

  // Instant toggle, not gated behind Apply like the System block above.
  els.fieldObsWidgetBitrate.addEventListener("change", () => {
    send({ command: "set_obs_widget_bitrate", value: els.fieldObsWidgetBitrate.checked });
  });



  RS.on("full", (message) => {
    for (const key of Object.keys(state)) delete state[key];
    Object.assign(state, message.data);
  });
  RS.on("delta", (message) => Object.assign(state, message.data));

  RS.on("settings", (message) => {
    settingsData = message.data;
    loadedSettings = message.data;
    populateSystemForm(message.data);
    renderSettingsLists();
    renderPresetsSettings();
    maybeOpenPendingEdit();  // auto-open edit after a successful create
  });
  RS.on("settings_saved", handleSettingsSaved);
  RS.on("source_result", (message) => handleResult("source", message));
  RS.on("platform_result", (message) => handleResult("platform", message));
  RS.on("group_result", handleGroupResult);
  RS.on("preset_result", handlePresetResult);
  RS.on("files", (message) => handleFilesListing(message.data));
  RS.on("files_result", handleFilesResult);
  RS.on("path_suggestions", handlePathSuggestions);
  RS.on("upload_check", handleUploadCheck);

  RS.onOpen(() => RS.sendCommand("get_settings"));
  // Розрив звʼязку вимикає кнопки списків -- їх треба перемалювати.
  RS.onConnectionChange(() => {
    if (settingsData) { renderSettingsLists(); renderPresetsSettings(); }
  });

  RS.connect();
})();
