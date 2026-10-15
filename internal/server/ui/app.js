// mutti's web UI: the instances of this machine, kept current by server-sent events.
// Everything that comes from the API reaches the page as text (textContent), never as markup:
// log lines and branch names are not ours. Only the fixed icons below are markup.
"use strict";

const icons = {
  open: '<svg viewBox="0 0 24 24"><path d="M14 4h6v6M20 4l-9 9M18 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h5"/></svg>',
  play: '<svg viewBox="0 0 24 24"><path d="M7 5v14l11-7z"/></svg>',
  stop: '<svg viewBox="0 0 24 24"><rect x="6" y="6" width="12" height="12" rx="2"/></svg>',
  alert: '<svg viewBox="0 0 24 24"><path d="M12 3 2 20h20L12 3zM12 10v4M12 17v.5"/></svg>',
  restart: '<svg viewBox="0 0 24 24"><path d="M20 12a8 8 0 1 1-2.34-5.66M20 4v5h-5"/></svg>',
  trash: '<svg viewBox="0 0 24 24"><path d="M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3"/></svg>',
  copy: '<svg viewBox="0 0 24 24"><rect x="8" y="8" width="12" height="12" rx="2"/><path d="M16 8V5a1 1 0 0 0-1-1H5a1 1 0 0 0-1 1v10a1 1 0 0 0 1 1h3"/></svg>',
  branch: '<svg viewBox="0 0 24 24"><circle cx="6" cy="5" r="2"/><circle cx="6" cy="19" r="2"/><circle cx="18" cy="7" r="2"/><path d="M6 7v10M18 9a6 6 0 0 1-6 6H6"/></svg>',
  logs: '<svg viewBox="0 0 24 24"><path d="M4 6h16M4 12h16M4 18h10"/></svg>',
};

const state = {
  list: null, // instances, null until loaded
  project: "", // "" for all
  query: "",
  selected: "", // path of the instance in the drawer
  tab: "services",
  busy: new Map(), // path -> action running
  events: [], // newest first
};

const $ = (id) => document.getElementById(id);

function el(tag, props = {}, ...children) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (k === "class") e.className = v;
    else if (k === "text") e.textContent = v;
    else if (k === "icon") e.innerHTML = icons[v];
    else if (k.startsWith("on")) e.addEventListener(k.slice(2), v);
    else if (v !== undefined && v !== null && v !== false) e.setAttribute(k, v === true ? "" : v);
  }
  for (const c of children) if (c !== null && c !== undefined) e.append(c);
  return e;
}

function iconSpan(name) {
  return el("span", { icon: name }).firstChild;
}

// api calls the JSON API and returns its data, or throws its error with code and hint.
async function api(path, options) {
  const res = await fetch(path, options);
  const body = await res.json();
  if (body.error) {
    const err = new Error(body.error.message);
    err.code = body.error.code;
    err.hint = body.error.hint;
    throw err;
  }
  return body.data;
}

const q = encodeURIComponent;

// ---- Instances

function worktreeName(s) {
  if (s.name === s.project) return s.name;
  return s.name.startsWith(s.project + "-") ? s.name.slice(s.project.length + 1) : s.name;
}

function statusOf(s) {
  if (state.busy.has(s.path)) return "busy";
  return s.orphaned ? "orphaned" : s.status;
}

const busyText = { up: "Starting…", stop: "Stopping…", down: "Removing…" };
const statusText = { running: "Running", partial: "Partly running", stopped: "Stopped", down: "Down", orphaned: "Worktree gone" };

function describe(s) {
  return state.busy.has(s.path) ? busyText[state.busy.get(s.path)] : statusText[statusOf(s)] || statusOf(s);
}

async function loadInstances() {
  try {
    state.list = await api("/api/instances");
  } catch (err) {
    toast("error", "Cannot list the instances", err);
    state.list = state.list || [];
  }
  render();
}

function matches(s) {
  if (state.project && s.project !== state.project) return false;
  if (!state.query) return true;
  const hay = [s.name, s.project, s.branch, s.path, ...(s.urls || [])].join(" ").toLowerCase();
  return state.query.split(/\s+/).every((w) => hay.includes(w));
}

