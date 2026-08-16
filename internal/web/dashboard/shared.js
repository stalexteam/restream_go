/*
 * Спільний шар обох сторінок: транспорт /ws, тости, дрібні хелпери.
 * Сторінка сама реєструє потрібні їй повідомлення -- `RS.on(type, fn)`.
 * Індикатор WS є лише в доці, тож дотики до нього -- через withIndicator.
 */
window.RS = (() => {
  "use strict";

  const token = new URLSearchParams(location.search).get("token") || "";

  const RECONNECT_MIN_DELAY_MS = 1000;
  const RECONNECT_MAX_DELAY_MS = 15000;
  const TOAST_DURATION_MS = 5000;

  const TYPE_LABELS = { rtmp: "RTMP", srt: "SRT" };

  const toastContainer = document.getElementById("toast-container");
  const wsIndicator = document.getElementById("ws-indicator");

  function withIndicator(fn) {
    if (wsIndicator) fn(wsIndicator);
  }

  function wsLive() { return !document.body.classList.contains("ws-disconnected"); }

  // Sources/platforms carry `type` + separate `vod_track`/`enhanced_broadcasting`
  // checkboxes (RTMP-only) -- combine them into one label.
  function typeLabel(entry) {
    let label = TYPE_LABELS[entry.type] || entry.type;
    if (entry.vod_track) label += "+VOD";
    if (entry.enhanced_broadcasting) label += "+EB";
    return label;
  }

  // --- toasts ---

  const toastQueue = [];
  let currentToast = null;
  let toastTimer = null;
  let toastRemainingMs = 0;
  let toastTimerStartedAt = 0;

  function enqueueToast(level, text) {
    toastQueue.push({ level, text });
    if (!currentToast) showNextToast();
  }
  function showNextToast() {
    const next = toastQueue.shift();
    if (!next) return;
    const el = document.createElement("div");
    el.className = "toast toast-" + next.level;
    el.textContent = next.text;
    el.addEventListener("click", dismissCurrentToast);
    el.addEventListener("mouseenter", pauseToastTimer);
    el.addEventListener("mouseleave", resumeToastTimer);
    toastContainer.appendChild(el);
    currentToast = el;
    toastRemainingMs = TOAST_DURATION_MS;
    resumeToastTimer();
  }
  function pauseToastTimer() {
    clearTimeout(toastTimer);
    toastRemainingMs -= Date.now() - toastTimerStartedAt;
  }
  function resumeToastTimer() {
    toastTimerStartedAt = Date.now();
    toastTimer = setTimeout(dismissCurrentToast, Math.max(0, toastRemainingMs));
  }
  function dismissCurrentToast() {
    clearTimeout(toastTimer);
    if (currentToast) { currentToast.remove(); currentToast = null; }
    showNextToast();
  }

  // --- websocket ---

  let socket = null;
  let reconnectDelay = RECONNECT_MIN_DELAY_MS;
  let reconnectTimer = null;
  let pingToken = 0;
  let pingSentAt = 0;
  let pingTimeoutTimer = null;

  const handlers = new Map();   // тип повідомлення -> обробник сторінки
  const openHooks = [];         // що зробити на кожному (пере)підключенні
  const statusHooks = [];       // сторінка може перемалюватись на зміну зв'язку

  function wsUrl() {
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    return `${proto}//${location.host}/ws?token=${encodeURIComponent(token)}`;
  }

  // "live" has no static symbol -- once connected the badge shows the
  // round-trip ping (or "WS !" on a missed reply), driven by sendPing()/
  // handlePong() below instead of a fixed label. WS_HELP enumerates every
  // state the badge can be in -- prepended with the current one-liner, so
  // hovering explains both what's happening now and what else it could show.
  const WS_SYMBOL = { connecting: "WS ⋯", lost: "WS ✗" };
  const WS_TITLE = {
    connecting: "Connecting…",
    lost: "Link to the controller lost, retrying…",
  };
  const WS_HELP =
    "WS — this dashboard's live link to the controller (state pushes, toasts, commands):\n" +
    "• NNN ms — connected; round-trip time of the last ping\n" +
    "• WS ! — connected, but the last ping got no reply within 1s\n" +
    "• WS ⋯ — connecting…\n" +
    "• WS ✗ — link lost, retrying…";

  function setConnectionStatus(status) {
    document.body.classList.toggle("ws-disconnected", status !== "live");
    if (status === "live") {
      sendPing();  // fresh reading right away, don't wait for the next 1s tick
    } else {
      clearTimeout(pingTimeoutTimer);
      withIndicator((el) => {
        el.className = "indicator ind-" + status;
        el.textContent = WS_SYMBOL[status];
        el.title = WS_TITLE[status] + "\n\n" + WS_HELP;
      });
    }
    for (const hook of statusHooks) hook(status);
  }

  // --- WS latency ping (app-level; a missed reply doesn't mean the socket
  // itself is closed, so it never touches ws-disconnected/wsLive()) ---

  function sendPing() {
    // Пінг потрібен лише індикатору.
    if (!wsIndicator || !wsLive()) return;
    pingToken += 1;
    const current = pingToken;
    pingSentAt = performance.now();
    if (!send({ command: "ping", t: current })) return;
    clearTimeout(pingTimeoutTimer);
    pingTimeoutTimer = setTimeout(() => {
      if (current === pingToken) setPingBadge(null);
    }, 999);
  }

  function handlePong(replyToken) {
    if (replyToken !== pingToken) return;  // stale reply (superseded by a newer ping)
    clearTimeout(pingTimeoutTimer);
    const ms = Math.max(0, Math.min(999, Math.round(performance.now() - pingSentAt)));
    setPingBadge(ms);
  }

  // Padding zeros ("0" in "015") are dimmed -- only the significant digits
  // (and the always-kept last digit, e.g. the "0" in "000") stay in the
  // indicator's normal color, so the number reads at a glance.
  function splitLeadingZeros(padded) {
    let i = 0;
    while (i < padded.length - 1 && padded[i] === "0") i++;
    return { zeros: padded.slice(0, i), rest: padded.slice(i) };
  }

  function setPingBadge(ms) {
    withIndicator((el) => {
      if (ms === null) {
        el.className = "indicator ind-warn";
        el.textContent = "WS !";
        el.title = "No reply to the last ping within 1s.\n\n" + WS_HELP;
        return;
      }
      el.className = "indicator ind-live";
      el.textContent = "";
      const { zeros, rest } = splitLeadingZeros(String(ms).padStart(3, "0"));
      if (zeros) {
        const dim = document.createElement("span");
        dim.className = "ping-zero";
        dim.textContent = zeros;
        el.appendChild(dim);
      }
      el.appendChild(document.createTextNode(`${rest} ms`));
      el.title = `Round-trip: ${ms} ms.\n\n` + WS_HELP;
    });
  }

  function connect() {
    clearTimeout(reconnectTimer);
    setConnectionStatus("connecting");
    socket = new WebSocket(wsUrl());
    socket.onopen = () => {
      reconnectDelay = RECONNECT_MIN_DELAY_MS;
      setConnectionStatus("live");
      for (const hook of openHooks) hook();
    };
    socket.onmessage = (event) => {
      let message;
      try { message = JSON.parse(event.data); } catch (e) { return; }
      if (message.type === "pong") {
        handlePong(message.t);
        return;
      }
      if (message.type === "event") {
        enqueueToast(message.level, message.text);
        return;
      }
      const handler = handlers.get(message.type);
      if (handler) handler(message);
    };
    socket.onclose = scheduleReconnect;
    socket.onerror = () => socket && socket.close();
  }

  function scheduleReconnect() {
    setConnectionStatus("lost");
    const seconds = Math.round(reconnectDelay / 1000);
    withIndicator((el) => {
      el.title = `Link to the controller lost, retrying in ${seconds}s…\n\n` + WS_HELP;
    });
    reconnectTimer = setTimeout(connect, reconnectDelay);
    reconnectDelay = Math.min(reconnectDelay * 2, RECONNECT_MAX_DELAY_MS);
  }

  function send(payload) {
    if (socket && socket.readyState === WebSocket.OPEN) {
      socket.send(JSON.stringify(payload));
      return true;
    }
    return false;
  }
  function sendCommand(command) { send({ command }); }

  // --- дрібні хелпери, потрібні обом сторінкам ---

  function formatDuration(totalSeconds) {
    const seconds = Math.max(0, Math.floor(totalSeconds));
    const h = Math.floor(seconds / 3600);
    const m = Math.floor((seconds % 3600) / 60);
    const s = seconds % 60;
    const pad = (n) => String(n).padStart(2, "0");
    return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${m}:${pad(s)}`;
  }

  function cssEscape(s) {
    return (window.CSS && CSS.escape) ? CSS.escape(s) : String(s).replace(/["\\]/g, "\\$&");
  }

  function showErrorsInto(container, errors) {
    container.innerHTML = "";
    for (const [field, text] of Object.entries(errors)) {
      const line = document.createElement("div");
      line.textContent = field === "_" ? text : `${field}: ${text}`;
      container.appendChild(line);
    }
    container.hidden = Object.keys(errors).length === 0;
  }

  function appendStar(el, hint) {
    const star = document.createElement("span");
    star.className = "role-star";
    star.textContent = "★";
    star.title = hint;
    el.append(" ", star);
  }
  function setNameWithStar(el, name, starHint) {
    el.textContent = name;
    if (starHint) appendStar(el, starHint);
  }

  return {
    token,
    connect,
    send,
    sendCommand,
    on: (type, handler) => handlers.set(type, handler),
    onOpen: (hook) => openHooks.push(hook),
    onConnectionChange: (hook) => statusHooks.push(hook),
    toast: enqueueToast,
    wsLive,
    typeLabel,
    formatDuration,
    cssEscape,
    showErrorsInto,
    appendStar,
    setNameWithStar,
    sendPing,
    MAX_TRACK_SLOTS: 6,  // Platform.audio_map -- output slots for an SRT-custom platform
  };
})();
