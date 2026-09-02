(function () {
  const root = document.documentElement;
  const themeBtn = document.getElementById("theme-toggle");
  const langSelect = document.getElementById("lang-select");
  const translations = {};
  // Deploy fingerprint from this script's own URL (app.js?v=…), reused for i18n fetches
  // so a new deploy never pairs new code with a browser-cached old translation file.
  const assetVersion = (document.currentScript && document.currentScript.src && document.currentScript.src.split("?v=")[1]) || "";

  function currentLang() {
    return (langSelect && langSelect.value) || "en";
  }

  function t(key, fallback) {
    const dict = translations[currentLang()];
    return (dict && dict[key]) || fallback;
  }

  // Small public helper so page-specific scripts (feedback.js) can reuse the
  // loaded translations for dynamic status messages.
  window.checklistI18n = { t, lang: currentLang };

  function applyTheme(theme) {
    root.setAttribute("data-theme", theme);
    try { localStorage.setItem("checklist-theme", theme); } catch (e) {}
    if (themeBtn) {
      themeBtn.textContent = theme === "dark"
        ? t("theme_toggle_light", "Light mode")
        : t("theme_toggle_dark", "Dark mode");
    }
  }

  // Dynamic texts (status lines, generated dropdown placeholders) are re-rendered
  // here when the language changes, since data-i18n only covers static markup.
  const onLanguageChange = [];

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
    onLanguageChange.forEach((fn) => { try { fn(); } catch (e) { console.error(e); } });
  }

  function loadLang(lang) {
    if (translations[lang]) {
      applyTranslations(lang);
      applyTheme(root.getAttribute("data-theme"));
      return;
    }
    fetch(`/i18n/${lang}.json?v=${encodeURIComponent(assetVersion)}`)
      .then((r) => r.json())
      .then((dict) => {
        translations[lang] = dict;
        applyTranslations(lang);
        applyTheme(root.getAttribute("data-theme"));
      })
      .catch((err) => console.error("failed to load translations", lang, err));
  }

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

  function el(tag, cls, text) {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined) e.textContent = text;
    return e;
  }

  // ---- Test type -> Slack channel hint ----
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

  // ---- Tag / Compare-against dropdowns and the diff summary ----
  // Both are real <select>s (a datalist only shows suggestions matching the
  // text already typed, which hid every other build once auto-filled).
  const tagSelect = document.getElementById("tag-select");
  const diffBase = document.getElementById("diff-base");
  const diffStatus = document.getElementById("diff-status");
  const diffResults = document.getElementById("diff-results");
  const diffJson = document.getElementById("diff-json");
  const diffLink = document.getElementById("diff-compare-link");
  const diffReload = document.getElementById("diff-reload");
  const tagFamily = (tag) => tag.replace(/\d{4}-\d{2}-\d{2}(-\d+)?$/, "");
  let knownTags = []; // newest first, for the selected test type
  let lastDiffKey = "";
  let statusKey = "diff_hint"; // which i18n key the status line currently shows, "" for custom text

  function setDiffStatus(text, kind, key) {
    if (!diffStatus) return;
    statusKey = key || "";
    diffStatus.textContent = text;
    diffStatus.className = "muted small" + (kind ? " " + kind : "");
  }
  onLanguageChange.push(() => { if (statusKey) setDiffStatus(t(statusKey, diffStatus.textContent), "", statusKey); });

  // Newest build of the selected kind; skip namespaced variants like "verified/...".
  function latestTag(tags) {
    return tags.find((x) => !x.includes("/")) || tags[0] || "";
  }

  // Previous build of the same kind as head (list is newest first).
  function previousOf(head) {
    const fam = tagFamily(head);
    for (let i = knownTags.indexOf(head) + 1; i < knownTags.length; i++) {
      if (tagFamily(knownTags[i]) === fam) return knownTags[i];
    }
    return "";
  }

  function fillSelect(sel, values, selected, emptyText) {
    if (!sel) return;
    sel.innerHTML = "";
    if (!values.length) {
      const o = el("option", "", emptyText);
      o.value = "";
      sel.appendChild(o);
      sel.disabled = true;
      return;
    }
    values.forEach((v) => {
      const o = el("option", "", v);
      o.value = v;
      if (v === selected) o.selected = true;
      sel.appendChild(o);
    });
    sel.disabled = false;
  }

  function resetDiff() {
    lastDiffKey = "";
    if (diffResults) diffResults.innerHTML = "";
    if (diffJson) diffJson.value = "";
    if (diffLink) diffLink.hidden = true;
    setDiffStatus(t("diff_hint", "The latest build of the selected test type is pre-selected and compared with the one before it."), "", "diff_hint");
  }

  function fillBaseOptions(head) {
    const fam = tagFamily(head);
    const options = knownTags.filter((x) => x !== head && tagFamily(x) === fam);
    fillSelect(diffBase, options, previousOf(head), t("diff_no_previous", "No earlier build of this kind"));
  }

  // Loads the builds for the chosen test type, pre-selects the latest one and
  // the one before it, then loads the diff. Nothing is loaded until a type is chosen.
  function loadTags() {
    if (!testTypeSelect || !tagSelect) return;
    const type = testTypeSelect.value;
    resetDiff();
    knownTags = [];
    if (!type) {
      fillSelect(tagSelect, [], "", t("tag_placeholder", "Select a test type first"));
      fillSelect(diffBase, [], "", t("tag_placeholder", "Select a test type first"));
      return;
    }
    fillSelect(tagSelect, [], "", t("tags_loading", "Loading builds…"));
    fillSelect(diffBase, [], "", t("tags_loading", "Loading builds…"));
    const requestedType = type;
    fetch(`/api/tags?test_type=${encodeURIComponent(type)}`)
      .then((r) => r.json())
      .then((tags) => {
        if (testTypeSelect.value !== requestedType) return; // user switched again meanwhile
        knownTags = Array.isArray(tags) ? tags : [];
        const latest = latestTag(knownTags);
        fillSelect(tagSelect, knownTags, latest, t("tags_none", "No builds found"));
        onTagChange();
      })
      .catch((err) => {
        console.error("failed to load tags", err);
        fillSelect(tagSelect, [], "", t("tags_none", "No builds found"));
        fillSelect(diffBase, [], "", t("tags_none", "No builds found"));
      });
  }

  function onTagChange() {
    const head = tagSelect ? tagSelect.value : "";
    if (!head) { resetDiff(); return; }
    fillBaseOptions(head);
    maybeLoadDiff(true);
  }

  // "2026-09-01" -> "Sep 1"; "2026-08-26-01" -> "Aug 26 #01" (localised month names via i18n).
  function friendlyDate(stamp) {
    if (!stamp || stamp.length < 10) return stamp || "";
    const [y, m, d] = stamp.slice(0, 10).split("-").map(Number);
    if (!m || !d) return stamp;
    const months = t("months_short", "Jan,Feb,Mar,Apr,May,Jun,Jul,Aug,Sep,Oct,Nov,Dec").split(",");
    let s = currentLang() === "ja" ? `${m}月${d}日` : `${months[m - 1]} ${d}`;
    if (stamp.length > 11) s += " #" + stamp.slice(11);
    return s;
  }

  let lastDiffData = null;

  const areaEmoji = { hmi: "🖥️", behavior: "🚦", planner: "🗺️", prediction: "🔮", fixes: "🛠️" };

  function countWord(n, oneKey, manyKey, one, many) {
    return `${n} ${n === 1 ? t(oneKey, one) : t(manyKey, many)}`;
  }

  // Plain-language sentence for an area: the model's when available for the
  // current language, otherwise a fixed template.
  function simpleSentence(d, c) {
    const total = c.items.length + c.undescribed;
    const s = d.simple && d.simple[currentLang()] && d.simple[currentLang()].areas && d.simple[currentLang()].areas[c.key];
    if (s && s.sentence) return s.sentence;
    if (total === 0) return t("simple_" + c.key + "_none", "No changes here.");
    const n = c.key === "fixes"
      ? countWord(total, "word_fix", "word_fixes", "fix", "fixes")
      : countWord(total, "word_change", "word_changes", "change", "changes");
    return t("simple_" + c.key + "_some", "{n} here.").replace("{n}", n);
  }

  // Where a change runs, as a short tag: {cls, text}. Unknown -> null.
  const impactEmoji = { visible: "👀", driving: "🚚", internal: "⚙️" };
  function impactTag(impact) {
    if (!impact || !impactEmoji[impact]) return null;
    return { cls: "badge badge-impact badge-" + impact, text: impactEmoji[impact] + " " + t("impact_" + impact, impact) };
  }
  const affectsBehavior = (impact) => impact === "visible" || impact === "driving";

  // "1 affects how the truck drives; 2 run on the truck but are hard to spot."
  function impactSentence(counts) {
    if (!counts) return "";
    const parts = [];
    const add = (n, oneKey, manyKey, one, many) => { if (n > 0) parts.push(`${n} ${n === 1 ? t(oneKey, one) : t(manyKey, many)}`); };
    add(counts.visible || 0, "impact_s_visible_one", "impact_s_visible", "is easy to spot on the truck", "are easy to spot on the truck");
    add(counts.driving || 0, "impact_s_driving_one", "impact_s_driving", "affects how the truck drives", "affect how the truck drives");
    add(counts.internal || 0, "impact_s_internal_one", "impact_s_internal", "runs on the truck but is hard to spot", "run on the truck but are hard to spot");
    add(counts.unknown || 0, "impact_s_unknown", "impact_s_unknown", "could not be checked", "could not be checked");
    if (!parts.length) return "";
    const s = parts.join(t("list_sep", "; "));
    return s.charAt(0).toUpperCase() + s.slice(1) + t("sentence_end", ".");
  }

  // Model-written bullets for the current language, or null to use templates.
  function modelBullets(d, c) {
    const s = d.simple && d.simple[currentLang()] && d.simple[currentLang()].areas && d.simple[currentLang()].areas[c.key];
    return s && Array.isArray(s.bullets) ? s.bullets : null;
  }

  // Prefer the pull request page; fall back to the commit page.
  const bestLink = (it) => it.pr_url || it.url || "#";
  const linkLabel = (it) => (it.pr ? `PR #${it.pr}` : t("link_commit", "commit"));

  // "PR #113119 on GitHub · commit" link row for a change.
  function githubLinks(it) {
    const row = el("div", "diff-links muted small");
    const a = el("a", "", linkLabel(it) + " " + t("link_on_github", "on GitHub"));
    a.href = bestLink(it); a.target = "_blank"; a.rel = "noopener";
    row.appendChild(a);
    if (it.pr_url && it.url) {
      row.appendChild(document.createTextNode(" · "));
      const c = el("a", "", t("link_commit", "commit"));
      c.href = it.url; c.target = "_blank"; c.rel = "noopener";
      row.appendChild(c);
    }
    return row;
  }

  // Card for a change that affects the truck's behavior (or the driver's screen).
  function behaviorCard(it) {
    const card = el("div", "diff-behavior" + (it.needs_info ? " needs-info" : ""));
    const tagText = it.impact === "visible" ? "👀 " + t("behavior_visible", "You'll see it on the driver's screen") : "🚚 " + t("behavior_driving", "Changes how the truck drives");
    card.appendChild(el("div", "diff-behavior-tag", tagText));
    const title = el("div", "diff-behavior-title");
    const a = el("a", "", it.plain_title || it.headline || it.title);
    a.href = bestLink(it); a.target = "_blank"; a.rel = "noopener";
    title.appendChild(a);
    if (it.is_revert) title.appendChild(el("span", "badge badge-revert", t("badge_revert", "reverted")));
    card.appendChild(title);
    if (it.plain_title && it.headline && it.plain_title !== it.headline) {
      card.appendChild(el("div", "muted small", t("behavior_original", "Engineer's title") + ": " + it.headline));
    }
    card.appendChild(githubLinks(it));
    const note = it.kind ? t("note_" + it.kind, it.note || "") : (it.note || "");
    if (note) {
      const p = el("p", "diff-behavior-note");
      p.appendChild(el("strong", "", t("behavior_meaning", "What this means") + ": "));
      p.appendChild(document.createTextNode((it.impact === "visible" ? t("note_visible_prefix", "On the driver's screen or in the sounds it makes. ") : "") + note));
      card.appendChild(p);
    }
    if (it.excerpt) {
      const p = el("p", "diff-behavior-excerpt muted");
      p.appendChild(el("em", "", t("behavior_engineer_wrote", "The engineer wrote") + ": "));
      p.appendChild(document.createTextNode(it.excerpt));
      card.appendChild(p);
    }
    if (it.needs_info) {
      card.appendChild(el("p", "diff-flag", "⚠️ " + t("flag_no_description", "No description — ask the author what this changes before testing.")));
    }
    return card;
  }

  function renderDiff(d) {
    lastDiffData = d;
    diffResults.innerHTML = "";
    if (diffLink) {
      diffLink.href = d.compare_url || "#";
      diffLink.hidden = !d.compare_url;
    }

    // "57 changes on the truck since the previous build (Aug 31 → Sep 1) — 10 you might notice"
    const head = el("p", "diff-headline");
    const from = friendlyDate(d.base_date) || d.base, to = friendlyDate(d.head_date) || d.head;
    const onTruck = (d.on_truck || d.on_truck === 0) && d.ignored !== undefined ? d.on_truck : d.total_commits;
    const n = countWord(onTruck, "word_change", "word_changes", "change", "changes");
    head.appendChild(el("strong", "", t("diff_total_line", "{n} on the truck since the previous build").replace("{n}", n)));
    head.appendChild(document.createTextNode(` (${from} → ${to})`));
    if (d.impact) {
      const notice = (d.impact.visible || 0) + (d.impact.driving || 0);
      head.appendChild(document.createTextNode(" — "));
      head.appendChild(el("strong", notice > 0 ? "diff-notice" : "muted", t("impact_notice", "{n} you might notice").replace("{n}", notice)));
    }
    diffResults.appendChild(head);
    if (d.ignored > 0) {
      diffResults.appendChild(el("p", "muted small diff-ignored", t("impact_ignored", "{n} tools, simulation and test changes are not shown — they don't run on the truck.").replace("{n}", d.ignored)));
    }

    const simple = d.simple && d.simple[currentLang()];
    const overall = (simple && simple.overall) || (currentLang() === "en" ? d.ai_summary : "");
    if (overall) {
      const box = el("div", "diff-ai");
      box.appendChild(el("strong", "", t("diff_ai_summary", "In short") + ": "));
      box.appendChild(document.createTextNode(overall));
      diffResults.appendChild(box);
    }

    d.categories.forEach((c) => {
      const total = c.items.length + c.undescribed;
      const row = el("div", "diff-area");
      const title = el("div", "diff-area-title");
      title.appendChild(el("span", "diff-emoji", areaEmoji[c.key] || "•"));
      title.appendChild(el("strong", "", t("cat_" + c.key, c.label)));
      title.appendChild(el("span", "badge diff-count", String(total)));
      row.appendChild(title);
      let sentence = simpleSentence(d, c);
      const modelSentence = d.simple && d.simple[currentLang()] && d.simple[currentLang()].areas && d.simple[currentLang()].areas[c.key] && d.simple[currentLang()].areas[c.key].sentence;
      if (total > 0 && !modelSentence) {
        const is = impactSentence(c.impact);
        if (is) sentence += " " + is;
      }
      row.appendChild(el("p", "diff-simple-sentence" + (total === 0 ? " muted" : ""), sentence));

      const mb = modelBullets(d, c);
      if (mb) {
        if (mb.length) {
          const ul = el("ul", "diff-simple-list");
          mb.forEach((text, i) => {
            const li = el("li", "", text);
            const it = c.items[i];
            if (it) {
              li.appendChild(document.createTextNode(" "));
              const a = el("a", "diff-pr-link", linkLabel(it));
              a.href = bestLink(it); a.target = "_blank"; a.rel = "noopener";
              li.appendChild(a);
              if (it.needs_info) li.appendChild(el("span", "diff-flag-inline", " ⚠️ " + t("flag_no_description_short", "no description — ask the author")));
            }
            ul.appendChild(li);
          });
          row.appendChild(ul);
        }
      } else {
        // Behavior-affecting changes as cards, the rest as a compact list.
        c.items.filter((it) => affectsBehavior(it.impact)).forEach((it) => row.appendChild(behaviorCard(it)));
        const rest = c.items.filter((it) => !affectsBehavior(it.impact));
        if (rest.length) {
          const ul = el("ul", "diff-simple-list");
          rest.forEach((it) => {
            const li = el("li");
            const a = el("a", "diff-title", it.plain_title || it.headline || it.title);
            a.href = bestLink(it); a.target = "_blank"; a.rel = "noopener";
            li.appendChild(a);
            const tag = impactTag(it.impact);
            if (tag) li.appendChild(el("span", tag.cls, tag.text));
            li.appendChild(document.createTextNode(" "));
            const pr = el("a", "diff-pr-link muted small", linkLabel(it));
            pr.href = bestLink(it); pr.target = "_blank"; pr.rel = "noopener";
            li.appendChild(pr);
            ul.appendChild(li);
          });
          row.appendChild(ul);
        }
      }
      if (c.undescribed_driving > 0) {
        const key = c.undescribed_driving === 1 ? "flag_undescribed_driving_one" : "flag_undescribed_driving";
        const fb = c.undescribed_driving === 1 ? "1 automatic update touched driving or driver-facing code with no description — worth asking about before testing." : "{u} automatic updates touched driving or driver-facing code with no description — worth asking about before testing.";
        row.appendChild(el("p", "diff-flag", "⚠️ " + t(key, fb).replace("{u}", c.undescribed_driving)));
        // These syncs have no pull request: show the commit, where it came from and what it touched.
        (c.flagged || []).forEach((f) => {
          const box = el("div", "diff-flagged");
          const line = el("div");
          const a = el("a", "", t("flag_sync", "Automatic sync") + " " + (f.sha || "").slice(0, 8));
          a.href = f.url || "#"; a.target = "_blank"; a.rel = "noopener";
          line.appendChild(a);
          let why = " — " + t("flag_no_pr", "no pull request (copied in from the main repository");
          if (f.origin) why += ", " + t("flag_origin", "source revision") + " " + f.origin.slice(0, 8);
          why += ")";
          line.appendChild(el("span", "muted small", why));
          box.appendChild(line);
          const files = (f.files && f.files.length) ? f.files : (f.dirs || []);
          if (files.length) box.appendChild(el("div", "muted small diff-files", t("flag_files", "Files") + ": " + files.join(", ")));
          row.appendChild(box);
        });
      }
      const restUndescribed = c.undescribed - (c.undescribed_driving || 0);
      if (restUndescribed > 0 && total > 0) {
        const key = restUndescribed === 1 ? "simple_undescribed_one" : "simple_undescribed";
        const fb = restUndescribed === 1 ? "1 more is a small automatic update without a description (hard to spot)." : "{u} more are small automatic updates without a description (hard to spot).";
        row.appendChild(el("p", "muted small", t(key, fb).replace("{u}", restUndescribed)));
      }
      diffResults.appendChild(row);
    });

    if (d.other_count > 0) {
      let text = t("simple_other", "{n} on the truck that don't affect these areas").replace("{n}", countWord(d.other_count, "word_other_change", "word_other_changes", "other change", "other changes"));
      if (d.other_automated === 1) text += " (" + t("simple_other_auto_one", "1 of them is an automatic system update") + ")";
      else if (d.other_automated > 1) text += " (" + t("simple_other_auto", "{m} of them are automatic system updates").replace("{m}", d.other_automated) + ")";
      diffResults.appendChild(el("p", "muted diff-other", text + "."));
    }
    if (d.note) diffResults.appendChild(el("p", "muted small", d.note));

    // Engineering detail, folded away.
    const tech = el("details", "diff-tech");
    tech.appendChild(el("summary", "muted small", t("diff_technical", "Show technical details")));
    renderTechnical(d, tech);
    diffResults.appendChild(tech);
  }

  function renderTechnical(d, container) {
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
        container.appendChild(details);
        return;
      }
      if (c.items.length) {
        const ul = el("ul", "diff-list");
        c.items.forEach((it) => {
          const li = el("li");
          const a = el("a", "diff-title", it.headline || it.title);
          a.href = bestLink(it); a.target = "_blank"; a.rel = "noopener";
          li.appendChild(a);
          if (it.is_revert) li.appendChild(el("span", "badge badge-revert", t("badge_revert", "reverted")));
          else if (it.is_fix && c.key !== "fixes") li.appendChild(el("span", "badge badge-fix", t("badge_fix", "fix")));
          const tag = impactTag(it.impact);
          if (tag) li.appendChild(el("span", tag.cls, tag.text));
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
      container.appendChild(details);
    });

    if (d.other_count > 0) {
      const p = el("p", "muted diff-other");
      let text = `${d.other_count} ${t("diff_other_line", "other changes outside these areas")}`;
      if (d.other_automated > 0) text += ` (${d.other_automated} ${t("diff_automated", "automated system updates")})`;
      p.appendChild(document.createTextNode(text + " — "));
      const a = el("a", "", t("diff_compare_link", "Full list on GitHub"));
      a.href = d.compare_url || "#"; a.target = "_blank"; a.rel = "noopener";
      p.appendChild(a);
      container.appendChild(p);
    }
    if (d.truncated) container.appendChild(el("p", "muted small", t("diff_truncated", "Large diff: some changes were classified by title only.")));
  }
  // Re-render the loaded summary in the new language.
  onLanguageChange.push(() => { if (lastDiffData && diffResults && diffResults.childElementCount) renderDiff(lastDiffData); });

  function maybeLoadDiff(force) {
    if (!tagSelect || !diffResults) return;
    const head = tagSelect.value;
    if (!head) return;
    const base = diffBase ? diffBase.value : "";
    const key = head + "|" + base;
    if (!force && key === lastDiffKey) return;
    lastDiffKey = key;
    setDiffStatus(t("diff_loading", "Loading changes…"), "", "diff_loading");
    diffResults.innerHTML = "";
    lastDiffData = null;
    if (diffJson) diffJson.value = "";
    const q = new URLSearchParams({ head });
    if (base) q.set("base", base);
    fetch(`/api/diff?${q}`)
      .then(async (r) => ({ ok: r.ok, status: r.status, data: await r.json().catch(() => ({})) }))
      .then(({ ok, status, data }) => {
        if (head + "|" + base !== lastDiffKey) return; // a newer selection superseded this request
        if (!ok) {
          setDiffStatus(t("diff_error", "Could not load the changes:") + " " + (data.error || status), "error");
          return;
        }
        if (diffBase && data.base && diffBase.value !== data.base) {
          if (!Array.from(diffBase.options).some((o) => o.value === data.base)) {
            const o = el("option", "", data.base); o.value = data.base; diffBase.appendChild(o);
          }
          diffBase.value = data.base;
        }
        setDiffStatus(`${t("diff_builds", "Builds compared")}: ${data.base} → ${data.head}`, "");
        renderDiff(data);
        if (diffJson) diffJson.value = JSON.stringify(data);
      })
      .catch((err) => setDiffStatus(t("diff_error", "Could not load the changes:") + " " + err, "error"));
  }

  if (testTypeSelect) testTypeSelect.addEventListener("change", loadTags);
  if (tagSelect) tagSelect.addEventListener("change", onTagChange);
  if (diffBase) diffBase.addEventListener("change", () => maybeLoadDiff(true));
  if (diffReload) diffReload.addEventListener("click", () => maybeLoadDiff(true));
  loadTags(); // no type selected on first load -> dropdowns stay disabled until the tester picks one

  // ---- Test Engineer suggestions: members of the access groups (see engineers.go). ----
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
