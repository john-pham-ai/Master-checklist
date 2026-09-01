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
})();
