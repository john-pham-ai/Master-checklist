(function () {
  const root = document.documentElement;
  const themeBtn = document.getElementById("theme-toggle");
  const langSelect = document.getElementById("lang-select");
  const translations = {};

  function applyTheme(theme) {
    root.setAttribute("data-theme", theme);
    try { localStorage.setItem("checklist-theme", theme); } catch (e) {}
    if (themeBtn) {
      themeBtn.textContent = theme === "dark"
        ? (translations[currentLang()]?.theme_toggle_light || "Light mode")
        : (translations[currentLang()]?.theme_toggle_dark || "Dark mode");
    }
  }

  function currentLang() {
    return (langSelect && langSelect.value) || "en";
  }

  function applyTranslations(lang) {
    const dict = translations[lang];
    if (!dict) return;
    document.querySelectorAll("[data-i18n]").forEach((el) => {
      const key = el.getAttribute("data-i18n");
      if (dict[key]) el.textContent = dict[key];
    });
    document.querySelectorAll("[data-i18n-placeholder]").forEach((el) => {
      const key = el.getAttribute("data-i18n-placeholder");
      if (dict[key]) el.setAttribute("placeholder", dict[key]);
    });
    document.documentElement.setAttribute("lang", lang);
    try { localStorage.setItem("checklist-lang", lang); } catch (e) {}
  }

  function loadLang(lang) {
    if (translations[lang]) {
      applyTranslations(lang);
      return;
    }
    fetch(`/i18n/${lang}.json`)
      .then((r) => r.json())
      .then((dict) => {
        translations[lang] = dict;
        applyTranslations(lang);
        applyTheme(root.getAttribute("data-theme"));
      })
      .catch((err) => console.error("failed to load translations", lang, err));
  }

  // Small public helper so page-specific scripts (feedback.js) can reuse the
  // loaded translations for dynamic status messages.
  window.checklistI18n = {
    t: (key, fallback) => (translations[currentLang()] && translations[currentLang()][key]) || fallback,
    lang: currentLang,
  };

  let savedTheme = "dark";
  let savedLang = "en";
  try {
    savedTheme = localStorage.getItem("checklist-theme") || "dark";
    savedLang = localStorage.getItem("checklist-lang") || "en";
  } catch (e) {}

  applyTheme(savedTheme);
  if (langSelect) langSelect.value = savedLang;
  loadLang(savedLang);

  if (themeBtn) {
    themeBtn.addEventListener("click", () => {
      const next = root.getAttribute("data-theme") === "dark" ? "light" : "dark";
      applyTheme(next);
    });
  }

  if (langSelect) {
    langSelect.addEventListener("change", () => loadLang(langSelect.value));
  }

  const testTypeSelect = document.getElementById("test-type-select");
  const slackHint = document.getElementById("slack-channel-hint");
  function applySlackHint() {
    if (!testTypeSelect || !slackHint) return;
    const href = testTypeSelect.value === "candidate"
      ? slackHint.getAttribute("data-candidate-href")
      : slackHint.getAttribute("data-master-href");
    slackHint.setAttribute("href", href);
  }
  applySlackHint();
  if (testTypeSelect) {
    testTypeSelect.addEventListener("change", applySlackHint);
  }

  const tagList = document.getElementById("tag-suggestions");
  const tagInput = document.querySelector('input[name="tag"]');
  let knownTags = [];
  let tagAuto = true; // Tag was filled by us (latest build), not typed by the tester

  // Newest build of the selected kind: tags arrive newest-first; skip
  // namespaced variants like "verified/..." when a plain tag exists.
  function latestTag(tags) {
    return tags.find((x) => !x.includes("/")) || tags[0] || "";
  }

  function loadTagSuggestions() {
    if (!testTypeSelect || !tagList) return;
    fetch(`/api/tags?test_type=${encodeURIComponent(testTypeSelect.value)}`)
      .then((r) => r.json())
      .then((tags) => {
        knownTags = Array.isArray(tags) ? tags : [];
        tagList.innerHTML = "";
        knownTags.forEach((tag) => {
          const option = document.createElement("option");
          option.value = tag;
          tagList.appendChild(option);
        });
        // Pre-fill the Tag with the latest build of this kind unless the
        // tester already typed a tag of this kind themselves.
        if (tagInput && (tagAuto || !knownTags.includes(tagInput.value.trim()))) {
          const latest = latestTag(knownTags);
          if (latest) {
            tagInput.value = latest;
            tagAuto = true;
            diffBaseAuto = true; // a new head means a new automatic base
          }
        }
        maybeLoadDiff(true);
      })
      .catch((err) => console.error("failed to load tag suggestions", err));
  }
  if (tagInput) {
    tagInput.addEventListener("input", () => { tagAuto = false; diffBaseAuto = true; });
  }
  loadTagSuggestions();
  if (testTypeSelect) {
    testTypeSelect.addEventListener("change", loadTagSuggestions);
  }

  // ---- Master diff summary (changes since the previous build of the same kind) ----
  const diffBase = document.getElementById("diff-base");
  let diffBaseAuto = true; // Compare-against was filled by us (previous build), not typed
  if (diffBase) {
    diffBase.addEventListener("input", () => { diffBaseAuto = diffBase.value.trim() === ""; });
  }
  const diffBaseList = document.getElementById("diff-base-suggestions");
  const diffStatus = document.getElementById("diff-status");
  const diffResults = document.getElementById("diff-results");
  const diffJson = document.getElementById("diff-json");
  const diffLink = document.getElementById("diff-compare-link");
  const diffReload = document.getElementById("diff-reload");
  const t = (k, fb) => (window.checklistI18n ? window.checklistI18n.t(k, fb) : fb);
  const tagFamily = (tag) => tag.replace(/\d{4}-\d{2}-\d{2}(-\d+)?$/, "");
  let lastDiffKey = "";
  let diffTimer = null;

  function setDiffStatus(text, kind) {
    if (!diffStatus) return;
    diffStatus.textContent = text;
    diffStatus.className = "muted small" + (kind ? " " + kind : "");
  }

  function fillBaseSuggestions(head) {
    if (!diffBaseList) return;
    diffBaseList.innerHTML = "";
    const fam = tagFamily(head);
    knownTags.filter((x) => x !== head && tagFamily(x) === fam).forEach((x) => {
      const o = document.createElement("option");
      o.value = x;
      diffBaseList.appendChild(o);
    });
  }

  function el(tag, cls, text) {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined) e.textContent = text;
    return e;
  }

  // "2026-09-01" -> "Sep 1"; "2026-08-26-01" -> "Aug 26 #01" (localised month names via i18n).
  function friendlyDate(stamp) {
    if (!stamp || stamp.length < 10) return stamp || "";
    const [y, m, d] = stamp.slice(0, 10).split("-").map(Number);
    if (!m || !d) return stamp;
    const months = (t("months_short", "Jan,Feb,Mar,Apr,May,Jun,Jul,Aug,Sep,Oct,Nov,Dec")).split(",");
    const lang = window.checklistI18n ? window.checklistI18n.lang() : "en";
    let s = lang === "ja" ? `${m}月${d}日` : `${months[m - 1]} ${d}`;
    if (stamp.length > 11) s += " #" + stamp.slice(11);
    return s;
  }

  function renderDiff(d) {
    diffResults.innerHTML = "";
    if (diffLink) {
      diffLink.href = d.compare_url || "#";
      diffLink.hidden = !d.compare_url;
    }

    // Headline sentence: "Aug 31 → Sep 1: 92 changes — HMI 0 · Behavior 1 · …"
    const head = el("p", "diff-headline");
    const from = friendlyDate(d.base_date) || d.base, to = friendlyDate(d.head_date) || d.head;
    head.appendChild(el("strong", "", `${from} → ${to}`));
    const counts = d.categories.map((c) => `${t("cat_" + c.key, c.label)} ${c.items.length + c.undescribed}`).join(" · ");
    head.appendChild(document.createTextNode(`: ${d.total_commits} ${t("diff_changes", "changes in total")} — ${counts}`));
    diffResults.appendChild(head);

    if (d.ai_summary) {
      const box = el("div", "diff-ai");
      box.appendChild(el("strong", "", t("diff_ai_summary", "In short")));
      d.ai_summary.split("\n").forEach((line) => { if (line.trim()) box.appendChild(el("div", "", line.replace(/^-\s*/, "• "))); });
      diffResults.appendChild(box);
    }

    d.categories.forEach((c) => {
      const total = c.items.length + c.undescribed;
      const details = el("details", "diff-cat");
      const summary = el("summary");
      summary.appendChild(el("strong", "", t("cat_" + c.key, c.label)));
      summary.appendChild(document.createTextNode(` (${total})`));
      summary.appendChild(el("span", "muted small diff-explain", " — " + t("cat_" + c.key + "_desc", "")));
      details.appendChild(summary);
      details.open = c.items.length > 0;
      if (total === 0) {
        details.appendChild(el("p", "muted small", t("diff_no_items", "No changes")));
        diffResults.appendChild(details);
        return;
      }
      if (c.items.length) {
        const ul = el("ul", "diff-list");
        c.items.forEach((it) => {
          const li = el("li");
          const a = el("a", "diff-title", it.headline || it.title);
          a.href = it.url; a.target = "_blank"; a.rel = "noopener";
          li.appendChild(a);
          if (it.is_revert) li.appendChild(el("span", "badge badge-revert", t("badge_revert", "reverted")));
          else if (it.is_fix && c.key !== "fixes") li.appendChild(el("span", "badge badge-fix", t("badge_fix", "fix")));
          if (it.summary) li.appendChild(el("div", "diff-desc", it.summary));
          const meta = [];
          if (it.pr) meta.push(`PR #${it.pr}`);
          if (it.jira) meta.push(it.jira);
          if (it.tags && it.tags.length) meta.push(it.tags.join(", "));
          if (it.dirs && it.dirs.length) meta.push(it.dirs.join(", "));
          if (meta.length) {
            const md = el("details", "diff-meta");
            md.appendChild(el("summary", "muted small", t("diff_details", "Details")));
            md.appendChild(el("div", "muted small", meta.join(" · ")));
            if (it.files && it.files.length) md.appendChild(el("div", "muted small diff-files", it.files.join(", ")));
            li.appendChild(md);
          }
          ul.appendChild(li);
        });
        details.appendChild(ul);
      }
      if (c.undescribed > 0) {
        details.appendChild(el("p", "muted small", `${c.undescribed} ${t("diff_undescribed", "more automated or undescribed changes also touched this area")}`));
      }
      diffResults.appendChild(details);
    });

    if (d.other_count > 0) {
      const p = el("p", "muted diff-other");
      let text = `${d.other_count} ${t("diff_other_line", "other changes outside these areas")}`;
      if (d.other_automated > 0) text += ` (${d.other_automated} ${t("diff_automated", "automated system updates")})`;
      p.appendChild(document.createTextNode(text + " — "));
      const a = el("a", "", t("diff_compare_link", "Open compare on GitHub"));
      a.href = d.compare_url || "#"; a.target = "_blank"; a.rel = "noopener";
      p.appendChild(a);
      diffResults.appendChild(p);
    }
    if (d.note) diffResults.appendChild(el("p", "muted small", d.note));
    if (d.truncated) diffResults.appendChild(el("p", "muted small", t("diff_truncated", "Large diff: some changes were classified by title only.")));
  }

  function maybeLoadDiff(force) {
    if (!tagInput || !diffResults) return;
    const head = tagInput.value.trim();
    if (!head || (!force && !knownTags.includes(head))) return;
    // When the base is automatic, let the server pick the previous build of
    // the same kind and show the result in the field.
    const base = diffBase && !diffBaseAuto ? diffBase.value.trim() : "";
    const key = head + "|" + base;
    if (!force && key === lastDiffKey) return;
    lastDiffKey = key;
    fillBaseSuggestions(head);
    setDiffStatus(t("diff_loading", "Loading changes…"), "");
    diffResults.innerHTML = "";
    if (diffJson) diffJson.value = "";
    const q = new URLSearchParams({ head });
    if (base) q.set("base", base);
    fetch(`/api/diff?${q}`)
      .then(async (r) => ({ ok: r.ok, status: r.status, data: await r.json().catch(() => ({})) }))
      .then(({ ok, status, data }) => {
        if (!ok) {
          setDiffStatus(t("diff_error", "Could not load the diff:") + " " + (data.error || status), "error");
          return;
        }
        if (diffBase && diffBaseAuto) diffBase.value = data.base;
        setDiffStatus(`${t("diff_builds", "Builds compared")}: ${data.base} → ${data.head}`, "");
        renderDiff(data);
        if (diffJson) diffJson.value = JSON.stringify(data);
      })
      .catch((err) => setDiffStatus(t("diff_error", "Could not load the diff:") + " " + err, "error"));
  }

  if (tagInput) {
    tagInput.addEventListener("change", () => maybeLoadDiff(false));
    tagInput.addEventListener("input", () => {
      clearTimeout(diffTimer);
      diffTimer = setTimeout(() => maybeLoadDiff(false), 600);
    });
  }
  if (diffBase) diffBase.addEventListener("change", () => maybeLoadDiff(true));
  if (diffReload) diffReload.addEventListener("click", () => maybeLoadDiff(true));
  // Switching Master <-> Candidate re-fills the Tag with that kind's latest build (see loadTagSuggestions).
  if (testTypeSelect) testTypeSelect.addEventListener("change", () => { tagAuto = true; diffBaseAuto = true; });

  // Test Engineer suggestions: members of the access groups (see engineers.go).
  // The field itself is pre-filled server-side with the signed-in user's name.
  const engineerList = document.getElementById("engineer-suggestions");
  if (engineerList) {
    fetch("/api/engineers")
      .then((r) => r.json())
      .then((engineers) => {
        engineerList.innerHTML = "";
        engineers.forEach((e) => {
          const option = document.createElement("option");
          option.value = e.name;
          option.label = e.email;
          engineerList.appendChild(option);
        });
      })
      .catch((err) => console.error("failed to load engineer suggestions", err));
  }
})();