function render() {
  if (state.list === null) return;
  renderProjects();
  const shown = state.list.filter(matches);
  const running = state.list.filter((s) => s.status === "running").length;
  $("title").textContent = state.project || "All instances";
  $("summary").textContent = state.list.length
    ? `${state.list.length} instance${state.list.length === 1 ? "" : "s"} · ${running} running`
    : "";
  $("empty").hidden = state.list.length > 0;
  $("nomatch").hidden = state.list.length === 0 || shown.length > 0;

  const groups = new Map();
  for (const s of shown) {
    if (!groups.has(s.project)) groups.set(s.project, []);
    groups.get(s.project).push(s);
  }
  $("grid").replaceChildren(
    ...[...groups].map(([project, list]) =>
      el(
        "section",
        { class: "group" },
        el("div", { class: "group-head" }, el("h2", { text: project }), el("span", { text: `${list.length} worktree${list.length === 1 ? "" : "s"}` })),
        el("div", { class: "cards" }, ...list.map(card)),
      ),
    ),
  );
  if (state.selected) renderDrawerHead();
}

function renderProjects() {
  const counts = new Map();
  for (const s of state.list) counts.set(s.project, (counts.get(s.project) || 0) + 1);
  const item = (label, project, count) =>
    el(
      "li",
      {},
      el(
        "button",
        {
          "aria-current": String(state.project === project),
          onclick: () => {
            state.project = project;
            render();
          },
        },
        el("span", { text: label }),
        el("span", { class: "count", text: String(count) }),
      ),
    );
  $("projects").replaceChildren(
    item("All instances", "", state.list.length),
    ...[...counts].sort(([a], [b]) => a.localeCompare(b)).map(([p, n]) => item(p, p, n)),
  );
}

