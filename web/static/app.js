"use strict";
(() => {
  const $ = (id) => document.getElementById(id);
  const reduced = matchMedia("(prefers-reduced-motion: reduce)").matches;
  const wait = (ms) => new Promise((r) => setTimeout(r, reduced ? 0 : ms));

  let state = null;
  let workflow = null;   // parsed workflow from upload or paste
  let analysis = null;
  let pollingRun = 0;
  let shownRun = null;   // run currently displayed (and offered for Submit)
  let runningSince = 0;  // when the shown run started running (client clock), for the stage animation

  // ---------- tiny DOM helpers (all text goes through textContent) ----------
  const el = (tag, attrs = {}, ...kids) => {
    const n = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs)) {
      if (v == null) continue;
      if (k === "class") n.className = v;
      else if (k === "text") n.textContent = v;
      else n.setAttribute(k, v);
    }
    for (const k of kids) if (k != null) n.append(k);
    return n;
  };
  const clear = (n) => { while (n.firstChild) n.removeChild(n.firstChild); };
  const show = (n, on = true) => n.classList.toggle("hidden", !on);
  const time = (u) => (u ? new Date(u * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) : "–");
  const store = {
    get(k) { try { return sessionStorage.getItem(k) || ""; } catch { return ""; } },
    set(k, v) { try { sessionStorage.setItem(k, v); } catch { /* private mode */ } },
  };
  const SVG = "http://www.w3.org/2000/svg";
  const icon = (kind) => {
    const svg = document.createElementNS(SVG, "svg");
    svg.setAttribute("viewBox", "0 0 24 24");
    svg.setAttribute("aria-hidden", "true");
    const p = document.createElementNS(SVG, "path");
    p.setAttribute("d", kind === "pass" ? "M5 12.5l4.5 4.5L19 7.5" : kind === "fail" ? "M7 7l10 10M17 7L7 17" : "M12 5v9M12 18.5v.5");
    svg.append(p);
    return svg;
  };
  const triggerName = (k, method) => (k === "chat" ? "Chat Trigger" : k === "telegram" ? "Telegram Trigger" : "Webhook" + (method ? " · " + method : ""));
  const tick = (kind) => el("span", { class: "tick" }, kind ? icon(kind) : null);

  async function api(path, body) {
    const opts = body === undefined ? {} : { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) };
    const res = await fetch(path, opts);
    if (res.status === 401) { location.href = "/login"; throw new Error("logged out"); }
    const data = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(data.error || "Something went wrong (" + res.status + ")");
    return data;
  }

  // ---------- boot animation (once per session) ----------
  async function boot() {
    const overlay = $("boot");
    if (store.get("booted")) { overlay.classList.add("gone"); return refresh(); }
    const list = $("bootSteps"), bar = $("bootBar");
    const steps = [];
    const add = (text) => {
      const li = el("li", {}, tick(), el("span", {}, text, el("span", { class: "detail" })));
      list.append(li);
      requestAnimationFrame(() => li.classList.add("show"));
      steps.push(li);
      return li;
    };
    const done = (li, detail, warn) => {
      li.classList.add(warn ? "warn" : "ok");
      li.querySelector(".tick").append(icon(warn ? "warn" : "pass"));
      li.querySelector(".detail").textContent = detail;
      bar.style.width = (steps.filter((s) => s.classList.contains("ok") || s.classList.contains("warn")).length / 4) * 100 + "%";
    };

    const s1 = add("Signing you in");
    await wait(500);
    try { state = await api("/api/state"); } catch { return; }
    done(s1, state.team);
    const s2 = add("Connecting to the judge");
    await wait(650);
    done(s2, "n8n " + state.n8nVersion + " ready");
    const s3 = add("Finding a test machine");
    await wait(750);
    done(s3, state.workers ? `${state.workers} test slot${state.workers > 1 ? "s" : ""} online` : "None online yet; your runs will wait in the queue", !state.workers);
    const s4 = add("Loading the round");
    await wait(600);
    done(s4, state.round ? `Round ${state.round.n}: ${state.round.title}` : "No round open yet");
    await wait(700);
    store.set("booted", "1");
    overlay.classList.add("gone");
    render();
  }

  // ---------- state ----------
  async function refresh() {
    try { state = await api("/api/state"); } catch { return; }
    render();
  }

  function render() {
    const s = state;
    $("n8nLabel").textContent = "n8n " + s.n8nVersion;
    $("workersText").textContent = s.workers ? `${s.workers} test slot${s.workers > 1 ? "s" : ""} online` : "No test machines online";
    $("workersDot").className = "dot " + (s.workers ? "live" : "off");

    const r = s.round;
    show($("closedCard"), !r);
    show($("openArea"), !!r);
    if (!r) {
      $("roundLabel").textContent = "Waiting";
      $("marker").textContent = "–";
      if ($("title").dataset.n !== "0") {
        $("title").dataset.n = "0";
        $("title").textContent = "Hang tight";
        clear($("metaChips"));
        clear($("statement"));
        $("statement").append(el("p", { class: "muted", text: "The problem appears here as soon as the organisers open the round. This page checks every few seconds." }));
      }
      return;
    }

    if ($("title").dataset.n !== String(r.n)) {
      $("title").dataset.n = String(r.n);
      $("title").textContent = r.title;
      $("statement").innerHTML = r.statementHtml; // organiser-written markdown, rendered server-side (raw HTML disabled)
      const chips = $("metaChips");
      clear(chips);
      chips.append(
        el("span", { class: "chip", text: triggerName(r.trigger, r.method) }),
        el("span", { class: "chip", text: `${r.samples} sample tests` }),
        el("span", { class: "chip", text: `${r.hidden} hidden tests` }),
      );
      shownRun = null;
      drawOutline();
    }
    $("roundLabel").textContent = "Round " + r.n;
    $("marker").textContent = "R" + r.n;
    $("runSub").textContent = `${r.samples + r.hidden} tests: the ${r.samples} samples show full input and output, hidden ones show only their category and verdict.`;
    $("tokenText").textContent = `Every run sends about ${r.requests} requests through your workflow, so it spends your own AI tokens and quota. Runs are unlimited, but each one costs you.`;

    const sub = s.submission;
    show($("submittedCard"), !!sub);
    show($("buildArea"), !sub);
    show($("runBtn"), !sub);
    show($("tokenWarn"), !sub);
    show($("submitCard"), !sub);
    if (sub) {
      $("submittedText").textContent = `Run #${sub.runId} with ${sub.passed}/${sub.total} tests passing, at ${time(sub.at)}` +
        (sub.auto ? ", submitted automatically when the round closed." : ". That's final for this round. Nice work.");
    }
    renderHistory();
    if (s.inFlight && pollingRun !== s.inFlight) watchRun(s.inFlight);
    if (!shownRun && !s.inFlight && s.runs.length && s.runs[0].status === "done") loadRun(s.runs[0].id, false);
    updateButtons();
  }

  function renderHistory() {
    const h = $("history");
    clear(h);
    if (!state.runs.length) {
      h.append(el("span", { class: "small", text: "No runs yet." }));
      return;
    }
    for (const r of state.runs.slice(0, 12)) {
      const label = r.status === "done" ? (r.invalid ? `#${r.id} · invalid` : `#${r.id} · ${r.passed}/${r.total}`) : `#${r.id} · ${r.status}`;
      const b = el("button", { type: "button", text: label, "aria-current": shownRun && shownRun.id === r.id ? "true" : "false" });
      b.addEventListener("click", () => loadRun(r.id, true));
      h.append(b);
    }
  }

  function updateButtons() {
    const busy = !!(state && state.inFlight);
    $("runBtn").disabled = busy || !analysis || (analysis.errors || []).length > 0;
    $("runBtn").textContent = busy ? "Running…" : "Run tests";
    $("runHint").textContent = !analysis && !busy ? "Upload a workflow first." : "";
    const sb = $("submitBtn");
    const ok = shownRun && shownRun.status === "done" && !shownRun.invalid && !state.submission;
    sb.disabled = !ok;
    sb.textContent = ok ? `Submit run #${shownRun.id} · ${shownRun.passed}/${shownRun.total}` : "Pick a finished run to submit";
  }

  // ---------- workflow input ----------
  function setTab(paste) {
    $("tabUpload").setAttribute("aria-selected", String(!paste));
    $("tabPaste").setAttribute("aria-selected", String(paste));
    show($("uploadPane"), !paste);
    show($("pastePane"), paste);
  }

  async function useFile(file) {
    if (!file) return;
    let text;
    try { text = await file.text(); } catch { return fail("Couldn't read that file."); }
    useText(text, file.name);
  }

  async function useText(text, name) {
    const out = $("analysis");
    clear(out);
    show($("credCard"), false);
    $("wfCard").classList.remove("done");
    workflow = null; analysis = null;
    updateButtons();
    try { workflow = JSON.parse(text); } catch { return fail("That isn't valid JSON. Export the workflow from n8n again (⋯ → Download)."); }
    const nodes = Array.isArray(workflow.nodes) ? workflow.nodes.length : 0;
    const bar = $("fileBar");
    clear(bar);
    const change = el("button", { class: "linkbtn", type: "button", text: "Change" });
    change.addEventListener("click", () => { show(bar, false); show($("tabs"), true); show($("uploadPane"), true); $("file").value = ""; clear(out); show($("credCard"), false); workflow = analysis = null; updateButtons(); setTab(false); });
    bar.append(el("span", { class: "badge", text: "JSON" }), el("span", {}, (workflow.name || name || "workflow"), el("span", { class: "muted", text: ` · ${nodes} nodes` })), el("span", { class: "spacer" }), change);
    show(bar, true);
    show($("tabs"), false);
    show($("uploadPane"), false);
    show($("pastePane"), false);
    out.append(el("p", { class: "notice ink" }, el("span", { class: "ic", text: "…" }), el("span", { text: "Reading your workflow…" })));
    try { analysis = await api("/api/analyze", { workflow }); }
    catch (e) { clear(out); return fail(e.message); }
    clear(out);
    for (const m of analysis.errors || []) out.append(el("p", { class: "notice bad reveal" }, el("span", { class: "ic", text: "✕" }), el("span", { text: m })));
    for (const m of analysis.warnings || []) out.append(el("p", { class: "notice warn reveal" }, el("span", { class: "ic", text: "!" }), el("span", { text: m })));
    if ((analysis.errors || []).length) {
      out.append(el("p", { class: "small muted", text: "Fix these in n8n, download the workflow again and re-upload." }));
      updateButtons();
      return;
    }
    const trig = analysis.trigger || {};
    out.append(el("p", { class: "notice ok reveal" }, el("span", { class: "ic", text: "✓" }),
      el("span", { text: `Looks runnable. Entry point: ${triggerName(trig.kind)} “${trig.nodeName}”.` })));
    $("wfCard").classList.add("done");
    renderCreds(analysis.needs || []);
    updateButtons();
  }

  function fail(msg) {
    const out = $("analysis");
    clear(out);
    out.append(el("p", { class: "notice bad reveal" }, el("span", { class: "ic", text: "✕" }), el("span", { text: msg })));
    updateButtons();
  }

  function renderCreds(needs) {
    const box = $("credFields");
    clear(box);
    if (!needs.length) box.append(el("p", { class: "notice ok", text: "This workflow doesn't need any credentials." }));
    for (const need of needs) {
      const sec = el("div", { class: "credbox" }, el("h3", { text: need.displayName }), el("p", { class: "used", text: "Used by " + need.nodes.join(", ") }));
      if (need.mocked) {
        sec.append(el("p", { class: "notice ok flush" }, el("span", { class: "ic", text: "✓" }),
          el("span", { text: "Simulated by the judge. Nothing to enter: tests run against a fake account so they're repeatable and nobody's real data is touched." })));
        box.append(sec);
        continue;
      }
      for (const f of need.fields || []) {
        const id = `cred-${need.type}-${f.name}`;
        const input = el("input", { id, type: f.password ? "password" : "text", autocomplete: "off", spellcheck: "false",
          placeholder: f.default ? f.default : f.password ? "paste your key" : "" });
        input.dataset.type = need.type;
        input.dataset.field = f.name;
        input.value = store.get(`cred:${need.type}:${f.name}`) || f.default || "";
        if (f.required && !f.default) input.required = true;
        const hint = f.required && !f.default ? "" : f.default ? " · has a default" : " · optional";
        sec.append(el("label", { for: id }, f.displayName || f.name, el("span", { class: "muted", text: hint })), input);
      }
      box.append(sec);
    }
    const card = $("credCard");
    show(card, true);
    card.classList.remove("reveal");
    void card.offsetWidth; // restart the entrance animation
    card.classList.add("reveal");
  }

  // ---------- running ----------
  async function startRun() {
    if (!workflow || !analysis) return;
    const creds = {};
    let missing = null;
    for (const input of $("credFields").querySelectorAll("input")) {
      const { type, field } = input.dataset;
      const v = input.value.trim();
      (creds[type] ||= {})[field] = v;
      store.set(`cred:${type}:${field}`, v);
      if (input.required && !v && !missing) missing = input;
    }
    if (missing) { missing.focus(); $("runHint").textContent = "Fill in the highlighted credential first."; return; }
    $("runBtn").disabled = true;
    try {
      const { runId } = await api("/api/run", { workflow, creds });
      state.inFlight = runId;
      updateButtons();
      watchRun(runId);
    } catch (e) {
      $("runHint").textContent = e.message;
      updateButtons();
    }
  }

  const STAGES = 5;
  function setStage(i, msg) {
    const stages = $("pipeline").children;
    for (let k = 0; k < STAGES; k++) stages[k].className = "stage" + (k < i ? " done" : k === i ? " active" : "");
    $("pipeMsg").textContent = msg;
  }

  function drawOutline(pending = false) {
    const list = $("tests");
    clear(list);
    clear($("scoreBox"));
    if (!state || !state.round) return;
    let sample = 0;
    state.round.tests.forEach((t) => {
      const name = t.visible ? `Sample ${++sample}` : "Hidden test";
      list.append(el("li", { class: "test " + (pending ? "pending" : "idle") }, tick(),
        el("div", {}, el("div", { class: "name", text: t.category || name }), el("div", { class: "kind", text: name })),
        el("span", { class: "ms" })));
    });
  }

  async function watchRun(id) {
    pollingRun = id;
    runningSince = 0;
    show($("progress"), true);
    drawOutline(true);
    shownRun = null;
    $("runCard").scrollIntoView({ behavior: reduced ? "auto" : "smooth", block: "start" });
    while (pollingRun === id) {
      let run;
      try { run = await api("/api/runs/" + id); } catch { break; }
      if (run.status === "queued") {
        setStage(0, run.position ? `Waiting for a test machine · ${run.position} of ${run.queueLength} in the queue` : "Waiting for a test machine…");
      } else if (run.status === "running") {
        runningSince ||= Date.now();
        const t = Date.now() - runningSince;
        if (t < 1400) setStage(1, "Spinning up a fresh n8n container just for this run");
        else if (t < 3000) setStage(2, "Importing your workflow and credentials, then publishing it");
        else setStage(3, `Running ${state.round ? state.round.tests.length : ""} tests against your workflow`);
      } else if (run.status === "grading") {
        setStage(4, "Grading the answers");
      } else {
        setStage(STAGES, run.status === "failed" ? "The judge hit a problem" : "Done");
        await wait(350);
        show($("progress"), false); // the score takes over from the pipeline
        await showResult(run, true);
        break;
      }
      await new Promise((r) => setTimeout(r, run.status === "running" ? 700 : 1500));
    }
    if (pollingRun === id) pollingRun = 0;
    await refresh();
  }

  async function loadRun(id, scroll) {
    if (pollingRun) return;
    try {
      const run = await api("/api/runs/" + id);
      show($("progress"), false);
      await showResult(run, false);
      if (scroll) $("runCard").scrollIntoView({ behavior: reduced ? "auto" : "smooth", block: "start" });
    } catch (e) { $("runHint").textContent = e.message; }
  }

  function verdictKind(v) {
    if (v === "Accepted") return "pass";
    if (v === "Key Rate-Limited" || v === "Time Limit") return "warn";
    return "fail";
  }

  async function showResult(run, animate) {
    shownRun = run;
    renderHistory();
    updateButtons();
    const list = $("tests"), scoreBox = $("scoreBox");
    clear(scoreBox);
    if (run.status === "failed") {
      drawOutline(false);
      scoreBox.append(el("p", { class: "notice warn reveal" }, el("span", { class: "ic", text: "!" }),
        el("span", { text: (run.infra || "The judge had a problem.") + " This run doesn't count against you." })));
      return;
    }
    if (run.status !== "done") return;
    if (run.invalid) {
      scoreBox.append(el("pre", { class: "notice bad reveal", text: run.invalid }));
    }
    const num = el("b", { text: "0" });
    scoreBox.append(el("div", { class: "score reveal" }, num, el("span", { class: "muted", text: `/ ${run.total} passed` }),
      run.seconds ? el("span", { class: "muted small", text: `· ${run.seconds}s · run #${run.id}` }) : el("span", { class: "muted small", text: `· run #${run.id}` })));

    clear(list);
    let sample = 0, passed = 0;
    const rows = run.tests.map((t) => {
      const kind = verdictKind(t.verdict);
      const label = t.visible ? `Sample ${++sample}` : "Hidden test";
      const body = el("div", {}, el("div", { class: "name", text: t.category || label }), el("div", { class: "kind", text: label }));
      if (t.visible && (t.input || t.actual || t.error || (t.checks || []).length)) {
        const det = el("details", {}, el("summary", { text: "Show input and output" }));
        det.append(el("h4", { text: "Input" }), el("pre", { text: t.input || "" }));
        if (t.expected) det.append(el("h4", { text: "Expected" }), el("pre", { text: t.expected }));
        det.append(el("h4", { text: "Your output" }), el("pre", { text: t.actual || "(empty)" }));
        if (t.error) det.append(el("p", { class: "notice bad", text: t.error }));
        const checks = el("ul", { class: "checks" });
        for (const c of t.checks || []) checks.append(el("li", { text: `${c.ok ? "✓" : "✕"} ${c.what}${c.detail ? " — " + c.detail : ""}` }));
        det.append(checks);
        body.append(det);
      }
      const li = el("li", { class: "test pending" }, tick(), body,
        el("span", { class: "row" }, el("span", { class: "ms", text: t.ms ? t.ms + " ms" : "" }), el("span", { class: "verdict", text: t.verdict })));
      list.append(li);
      return { li, kind };
    });
    for (const { li, kind } of rows) {
      if (animate) await wait(110);
      li.className = "test " + kind;
      li.querySelector(".tick").append(icon(kind));
      if (kind === "pass") num.textContent = String(++passed);
    }
    num.textContent = String(run.passed);

    if (run.tests.some((t) => t.verdict === "Key Rate-Limited")) {
      scoreBox.append(el("p", { class: "notice warn" }, el("span", { class: "ic", text: "!" }),
        el("span", { text: "Your API key hit its rate limit even after a retry. Wait a minute or use a key with more quota, then run again." })));
    }
    if (run.tests.some((t) => t.verdict === "Key Rejected")) {
      scoreBox.append(el("p", { class: "notice bad" }, el("span", { class: "ic", text: "✕" }),
        el("span", { text: "Your LLM provider rejected the API key. Check it in step 2 and run again." })));
    }
  }

  // ---------- submit ----------
  function askSubmit() {
    if (!shownRun) return;
    $("confirmTitle").textContent = `Submit run #${shownRun.id}?`;
    $("confirmText").textContent = `It passed ${shownRun.passed} of ${shownRun.total} tests. Submitting is final: you won't be able to run or submit again this round.`;
    $("confirm").showModal();
  }

  async function doSubmit() {
    $("confirm").close();
    try {
      await api("/api/submit", { runId: shownRun.id });
      await refresh();
    } catch (e) { $("runHint").textContent = e.message; }
  }

  // ---------- wiring ----------
  document.addEventListener("DOMContentLoaded", () => {
    $("tabUpload").addEventListener("click", () => setTab(false));
    $("tabPaste").addEventListener("click", () => setTab(true));
    $("file").addEventListener("change", (e) => useFile(e.target.files[0]));
    $("pasteBtn").addEventListener("click", () => useText($("paste").value, "pasted workflow"));
    const drop = $("drop");
    drop.addEventListener("dragover", (e) => { e.preventDefault(); drop.classList.add("over"); });
    drop.addEventListener("dragleave", () => drop.classList.remove("over"));
    drop.addEventListener("drop", (e) => { e.preventDefault(); drop.classList.remove("over"); useFile(e.dataTransfer.files[0]); });
    $("credCard").addEventListener("submit", (e) => { e.preventDefault(); startRun(); });
    $("runBtn").addEventListener("click", startRun);
    $("submitBtn").addEventListener("click", askSubmit);
    $("confirmNo").addEventListener("click", () => $("confirm").close());
    $("confirmYes").addEventListener("click", doSubmit);
    $("themeBtn").addEventListener("click", () => {
      const root = document.documentElement;
      const dark = root.dataset.theme ? root.dataset.theme === "dark" : matchMedia("(prefers-color-scheme: dark)").matches;
      root.dataset.theme = dark ? "light" : "dark";
      try { localStorage.setItem("theme", root.dataset.theme); } catch { /* fine */ }
    });
    // Logging out forgets this tab's saved keys (shared laptops) and replays the intro next time.
    document.querySelector('form[action="/logout"]').addEventListener("submit", () => { try { sessionStorage.clear(); } catch { /* fine */ } });
    boot();
    setInterval(() => { if (!pollingRun) refresh(); }, 5000);
  });
})();
