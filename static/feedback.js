(function () {
  const form = document.getElementById("feedback-form");
  if (!form) return;

  const status = document.getElementById("feedback-status");
  const connectBox = document.getElementById("feedback-connect");
  const connectBtn = document.getElementById("feedback-connect-btn");
  const submitBtn = document.getElementById("feedback-submit");
  const langSelect = document.getElementById("lang-select");
  const feedbackTo = form.getAttribute("data-feedback-to") || "";

  function t(key, fallback) {
    return (window.checklistI18n && window.checklistI18n.t(key, fallback)) || fallback;
  }

  function setStatus(text, kind) {
    status.textContent = text;
    status.className = "status " + (kind || "");
    status.hidden = !text;
  }

  // The platform injects the per-request token (needed to send mail as the
  // signed-in user) only when this cookie is present.
  function ensureTridentCookie() {
    if (!/(^|;\s*)trident=true/.test(document.cookie)) {
      document.cookie = "trident=true; path=/; SameSite=Lax" + (location.protocol === "https:" ? "; Secure" : "");
    }
  }

  async function send() {
    const message = document.getElementById("feedback-message").value.trim();
    if (!message) return;
    ensureTridentCookie();
    submitBtn.disabled = true;
    setStatus(t("feedback_sending", "Sending…"), "");
    const payload = {
      type: document.getElementById("feedback-type").value,
      subject: document.getElementById("feedback-subject").value,
      message: message,
      lang: langSelect ? langSelect.value : "en",
      page: location.href,
    };
    try {
      const r = await fetch("/api/feedback", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      });
      const data = await r.json().catch(() => ({}));
      if (r.ok) {
        let msg = t("feedback_sent", "Sent — thank you!");
        if (data.translated) msg += " " + t("feedback_translated_note", "(Automatically translated to English for the recipient.)");
        setStatus(msg, "ok");
        connectBox.hidden = true;
        document.getElementById("feedback-message").value = "";
        document.getElementById("feedback-subject").value = "";
        return;
      }
      if (data.needs_connect) {
        connectBox.hidden = false;
        setStatus(t("feedback_need_connect", "One-time step: connect Gmail below, then press Send again."), "error");
        return;
      }
      setStatus(t("feedback_error", "Could not send. Please email the owner directly:") + " " + feedbackTo +
        (data.detail ? " — " + data.detail : " (HTTP " + r.status + ")"), "error");
    } catch (err) {
      setStatus(t("feedback_error", "Could not send. Please email the owner directly:") + " " + feedbackTo + " — " + err, "error");
    } finally {
      submitBtn.disabled = false;
    }
  }

  form.addEventListener("submit", (e) => {
    e.preventDefault();
    send();
  });

  connectBtn.addEventListener("click", async () => {
    ensureTridentCookie();
    connectBtn.disabled = true;
    try {
      const r = await fetch("/api/feedback/connect", { method: "POST" });
      const data = await r.json().catch(() => ({}));
      if (!r.ok || !data.url) {
        setStatus(t("feedback_error", "Could not send. Please email the owner directly:") + " " + feedbackTo +
          (data.detail ? " — " + data.detail : ""), "error");
        return;
      }
      const popup = window.open(data.url, "_blank", "width=600,height=720");
      if (!popup) {
        setStatus(t("feedback_popup_blocked", "Your browser blocked the popup. Allow popups for this site and try again."), "error");
        return;
      }
      setStatus(t("feedback_connecting", "Finish the Google consent in the popup…"), "");
      const check = setInterval(() => {
        if (popup.closed) {
          clearInterval(check);
          connectBox.hidden = true;
          send(); // retry the pending message once the connection exists
        }
      }, 500);
    } finally {
      connectBtn.disabled = false;
    }
  });
})();