function card(s) {
  const status = statusOf(s);
  const busy = state.busy.has(s.path);
  const pct = s.total ? Math.round((100 * s.running) / s.total) : 0;
  const urls = s.urls || [];
  const up = s.status === "running" || s.status === "partial";

  const actions = el("div", { class: "card-actions" });
  if (up && urls.length) {
    actions.append(el("a", { class: "btn primary", href: urls[0], target: "_blank", rel: "noopener", onclick: (e) => e.stopPropagation() }, iconSpan("open"), "Open"));
  }
  if (!s.orphaned) {
    actions.append(
      up
        ? el("button", { class: "btn", disabled: busy, onclick: (e) => act(e, "stop", s) }, iconSpan("stop"), "Stop")
        : el("button", { class: "btn primary", disabled: busy, onclick: (e) => act(e, "up", s) }, iconSpan("play"), "Start"),
    );
  }
  actions.append(el("button", { class: "btn", onclick: (e) => { e.stopPropagation(); openDrawer(s.path, "logs"); } }, iconSpan("logs"), "Logs"));

  return el(
    "article",
    {
      class: `card s-${status}${state.selected === s.path ? " selected" : ""}`,
      tabindex: "0",
      onclick: () => openDrawer(s.path),
      onkeydown: (e) => e.key === "Enter" && openDrawer(s.path),
    },
    el(
      "div",
      { class: "card-head" },
      el("span", { class: "dot" }),
      el("h3", { text: worktreeName(s), title: s.name }),
      s.branch ? el("span", { class: "chip", title: s.branch }, iconSpan("branch"), el("span", { text: s.branch })) : null,
    ),
    el("p", { class: "card-path", text: s.path, title: s.path }),
    el(
      "div",
      { class: "meter" },
      el("div", { class: "meter-bar" }, el("span", { style: `width:${busy ? 100 : pct}%` })),
      el("div", { class: "meter-label" }, el("b", { text: describe(s) }), el("span", { text: s.orphaned ? "" : `${s.running} of ${s.total} services` })),
    ),
    urls.length
      ? el(
          "div",
          { class: "card-url" },
          el("a", { href: urls[0], target: "_blank", rel: "noopener", text: urls[0].replace(/^https?:\/\//, ""), onclick: (e) => e.stopPropagation() }),
          urls.length > 1 ? el("span", { class: "more", text: `+${urls.length - 1} more` }) : null,
        )
      : el("div", { class: "card-url none", text: "No HTTP routes" }),
    actions,
  );
}

// ---- Actions

async function act(event, action, s) {
  event?.stopPropagation();
  if (action === "down") {
    const ok = await confirmDialog(`Remove ${s.name}?`, "Its containers and networks are removed. Its data stays, and Start brings it back.", "Remove");
    if (!ok) return;
  }
  state.busy.set(s.path, action);
  render();
  try {
    const done = await api(`/api/${action}?dir=${q(s.path)}`, { method: "POST" });
    const what = { up: "is up", stop: "is stopped", down: "is removed" }[action];
    toast("ok", `${done.instance} ${what}`, done.url ? { message: done.url } : null);
  } catch (err) {
    toast("error", `Could not ${action === "up" ? "start" : action === "stop" ? "stop" : "remove"} ${s.name}`, err);
  }
  state.busy.delete(s.path);
  await loadInstances();
  if (state.selected === s.path && state.tab === "services") loadServices();
}

function confirmDialog(title, text, okLabel) {
  const d = $("confirm");
  $("c-title").textContent = title;
  $("c-text").textContent = text;
  $("c-ok").textContent = okLabel;
  d.returnValue = "";
  d.showModal();
  return new Promise((resolve) => d.addEventListener("close", () => resolve(d.returnValue === "ok"), { once: true }));
}

function toast(kind, title, detail) {
  const box = el(
    "div",
    { class: `toast ${kind}` },
    el(
      "div",
      {},
      el("strong", { text: title }),
      detail?.message ? el("p", { text: detail.message.split("\n\n")[0] }) : null,
      detail?.hint ? el("p", { class: "hint", text: "→ " + detail.hint }) : null,
    ),
  );
  $("toasts").append(box);
  setTimeout(() => box.remove(), kind === "error" ? 12000 : 4000);
}

async function copy(text) {
  try {
    await navigator.clipboard.writeText(text);
    toast("ok", "Copied", { message: text });
  } catch {
    toast("error", "Cannot copy", { message: text });
  }
}

// ---- Drawer

function selectedInstance() {
  return state.list?.find((s) => s.path === state.selected);
}

function openDrawer(path, tab = "services") {
  const changed = state.selected !== path;
  state.selected = path;
  $("drawer").hidden = false;
  if (changed) {
    $("tab-services").replaceChildren();
    $("l-service").replaceChildren();
    lastLogs = "";
  }
  render();
  selectTab(tab);
}

function closeDrawer() {
  state.selected = "";
  $("drawer").hidden = true;
  stopFollow();
  render();
}

function renderDrawerHead() {
  const s = selectedInstance();
  if (!s) return closeDrawer();
  $("d-dot").className = `dot s-${statusOf(s)}`;
  $("d-name").textContent = s.name;
  $("d-meta").textContent = [describe(s), s.branch && `branch ${s.branch}`, s.path].filter(Boolean).join(" · ");
  const busy = state.busy.has(s.path);
  const up = s.status === "running" || s.status === "partial";
  const buttons = [];
  if (up && s.urls?.length) buttons.push(el("a", { class: "btn primary", href: s.urls[0], target: "_blank", rel: "noopener" }, iconSpan("open"), "Open"));
  if (!s.orphaned) {
    buttons.push(el("button", { class: up ? "btn" : "btn primary", disabled: busy, onclick: (e) => act(e, "up", s) }, iconSpan("play"), up ? "Update" : "Start"));
    if (up) buttons.push(el("button", { class: "btn", disabled: busy, onclick: (e) => act(e, "stop", s) }, iconSpan("stop"), "Stop"));
  }
  buttons.push(el("button", { class: "btn danger", disabled: busy, onclick: (e) => act(e, "down", s) }, iconSpan("trash"), "Remove"));
  buttons.push(el("button", { class: "btn", title: "Copy the command to work on it in a terminal", onclick: () => copy(`cd ${s.path}`) }, iconSpan("copy"), "Copy path"));
  $("d-actions").replaceChildren(...buttons);
}

function selectTab(tab) {
  state.tab = tab;
  for (const b of document.querySelectorAll(".tabs button")) b.setAttribute("aria-selected", String(b.dataset.tab === tab));
  for (const t of ["services", "logs", "events"]) $(`tab-${t}`).hidden = t !== tab;
  stopFollow();
  if (tab === "services") loadServices();
  if (tab === "logs") openLogs();
  if (tab === "events") renderEvents();
}

function badgeOf(svc) {
  if (svc.profiles && svc.state !== "running") return ["off", "off"];
  if (svc.state === "missing") return ["missing", "not created"];
  if (/\(healthy\)/.test(svc.status)) return ["healthy", "healthy"];
  if (/\(unhealthy\)/.test(svc.status)) return ["unhealthy", "unhealthy"];
  if (/health: starting/.test(svc.status)) return ["starting", "starting"];
  if (svc.completed) return ["completed", "done"];
  if (svc.state === "exited") return ["exited", "stopped"];
  return [svc.state, svc.state];
}

function row(label, valueNode, copyText) {
  return el(
    "div",
    { class: "svc-row" },
    el("span", { class: "k", text: label }),
    valueNode,
    copyText ? el("button", { class: "copy", title: "Copy", "aria-label": "Copy", icon: "copy", onclick: () => copy(copyText) }) : null,
  );
}

// diagnosisBox shows what is wrong with a service and the evidence: its health checks and logs.
function diagnosisBox(s, d) {
  const last = (d.health || []).at(-1);
  const text = [`${d.service}: ${d.problem}`, d.hint ? `-> ${d.hint}` : "", last ? `last health check (exit ${last.exitCode}):\n${last.output}` : "", d.logs ? `last logs:\n${d.logs}` : ""].filter(Boolean).join("\n\n");
  return el(
    "div",
    { class: "diag", role: "alert" },
    el("div", { class: "diag-head" }, iconSpan("alert"), el("strong", { text: d.problem })),
    d.hint ? el("p", { class: "hint", text: "→ " + d.hint }) : null,
    last
      ? el(
          "details",
          { open: true },
          el("summary", { text: `Last health check · exit ${last.exitCode} · ${new Date(last.time).toLocaleTimeString()}${d.health.length > 1 ? ` · ${d.health.filter((h) => h.exitCode !== 0).length} of ${d.health.length} failed` : ""}` }),
          el("pre", { class: "logs code", text: last.output || "(no output)" }),
        )
      : null,
    d.logs ? el("details", { open: true }, el("summary", { text: "Last logs" }), el("pre", { class: "logs code wrap" }, logFragment(d.logs))) : null,
    el(
      "div",
      { class: "diag-actions" },
      el("button", { class: "btn sm", onclick: () => copy(text) }, iconSpan("copy"), el("span", { text: "Copy diagnosis" })),
      el("button", { class: "btn sm", onclick: () => { $("l-service").value = d.service; selectTab("logs"); } }, iconSpan("logs"), el("span", { text: "All logs" })),
    ),
  );
}

function serviceCard(s, svc, diag) {
  const [kind, label] = badgeOf(svc);
  const rows = [];
  for (const u of svc.urls || []) rows.push(row("URL", el("a", { href: u, target: "_blank", rel: "noopener", text: u }), u));
  for (const p of svc.ports || []) rows.push(row(`:${p.containerPort}`, el("code", { text: p.address }), p.address));
  if (svc.state === "running") rows.push(row("shell", el("code", { text: `mutti shell ${svc.service}` }), `mutti -C ${s.path} shell ${svc.service}`));
  return el(
    "div",
    { class: "svc" },
    el(
      "div",
      { class: "svc-head" },
      el("strong", { text: svc.service }),
      el("span", { class: `badge ${kind}`, text: label }),
      el("span", { class: "svc-status", text: svc.status }),
      el("span", { class: "svc-actions" }, ...(svc.state === "running" || svc.state === "restarting" ? ["restart", "stop"] : ["up"]).map((action) => serviceButton(s, action, [svc.service]))),
    ),
    rows.length ? el("div", { class: "svc-rows" }, ...rows) : null,
    diag ? diagnosisBox(s, diag) : null,
  );
}

function profileHead(s, profile, services) {
  const idle = services.filter((svc) => svc.state !== "running").map((svc) => svc.service);
  return el(
    "div",
    { class: "profile-head" },
    el("div", {}, el("strong", { text: profile }), el("span", { class: "muted", text: "Optional, runs when started" })),
    idle.length > 1 ? serviceButton(s, "up", idle, "Start all") : null,
  );
}

const serviceActions = {
  up: { icon: "play", label: "Start", busy: "Starting", done: "started", fail: "start" },
  stop: { icon: "stop", label: "Stop", busy: "Stopping", done: "stopped", fail: "stop" },
  restart: { icon: "restart", label: "Restart", busy: "Restarting", done: "restarted", fail: "restart" },
};

// serviceButton runs an action on some services. With a text it is a labelled button, without
// one an icon button that names the action in its tooltip.
function serviceButton(s, action, services, text) {
  const a = serviceActions[action];
  const key = action + ":" + services.join(",");
  const busy = state.busy.get(s.path) === key;
  const label = `${a.label} ${services.join(", ")}`;
  return el(
    "button",
    { class: text ? "btn sm" : "btn sm icon", title: label, "aria-label": label, disabled: busy, onclick: () => serviceAction(s, action, services, key) },
    iconSpan(a.icon),
    text ? el("span", { text: busy ? a.busy : text }) : null,
  );
}

async function serviceAction(s, action, services, key) {
  const a = serviceActions[action];
  state.busy.set(s.path, key);
  loadServices();
  try {
    await api(`/api/${action}?dir=${q(s.path)}${services.map((x) => "&service=" + q(x)).join("")}`, { method: "POST" });
    toast("ok", `${services.join(", ")} ${a.done}`);
  } catch (err) {
    toast("error", `Could not ${a.fail} ${services.join(", ")}`, err);
  }
  state.busy.delete(s.path);
  await loadInstances();
  if (state.selected === s.path && state.tab === "services") loadServices();
}

async function loadServices() {
  const s = selectedInstance();
  const panel = $("tab-services");
  if (!s) return;
  if (!panel.firstChild) panel.append(el("div", { class: "skeleton-line" }), el("div", { class: "skeleton-line" }), el("div", { class: "skeleton-line" }));
  let st;
  try {
    st = await api(`/api/status?dir=${q(s.path)}`);
    // What is not well comes with its diagnosis; a failing diagnosis must not hide the services.
    st.diags = new Map((await api(`/api/diagnose?dir=${q(s.path)}`).catch(() => [])).map((d) => [d.service, d]));
  } catch (err) {
    panel.replaceChildren(el("p", { class: "muted", text: err.message + (err.hint ? "\n→ " + err.hint : "") }));
    return;
  }
  // The services every start brings up first, then one group per profile (the first one of a
  // service) with a button that starts the services of that group.
  const groups = new Map();
  for (const svc of st.services) {
    const profile = (svc.profiles || [])[0] || "";
    if (!groups.has(profile)) groups.set(profile, []);
    groups.get(profile).push(svc);
  }
  panel.replaceChildren(
    ...[...groups].map(([profile, services]) =>
      el(
        "div",
        { class: "svc-group" },
        profile ? profileHead(s, profile, services) : null,
        el("div", { class: "services" }, ...services.map((svc) => serviceCard(s, svc, st.diags.get(svc.service)))),
      ),
    ),
  );
  for (const pre of panel.querySelectorAll(".diag pre")) pre.scrollTop = pre.scrollHeight; // newest lines in view
  const select = $("l-service");
  const current = select.value;
  select.replaceChildren(el("option", { value: "", text: "All services" }), ...st.services.map((x) => el("option", { value: x.service, text: x.service })));
  select.value = current;
}

// ---- Logs

let followTimer;
let lastLogs = "";

function stopFollow() {
  clearInterval(followTimer);
  followTimer = undefined;
}

async function openLogs() {
  if ($("l-service").options.length === 0) await loadServices();
  await loadLogs();
  if ($("l-follow").checked && state.tab === "logs") followTimer = setInterval(loadLogs, 2000);
}

async function loadLogs() {
  const s = selectedInstance();
  if (!s) return;
  try {
    const logs = await api(`/api/logs?dir=${q(s.path)}&tail=500&service=${q($("l-service").value)}`);
    lastLogs = logs.output;
  } catch (err) {
    lastLogs = err.message;
  }
  renderLogs();
}

// logFragment renders log lines with the service name, timestamp and errors set apart.
function logFragment(text, filter = "") {
  const lines = text.split("\n").filter((l) => l && (!filter || l.toLowerCase().includes(filter)));
  const frag = document.createDocumentFragment();
  if (lines.length === 0) frag.append(el("span", { class: "lempty", text: filter ? "No line matches the filter." : "No output yet." }));
  for (const line of lines) {
    const m = line.match(/^(\S+\s*\|\s)(\S+Z\s)?(.*)$/); // "service-1  | 2026-...Z message"
    const kind = /\b(error|fatal|panic|exception|critical)\b/i.test(line) ? "lerr" : /\bwarn(ing)?\b/i.test(line) ? "lwarn" : "";
    const span = el("span", { class: kind });
    if (m) span.append(el("span", { class: "svcname", text: m[1] }), m[2] ? el("span", { class: "ts", text: m[2] }) : "", m[3]);
    else span.append(line);
    frag.append(span, "\n");
  }
  return frag;
}

function renderLogs() {
  const out = $("l-output");
  const atBottom = out.scrollHeight - out.scrollTop - out.clientHeight < 40;
  const filter = $("l-filter").value.toLowerCase();
  out.replaceChildren(logFragment(lastLogs, filter));
  if (atBottom) out.scrollTop = out.scrollHeight;
}

$("l-service").onchange = loadLogs;
$("l-filter").oninput = renderLogs;
$("l-wrap").onchange = (e) => $("l-output").classList.toggle("wrap", e.target.checked);
$("l-follow").onchange = (e) => {
  stopFollow();
  if (e.target.checked) followTimer = setInterval(loadLogs, 2000);
};

// ---- Events

function renderEvents() {
  const s = selectedInstance();
  if (!s) return;
  const mine = state.events.filter((e) => e.instance === s.name);
  $("d-events").replaceChildren(
    ...(mine.length
      ? mine.map((e) =>
          el(
            "li",
            {},
            el("time", { text: new Date(e.time).toLocaleTimeString() }),
            el("span", { text: `${e.service || "instance"} ${e.event}${e.exitCode !== undefined ? ` (exit ${e.exitCode})` : ""}` }),
          ),
        )
      : [el("li", { class: "empty-note", text: "No events since this page opened. They appear here as containers change." })]),
  );
}

let reload;
function listen() {
  const source = new EventSource("/api/events");
  const conn = $("conn");
  source.onopen = () => {
    conn.className = "conn live";
    conn.lastChild.textContent = "Live";
    loadInstances();
  };
  source.onerror = () => {
    conn.className = "conn lost";
    conn.lastChild.textContent = "Reconnecting…";
  };
  source.onmessage = (msg) => {
    const ev = JSON.parse(msg.data).data;
    state.events.unshift(ev);
    state.events.length = Math.min(state.events.length, 300);
    if (state.selected && state.tab === "events") renderEvents();
    clearTimeout(reload);
    reload = setTimeout(() => {
      loadInstances();
      if (state.selected && state.tab === "services") loadServices();
    }, 300);
  };
}

// ---- System

async function loadChecks() {
  let report;
  try {
    report = await api("/api/doctor");
  } catch (err) {
    $("checks").replaceChildren(el("li", { class: "fail" }, el("span", { class: "ci", text: "!" }), el("span", { text: err.message })));
    return;
  }
  const mark = { ok: "✓", warn: "!", fail: "✕" };
  $("checks").replaceChildren(
    ...report.checks.map((c) =>
      el(
        "li",
        { class: c.status, title: [c.detail, c.hint].filter(Boolean).join("\n") },
        el("span", { class: "ci", text: mark[c.status] || "·" }),
        el(
          "div",
          {},
          el("div", { class: "cname", text: c.name }),
          // Healthy checks say their name; the details are a hover away. Problems speak up.
          c.status === "ok" ? null : el("div", { class: "cdetail", text: c.hint || c.detail }),
        ),
      ),
    ),
  );
}

// ---- Wiring

for (const b of document.querySelectorAll(".tabs button")) b.onclick = () => selectTab(b.dataset.tab);
$("d-close").onclick = closeDrawer;
$("search").oninput = (e) => {
  state.query = e.target.value.trim().toLowerCase();
  render();
};
document.addEventListener("keydown", (e) => {
  const typing = /^(INPUT|SELECT|TEXTAREA)$/.test(document.activeElement?.tagName);
  if (e.key === "/" && !typing) {
    e.preventDefault();
    $("search").focus();
  } else if (e.key === "Escape" && !$("confirm").open) {
    if (typing) document.activeElement.blur();
    else if (state.selected) closeDrawer();
  }
});

loadInstances();
loadChecks();
listen();
