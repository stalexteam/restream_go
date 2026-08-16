/*
 * Док OBS: Status + Control. Налаштування живуть окремою сторінкою
 * (/config, config.js) -- док не тягне ані їхній код, ані їхні стилі,
 * і залишається легким, бо висить в OBS постійно.
 */
(() => {
  "use strict";

  const send = RS.send;
  const wsLive = RS.wsLive;
  const formatDuration = RS.formatDuration;
  const cssEscape = RS.cssEscape;
  const setNameWithStar = RS.setNameWithStar;
  const typeLabel = RS.typeLabel;
  const enqueueToast = RS.toast;

  const state = {};            // snapshot: {sources, platforms, groups, session, manual_halt, components, obs_source_connected}

  const LIVE_MIN_SEC = 3;

  const els = {
    sourceIndicator: document.getElementById("source-indicator"),
    obsIndicator: document.getElementById("obs-indicator"),
    broadcastIndicator: document.getElementById("broadcast-indicator"),
    haltBtn: document.getElementById("halt-btn"),
    settingsBtn: document.getElementById("settings-btn"),
    configOverlay: document.getElementById("config-overlay"),
    configFrame: document.getElementById("config-frame"),
    configBack: document.getElementById("config-back"),
    sourcesBody: document.querySelector("#sources-table tbody"),
    componentsBody: document.querySelector("#components-table tbody"),
    controlGroups: document.getElementById("control-groups"),
    tabButtons: document.querySelectorAll(".tab-button[data-tab]"),
    tabStatus: document.getElementById("tab-status"),
    tabControl: document.getElementById("tab-control"),
    tplGroupControl: document.getElementById("group-control-template"),
  };

  function sources() { return state.sources || []; }
  function platforms() { return state.platforms || []; }
  function groups() { return state.groups || []; }
  function session() { return state.session || { state: "OFFLINE", fallback_deadline: null }; }
  function defaultSource() { return sources().find((s) => s.is_default) || null; }

  // Значок драбини Enhanced Broadcasting -- один і той самий для source
  // у Status і для плеча в Control. Приглушений, поки драбини на проводі
  // ще не бачили (увімкнена, але не підтверджена).
  function appendEbBadge(el, hint, observed) {
    const badge = document.createElement("span");
    badge.className = observed ? "pf-badge" : "pf-badge pf-badge-idle";
    badge.textContent = "EB";
    badge.title = hint;
    el.append(" ", badge);
  }

  // Broadcast badge = what's actually going OUT, aggregated over platforms.
  function sessionBadge() {
    const ps = platforms();
    const anyLive = ps.some((p) => p.enabled && p.gate && !p.failed && p.state === "LIVE");
    const anyBackup = ps.some((p) => p.enabled && p.gate && p.state === "FALLBACK");
    if (anyLive) return { text: "ON AIR", cls: "bstate-live" };
    if (anyBackup) return { text: "BACKUP", cls: "bstate-fallback" };
    if (session().state !== "OFFLINE" || ps.some((p) => p.state !== "OFFLINE")) {
      return { text: "IDLE", cls: "bstate-idle" };
    }
    if (state.manual_halt) return { text: "HALTED", cls: "bstate-halt" };
    if (ps.some((p) => p.halted)) return { text: "FAILURE", cls: "bstate-halt" };
    return { text: "OFFLINE", cls: "bstate-offline" };
  }

  // --- render ---

  function render() {
    renderSourceIndicator();
    renderHeaderObs();
    renderHeaderBadge();
    renderStatusSources();
    renderComponents();
    renderControlGroups();
    updateHaltButton();
  }

  function updateHaltButton() {
    const broadcasting = session().state !== "OFFLINE"
      || platforms().some((p) => p.state === "LIVE" || p.state === "FALLBACK");
    els.haltBtn.disabled = !(wsLive() && broadcasting);
  }

  const SRC_HELP =
    "SRC — whether the OBS browser-source (obs-source.html) is connected to this controller; needed for Start/Stop detection and remote HALT:\n" +
    "• SRC ✓ — connected\n" +
    "• SRC ✗ — not connected (grey is normal in a plain browser tab; only turns green once added as a Browser Source in OBS)";

  function renderSourceIndicator() {
    const connected = !!state.obs_source_connected;
    els.sourceIndicator.className = "indicator ind-" + (connected ? "live" : "connecting");
    els.sourceIndicator.textContent = connected ? "SRC ✓" : "SRC ✗";
    const current = connected
      ? "Connected -- Start/Stop detection and remote HALT work."
      : "Not connected -- Start/Stop detection and telling OBS to stop won't work.";
    els.sourceIndicator.title = current + "\n\n" + SRC_HELP;
  }

  // Video/audio parts of a platform's measured input (its relay of a source).
  function obsParts(obs) {
    const vparts = [];
    if (obs.width && obs.height) vparts.push(`${obs.width}×${obs.height}@${obs.fps || "?"}`);
    if (obs.video_codec) vparts.push(obs.video_codec);
    if (obs.video_kbps != null) vparts.push(`${obs.video_kbps} kbps`);
    const aparts = [];
    if (obs.audio_codec) aparts.push(obs.audio_codec);
    if (obs.audio_kbps != null) aparts.push(`${obs.audio_kbps} kbps`);
    return { vparts, aparts };
  }

  // First platform with flowing input for this source -- the measured stats
  // live in each platform's relay, not on the source itself.
  function flowingPlatformOf(sourceName) {
    return platforms().find((p) => p.source === sourceName && p.obs && p.obs.flowing) || null;
  }

  const OBS_HELP =
    "OBS — whether video from the DEFAULT source is currently flowing into the VPS:\n" +
    "• OBS ✓ — flowing normally\n" +
    "• OBS ✗ (grey) — no input yet, OBS hasn't started streaming to this VPS\n" +
    "• OBS ✗ (red) — input dropped mid-stream; the backup video is playing on its platforms";

  // Header OBS indicator: is the DEFAULT source feeding the VPS.
  function renderHeaderObs() {
    const src = defaultSource();
    const plat = src ? flowingPlatformOf(src.name) : null;
    const flowing = !!plat || !!(src && src.available && src.validated);
    let status, symbol, current;
    if (flowing) {
      status = "live"; symbol = "OBS ✓"; current = "Video input into the VPS: flowing.";
    } else if (session().state === "FALLBACK") {
      status = "lost"; symbol = "OBS ✗"; current = "Video input into the VPS: none (OBS dropped, showing backup video).";
    } else {
      status = "connecting"; symbol = "OBS ✗"; current = "No video input from OBS into the VPS yet.";
    }
    if (plat) {
      const { vparts, aparts } = obsParts(plat.obs);
      if (vparts.length || aparts.length) {
        current += "\n" + [vparts.join(" · "), aparts.join(" · ")].filter(Boolean).join(" | ");
      }
    }
    els.obsIndicator.className = "indicator ind-" + status;
    els.obsIndicator.textContent = symbol;
    els.obsIndicator.title = current + "\n\n" + OBS_HELP;
  }

  function renderHeaderBadge() {
    const info = sessionBadge();
    els.broadcastIndicator.className = "indicator " + info.cls;
    els.broadcastIndicator.textContent = info.text;
  }

  // Tracks as read back from the wire (never from onMetaData -- it only ever
  // describes the default track). One line per track: number/name first, then
  // its parameters. Bitrate is deliberately NOT here: it is measured once, in
  // the probe at publication start, so it would sit frozen pretending to be
  // live (the header's OBS indicator carries the live figure instead).
  function videoTrackLines(source, media) {
    const ladder = (media && media.video_tracks_actual) || [];
    if (ladder.length) {
      return ladder.map(
        (t) => [`#${t.index + 1}`, `${t.width}×${t.height} @${t.fps || "?"}`, t.codec].join(" · "));
    }
    if (!media) return [];
    return [`#1 · ${media.width}×${media.height} @${media.fps || "?"} · ${media.video_codec}`];
  }
  // "#2" for a plain numbered track, "#1 Live" when the source names them
  // (VOD Track pair) -- number first either way.
  function audioTrackName(labels, index) {
    const number = `#${index + 1}`;
    const label = labels[index] || "";
    return (!label || label === number) ? number : `${number} ${label.replace(/^#/, "")}`;
  }
  function audioTrackLines(source, media) {
    const labels = source.track_labels || [];
    const detail = (media && media.audio_tracks_detail) || [];
    if (detail.length) {
      return detail.map((t) => {
        const parts = [audioTrackName(labels, t.index), t.codec];
        if (t.sample_rate) parts.push(`${Math.round(t.sample_rate / 100) / 10} kHz`);
        if (t.channels) parts.push(`${t.channels}ch`);
        return parts.join(" · ");
      });
    }
    if (!media) return [];
    const parts = [audioTrackName(labels, 0), media.audio_codec];
    if (media.sample_rate) parts.push(`${Math.round(media.sample_rate / 100) / 10} kHz`);
    if (media.channels) parts.push(`${media.channels}ch`);
    return [parts.join(" · ")];
  }
  function fillLines(cell, lines) {
    cell.textContent = "";
    if (!lines.length) { cell.textContent = "–"; return; }
    lines.forEach((line, i) => {
      if (i) cell.appendChild(document.createElement("br"));
      cell.appendChild(document.createTextNode(line));
    });
  }

  function renderStatusSources() {
    els.sourcesBody.innerHTML = "";
    for (const s of sources()) {
      const row = document.createElement("tr");
      const tdName = document.createElement("td");
      setNameWithStar(tdName, s.name, s.is_default ? "Default source" : null);
      tdName.title = typeLabel(s);
      if (s.enhanced_broadcasting) {
        const rungs = ((s.media && s.media.video_tracks_actual) || []).length;
        appendEbBadge(
          tdName,
          rungs
            ? `Enhanced Broadcasting — ${rungs} rung(s) arriving on the wire`
            : "Enhanced Broadcasting is enabled, but no ladder has arrived yet",
          rungs > 0);
      }

      const tdState = document.createElement("td");
      if (s.contract_error) {
        // Останнє підключення провалило контракт: посилання на Settings.
        const link = document.createElement("span");
        link.className = "src-fix";
        link.textContent = "Settings";
        link.title = s.contract_error;
        link.addEventListener("click", () => openConfigWindow());
        tdState.appendChild(link);
      } else if (s.available && s.validated && s.available_since) {
        tdState.className = "status-up src-uptime";
        tdState.dataset.since = String(s.available_since);
        tdState.textContent = formatDuration(Date.now() / 1000 - s.available_since);
      } else if (s.available) {
        tdState.className = "status-down";
        tdState.textContent = "checking…";
      } else {
        tdState.className = "status-down";
        tdState.textContent = "Offline";
      }

      const m = s.media;
      const tdVideo = document.createElement("td");
      tdVideo.className = "track-cell";
      fillLines(tdVideo, videoTrackLines(s, m));
      const tdAudio = document.createElement("td");
      tdAudio.className = "track-cell";
      fillLines(tdAudio, audioTrackLines(s, m));
      if (m) tdAudio.title = `${m.audio_tracks_actual}/${s.audio_tracks} audio track(s)`;

      row.append(tdName, tdState, tdVideo, tdAudio);
      els.sourcesBody.appendChild(row);
    }
  }

  function componentOrder() {
    const order = ["mediamtx", "controller", "fallback-preparer"];
    for (const p of platforms()) {
      order.push(`relay:${p.name}`, `backup:${p.name}`);
    }
    return order;
  }

  function renderComponents() {
    const components = state.components || {};
    els.componentsBody.innerHTML = "";
    for (const name of componentOrder()) {
      const c = components[name];
      if (!c) continue;
      const row = document.createElement("tr");
      // Background work (fallback preparation) reports a share of the job
      // instead of running/stopped -- it has no process of its own.
      const cells = [
        name,
        c.status || (c.running ? "running" : "stopped"),
        c.pid != null ? String(c.pid) : "–",
        c.cpu_percent != null ? `${c.cpu_percent}%` : "–",
        c.rss_mb != null ? `${c.rss_mb} MB` : "–",
      ];
      cells.forEach((text, i) => {
        const td = document.createElement("td");
        td.textContent = text;
        if (i === 1) {
          td.className = (c.running || c.status === "ready") ? "status-up" : "status-down";
          if (c.detail) td.title = c.detail;
        }
        row.appendChild(td);
      });
      els.componentsBody.appendChild(row);
    }
  }

  function platformStatus(p) {
    if (!p.enabled) return { text: "Disabled", cls: "pill-off" };
    // Group gate off: the platform is checked but suppressed group-wide.
    if (!p.gate) return { text: "Muted", cls: "pill-off" };
    if (p.failed) return { text: "Failed", cls: "pill-failed" };
    const o = p.output || {};
    if (o.running) {
      if ((o.uptime_sec || 0) < LIVE_MIN_SEC) return { text: "Connecting", cls: "pill-connecting" };
      return p.state === "FALLBACK"
        ? { text: "Backup", cls: "pill-fallback" }
        : { text: "Live", cls: "pill-live" };
    }
    return { text: "Offline", cls: "pill-off" };
  }
  function platformHealth(p) {
    const o = p.output || {};
    if (!p.enabled || !o.running || o.dropped == null) return { text: "–", cls: "" };
    if (o.behind) return { text: `behind (${o.dropped} drops)`, cls: "status-down" };
    if (o.dropped > 0) return { text: `${o.dropped} drops`, cls: "" };
    return { text: "OK", cls: "status-up" };
  }

  // Control tab: one panel per group, keyed (so a just-clicked toggle isn't
  // clobbered by a delta). Rows keyed by platform name. Порожні групи не
  // показуємо (панель без рядків -- лише шум).
  function renderControlGroups() {
    const live = wsLive();
    const seenGroups = new Set();
    for (const g of groups()) {
      if (!platforms().some((p) => p.group === g.id)) continue;
      seenGroups.add(g.id);
      let panel = els.controlGroups.querySelector(`.group-panel[data-group="${cssEscape(g.id)}"]`);
      if (!panel) {
        const frag = els.tplGroupControl.content.cloneNode(true);
        panel = frag.querySelector(".group-panel");
        panel.dataset.group = g.id;
        els.controlGroups.appendChild(frag);
      }
      setNameWithStar(panel.querySelector(".section-title"), g.name, g.is_default ? "Default group" : null);

      const toggle = panel.querySelector(".group-toggle");
      if (toggle) {
        if (document.activeElement !== toggle) toggle.checked = g.enabled;
        toggle.disabled = !live;
        if (!toggle.dataset.bound) {
          toggle.dataset.bound = "1";
          toggle.addEventListener("change", () => {
            send({ command: toggle.checked ? "enable_group" : "disable_group", id: panel.dataset.group });
          });
        }
      }

      renderControlRows(panel.querySelector("tbody"), g, live);
    }
    // drop panels for removed groups
    for (const panel of Array.from(els.controlGroups.children)) {
      if (!seenGroups.has(panel.dataset.group)) panel.remove();
    }
    // Keep panel order in sync with the group order (default first). Panels
    // are reused across renders, so a changed order wouldn't otherwise
    // reflow; move only misplaced nodes.
    let expected = els.controlGroups.firstElementChild;
    for (const g of groups()) {
      const panel = els.controlGroups.querySelector(`.group-panel[data-group="${cssEscape(g.id)}"]`);
      if (!panel) continue;
      if (panel === expected) expected = expected.nextElementSibling;
      else els.controlGroups.insertBefore(panel, expected);
    }
  }

  function renderControlRows(tbody, group, live) {
    const members = platforms().filter((p) => p.group === group.id);
    const seen = new Set();
    for (const p of members) {
      seen.add(p.name);
      let row = tbody.querySelector(`tr[data-name="${cssEscape(p.name)}"]`);
      if (!row) {
        row = document.createElement("tr");
        row.dataset.name = p.name;
        const tdName = document.createElement("td");
        tdName.className = "cell-name";
        const cb = document.createElement("input");
        cb.type = "checkbox";
        cb.className = "control-toggle";
        cb.addEventListener("change", () => {
          send({ command: cb.checked ? "enable_platform" : "disable_platform", name: p.name });
        });
        const nameSpan = document.createElement("span");
        nameSpan.className = "pf-label";
        tdName.append(cb, nameSpan);
        const tdStatus = document.createElement("td");
        const tdUptime = document.createElement("td");
        const tdHealth = document.createElement("td");
        const tdPing = document.createElement("td");
        row.append(tdName, tdStatus, tdUptime, tdHealth, tdPing);
        tbody.appendChild(row);
      }
      const [tdName, tdStatus, tdUptime, tdHealth, tdPing] = row.children;
      const cb = tdName.querySelector(".control-toggle");
      if (document.activeElement !== cb) cb.checked = p.enabled;
      cb.disabled = !live;
      const label = tdName.querySelector(".pf-label");
      label.textContent = p.name;
      // Це плече несе всю драбину -- його заглушка кодується в кожну
      // сходинку, тож готова пізніше за інші.
      if (p.eb) {
        appendEbBadge(
          label,
          "Enhanced Broadcasting passthrough — the whole ladder goes out, "
          + "and the fallback video is re-encoded into every rung",
          true);
      }
      const st = platformStatus(p);
      tdStatus.innerHTML = "";
      const pill = document.createElement("span");
      pill.className = "pill " + st.cls;
      pill.textContent = st.text;
      tdStatus.appendChild(pill);
      const o = p.output || {};
      tdUptime.textContent = o.running && o.uptime_sec ? formatDuration(o.uptime_sec) : "–";
      const h = platformHealth(p);
      tdHealth.textContent = h.text;
      tdHealth.className = h.cls;
      tdPing.textContent = o.rtt_ms != null ? `${o.rtt_ms} ms` : "–";
    }
    for (const row of Array.from(tbody.children)) {
      if (!seen.has(row.dataset.name)) row.remove();
    }
  }

  // --- clocks (fallback countdown on the header badge tooltip) ---

  const BADGE_HELP =
    "Broadcast — what's actually going out to the platforms:\n" +
    "• ON AIR — streaming live to at least one enabled platform\n" +
    "• BACKUP — a source dropped; the backup video is playing on its platforms\n" +
    "• IDLE — a source is publishing, but nothing is going out (no platform or group is enabled)\n" +
    "• OFFLINE — nothing is streaming\n" +
    "• FAILURE — stopped: none of the enabled platforms could be reached\n" +
    "• HALTED — stopped from the dashboard; won't auto-restart until OBS is stopped and started again";

  function tickClocks() {
    RS.sendPing();
    for (const td of document.querySelectorAll(".src-uptime")) {
      td.textContent = formatDuration(Date.now() / 1000 - Number(td.dataset.since));
    }
    let title = BADGE_HELP;
    const deadline = session().fallback_deadline;
    if (deadline) {
      const remaining = deadline - Date.now() / 1000;
      const line = remaining > 0
        ? `Stopping in ${formatDuration(remaining)} if OBS doesn't reconnect.`
        : "Stopping now…";
      title = line + "\n\n" + BADGE_HELP;
    }
    els.broadcastIndicator.title = title;
  }

  // --- tabs ---

  function switchTab(name) {
    els.tabButtons.forEach((btn) => btn.classList.toggle("active", btn.dataset.tab === name));
    els.tabStatus.hidden = name !== "status";
    els.tabControl.hidden = name !== "control";
  }
  els.tabButtons.forEach((btn) => btn.addEventListener("click", () => switchTab(btn.dataset.tab)));
  els.obsIndicator.addEventListener("click", () => switchTab("status"));

  // --- налаштування ---

  // Ознаку ставить обгортка obs-dock.html (window.obsstudio видно лише
  // головному фрейму). Окреме вікно доречне тільки в доці, де адресного
  // рядка немає; у браузері воно світило б URL із токеном.
  const inOBS = new URLSearchParams(location.search).get("obs") === "1";

  function configUrl() {
    return `${location.origin}/config?token=${encodeURIComponent(RS.token)}`;
  }

  function openConfigInline() {
    els.configFrame.src = configUrl();
    els.configOverlay.hidden = false;
  }

  function closeConfigInline() {
    els.configOverlay.hidden = true;
    // Знімаємо сторінку, щоб вона не тримала друге /ws-з'єднання.
    els.configFrame.src = "about:blank";
  }

  function openConfigWindow() {
    if (!inOBS) {
      openConfigInline();
      return;
    }
    let opened = null;
    try {
      opened = window.open(configUrl(), "restream-config", "popup=yes,width=1100,height=900");
    } catch (e) {
      opened = null;
    }
    // Popup заборонений -- вбудований варіант працює завжди.
    if (!opened) {
      openConfigInline();
      return;
    }
    try { opened.focus(); } catch (e) { /* фокус -- не обов'язковий */ }
  }

  // --- listeners ---

  els.settingsBtn.addEventListener("click", openConfigWindow);
  els.configBack.addEventListener("click", closeConfigInline);
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape" && !els.configOverlay.hidden) closeConfigInline();
  });

  els.haltBtn.addEventListener("click", () => {
    if (confirm("Halt the broadcast now? This stops streaming to all platforms and tells OBS to stop streaming.")) {
      send({ command: "halt" });
    }
  });

  RS.on("full", (message) => {
    for (const key of Object.keys(state)) delete state[key];
    Object.assign(state, message.data);
    render();
  });
  RS.on("delta", (message) => {
    Object.assign(state, message.data);
    render();
  });
  RS.onConnectionChange(render);

  RS.connect();
  setInterval(tickClocks, 1000);
})();
