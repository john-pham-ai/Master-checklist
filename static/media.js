// Per-check screenshot/video capture for the checklist form.
//
// Pasted screenshots and recorded/attached clips are kept as in-memory File
// objects here, then synced into the check's real (hidden) <input type=file>
// via the DataTransfer trick right before the form submits. That keeps the
// existing plain <form method=post enctype=multipart/form-data> submission
// working unchanged — no fetch/XHR upload path to maintain separately.
(function () {
  const t = (key, fallback) => (window.checklistI18n ? window.checklistI18n.t(key, fallback) : fallback);

  const state = new Map(); // checkKey -> { shots: File[], clips: File[] }
  const recorders = new Map(); // "checkKey:kind" -> MediaRecorder
  let activeContainer = null; // last .check-media the user interacted with; paste targets this one

  function getState(key) {
    if (!state.has(key)) state.set(key, { shots: [], clips: [] });
    return state.get(key);
  }

  function setStatus(container, text, isError) {
    const status = container.querySelector(".media-status");
    if (!status) return;
    status.hidden = !text;
    status.textContent = text || "";
    status.classList.toggle("error", !!isError);
  }

  // Browsers only let script assign a FileList built from a DataTransfer,
  // never an arbitrary array — this is that bridge, run after every add/remove
  // so the hidden inputs always mirror `state` for the native form submit.
  function syncInputs(container, key) {
    const s = getState(key);
    const shotInput = container.querySelector(".media-input-shot");
    const videoInput = container.querySelector(".media-input-video");
    const dtShots = new DataTransfer();
    s.shots.forEach((f) => dtShots.items.add(f));
    if (shotInput) shotInput.files = dtShots.files;
    const dtClips = new DataTransfer();
    s.clips.forEach((f) => dtClips.items.add(f));
    if (videoInput) videoInput.files = dtClips.files;
  }

  function renderList(container, key) {
    const list = container.querySelector(".media-list");
    if (!list) return;
    list.innerHTML = "";
    const s = getState(key);
    const entries = s.shots.map((f) => ({ f, kind: "image" })).concat(s.clips.map((f) => ({ f, kind: "video" })));
    entries.forEach(({ f, kind }) => {
      const item = document.createElement("div");
      item.className = "media-item";

      const thumb = document.createElement(kind === "video" ? "video" : "img");
      thumb.className = "media-thumb" + (kind === "video" ? " media-thumb-video" : "");
      thumb.src = URL.createObjectURL(f);
      if (kind === "video") thumb.controls = true;
      item.appendChild(thumb);

      const name = document.createElement("span");
      name.className = "media-name muted small";
      name.textContent = f.name;
      item.appendChild(name);

      const remove = document.createElement("button");
      remove.type = "button";
      remove.className = "media-remove";
      remove.textContent = "✕";
      remove.setAttribute("aria-label", t("media_remove", "Remove"));
      remove.addEventListener("click", () => {
        const arr = kind === "image" ? s.shots : s.clips;
        const i = arr.indexOf(f);
        if (i !== -1) arr.splice(i, 1);
        renderList(container, key);
        syncInputs(container, key);
      });
      item.appendChild(remove);

      list.appendChild(item);
    });
  }

  function addFiles(container, key, kind, files) {
    const s = getState(key);
    const arr = kind === "image" ? s.shots : s.clips;
    Array.from(files || []).forEach((f) => arr.push(f));
    renderList(container, key);
    syncInputs(container, key);
  }

  function markActive(container) {
    activeContainer = container;
  }

  // A short clip recorded live doesn't come with a filename, so name it after
  // the check and moment so the tester can still tell entries apart.
  function recordedFileName(key, kind) {
    return `${key}-${kind}-${Date.now()}.webm`;
  }

  function wireRecorder(container, key, btn, kind, getStream) {
    if (!btn) return;
    const idleLabel = btn.textContent;
    btn.addEventListener("click", async () => {
      const recKey = key + ":" + kind;
      const existing = recorders.get(recKey);
      if (existing) {
        existing.stop(); // onstop below finishes cleanup and re-enables the button
        return;
      }
      markActive(container);
      try {
        const stream = await getStream();
        const chunks = [];
        const recorder = new MediaRecorder(stream);
        recorder.ondataavailable = (e) => {
          if (e.data && e.data.size) chunks.push(e.data);
        };
        recorder.onstop = () => {
          stream.getTracks().forEach((track) => track.stop());
          recorders.delete(recKey);
          btn.textContent = idleLabel;
          btn.classList.remove("recording");
          setStatus(container, "");
          const blob = new Blob(chunks, { type: "video/webm" });
          const file = new File([blob], recordedFileName(key, kind), { type: "video/webm" });
          addFiles(container, key, "video", [file]);
        };
        // A tester closing the "share screen" browser dialog stops the track
        // directly, which never fires our button click — catch that too.
        stream.getVideoTracks()[0].addEventListener("ended", () => {
          if (recorder.state !== "inactive") recorder.stop();
        });
        recorder.start();
        recorders.set(recKey, recorder);
        btn.textContent = t("media_recording_stop", "⏹ Stop recording");
        btn.classList.add("recording");
        setStatus(container, t("media_recording_status", "Recording… click the button again to stop."));
      } catch (err) {
        setStatus(container, t("media_permission_error", "Could not start recording: ") + (err && err.message ? err.message : err), true);
      }
    });
  }

  function initCheck(container) {
    const key = container.getAttribute("data-check-key");
    if (!key) return;

    const shotBtn = container.querySelector(".media-btn-shot");
    const shotInput = container.querySelector(".media-input-shot");
    const videoBtn = container.querySelector(".media-btn-video");
    const videoInput = container.querySelector(".media-input-video");
    const screenBtn = container.querySelector(".media-btn-screen");
    const camBtn = container.querySelector(".media-btn-cam");

    container.addEventListener("click", () => markActive(container));
    container.addEventListener("focusin", () => markActive(container));

    if (shotBtn && shotInput) {
      shotBtn.addEventListener("click", () => {
        markActive(container);
        shotInput.click();
      });
      shotInput.addEventListener("change", () => addFiles(container, key, "image", shotInput.files));
    }
    if (videoBtn && videoInput) {
      videoBtn.addEventListener("click", () => {
        markActive(container);
        videoInput.click();
      });
      videoInput.addEventListener("change", () => addFiles(container, key, "video", videoInput.files));
    }

    wireRecorder(container, key, screenBtn, "screen", () => navigator.mediaDevices.getDisplayMedia({ video: true, audio: true }));
    wireRecorder(container, key, camBtn, "cam", () => navigator.mediaDevices.getUserMedia({ video: true, audio: true }));
  }

  const mediaSupported = !!(navigator.mediaDevices && window.MediaRecorder);

  document.querySelectorAll(".check-media").forEach((container) => {
    initCheck(container);
    if (!mediaSupported) {
      container.querySelectorAll(".media-btn-screen, .media-btn-cam").forEach((b) => {
        b.disabled = true;
        b.title = t("media_unsupported", "Not supported in this browser");
      });
    }
  });

  // Routes a clipboard-pasted image to whichever check the tester last clicked
  // into or focused — there is no single "paste target" input to listen on.
  document.addEventListener("paste", (e) => {
    if (!activeContainer) return;
    const clipboardData = e.clipboardData || window.clipboardData;
    if (!clipboardData) return;
    const files = [];
    Array.from(clipboardData.items || []).forEach((item) => {
      if (item.kind === "file" && item.type.startsWith("image/")) {
        const f = item.getAsFile();
        if (f) files.push(f);
      }
    });
    if (files.length) {
      e.preventDefault();
      const key = activeContainer.getAttribute("data-check-key");
      addFiles(activeContainer, key, "image", files);
    }
  });

  // One-time permission "warm-up" so recording during an actual check doesn't
  // stall on a browser prompt. getDisplayMedia always needs its own picker
  // (browsers won't let that be pre-armed), so this only primes the camera/mic.
  const setupBtn = document.getElementById("media-setup-btn");
  const setupStatus = document.getElementById("media-setup-status");
  if (setupBtn && setupStatus) {
    setupBtn.addEventListener("click", async () => {
      if (!mediaSupported) {
        setupStatus.hidden = false;
        setupStatus.classList.add("error");
        setupStatus.textContent = t("media_unsupported", "Not supported in this browser");
        return;
      }
      setupStatus.hidden = false;
      setupStatus.classList.remove("error");
      setupStatus.textContent = t("media_setup_checking", "Requesting camera/microphone permission…");
      try {
        const stream = await navigator.mediaDevices.getUserMedia({ video: true, audio: true });
        stream.getTracks().forEach((track) => track.stop());
        setupStatus.textContent = t("media_setup_ready", "Ready — screen and webcam recording can be started from any check below.");
      } catch (err) {
        setupStatus.classList.add("error");
        setupStatus.textContent = t("media_setup_error", "Camera/microphone permission was not granted: ") + (err && err.message ? err.message : err);
      }
    });
  }

  // Belt-and-suspenders: hidden inputs are already re-synced on every
  // add/remove, but do it once more right before submit in case some future
  // code path mutates `state` without going through addFiles/renderList.
  const form = document.getElementById("checklist-form");
  if (form) {
    form.addEventListener("submit", () => {
      document.querySelectorAll(".check-media").forEach((container) => {
        const key = container.getAttribute("data-check-key");
        if (key) syncInputs(container, key);
      });
    });
  }
})();
