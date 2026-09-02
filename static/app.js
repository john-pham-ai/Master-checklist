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
  function loadTagSuggestions() {
    if (!testTypeSelect || !tagList) return;
    fetch(`/api/tags?test_type=${encodeURIComponent(testTypeSelect.value)}`)
      .then((r) => r.json())
      .then((tags) => {
        tagList.innerHTML = "";
        tags.forEach((tag) => {
          const option = document.createElement("option");
          option.value = tag;
          tagList.appendChild(option);
        });
      })
      .catch((err) => console.error("failed to load tag suggestions", err));
  }
  loadTagSuggestions();
  if (testTypeSelect) {
    testTypeSelect.addEventListener("change", loadTagSuggestions);
  }

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
