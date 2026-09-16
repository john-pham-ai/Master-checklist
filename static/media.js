// Per-check screenshot/video capture for the checklist form.
//
// Pasted screenshots and recorded/attached clips are kept as in-memory File
// objects here, then synced into the check's real (hidden) <input type=file>
// via the DataTransfer trick right before the form submits. That keeps the
// existing plain <form method=post enctype=multipart/form-data> submission
// working unchanged — no fetch/XHR upload path to maintain separately.
//
// Incoming files are copied into app-owned in-memory Files (snapshotFile) and
// re-checked right before submit (fileIsReadable), because a long-lived tab
// can lose the bytes behind browser-owned File references (see snapshotFile).
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

  // A File handed over by a paste event or a file picker only *references*
  // data owned by the browser (clipboard storage, the picked file on disk).
  // In a tab left open for hours (overnight, laptop asleep) Chrome can drop
  // that backing data, and the form then submits the filename with 0 bytes —
  // which is exactly what produced empty attachments on a real run page.
  // Copying the bytes into a fresh in-memory File right away makes the
  // attachment independent of where it came from. Falls back to the original
  // File if the read fails, so a paste never silently disappears.
  async function snapshotFile(f) {
    try {
      const buf = await f.arrayBuffer();
      if (buf.byteLength === 0 && f.size > 0) return f; // unreadable already; keep the original for the pre-submit check
      return new File([buf], f.name, { type: f.type, lastModified: f.lastModified });
    } catch (err) {
      console.warn("could not snapshot attachment", f.name, err);
      return f;
    }
  }

  async function addFiles(container, key, kind, files) {
    const s = getState(key);
    const arr = kind === "image" ? s.shots : s.clips;
    const snapshots = await Promise.all(Array.from(files || []).map(snapshotFile));
    snapshots.forEach((f) => arr.push(f));
    renderList(container, key);
    syncInputs(container, key);
  }

  // Reads a File end to end and reports whether its bytes are still there.
  // The size property alone is not enough: a File whose backing data is
  // gone can still report its original size while the upload sends 0 bytes.
  async function fileIsReadable(f) {
    if (!f || f.size === 0) return false;
    try {
      const buf = await f.arrayBuffer();
      return buf.byteLength === f.size;
    } catch (err) {
      return false;
    }
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
  }

  const mediaSupported = !!(navigator.mediaDevices && window.MediaRecorder);

  document.querySelectorAll(".check-media").forEach((container) => {
    initCheck(container);
    if (!mediaSupported) {
      container.querySelectorAll(".media-btn-screen").forEach((b) => {
        b.disabled = true;
        b.title = t("media_unsupported", "Not supported in this browser");
      });
    }
  });

  // Finds the .check-media that a pasted image belongs to: the one in the
  // same check item (pasting into a check's Notes box attaches to that
  // check), or the notes media block next to "Notes on the changes".
  // Returns null when pasting outside any check / notes area.
  function mediaForElement(el) {
    if (!el || !el.closest) return null;
    const item = el.closest(".check-item");
    if (item) return item.querySelector(".check-media");
    const notesArea = el.closest("#diff-notes-area");
    if (notesArea) return notesArea.querySelector(".check-media");
    return null;
  }

  // Routes a clipboard-pasted image to the check the tester pasted into (the
  // Notes box of a check, or the notes area), falling back to whichever check
  // they last clicked or focused.
  document.addEventListener("paste", (e) => {
    const container = (e.target && e.target.closest ? mediaForElement(e.target) : null) || activeContainer;
    if (!container) return;
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
      markActive(container);
      const key = container.getAttribute("data-check-key");
      addFiles(container, key, "image", files);
    }
  });

  // Belt-and-suspenders: hidden inputs are already re-synced on every
  // add/remove, but do it once more right before submit in case some future
  // code path mutates `state` without going through addFiles/renderList.
  //
  // Every attachment is also readability-checked first: a File can lose the
  // bytes behind it while the tab stays open (see snapshotFile), and without
  // this check the form would happily POST it as 0 bytes and the Confluence
  // page would end up with empty attachment placeholders (a real incident:
  // six 0-byte attachments on the 2026-09-16 run page). Blocking the submit
  // with a message lets the tester re-capture instead of filing broken runs.
  const form = document.getElementById("checklist-form");
  if (form) {
    let checking = false;
    form.addEventListener("submit", async (e) => {
      if (checking) return; // second pass: the check passed, let it through
      e.preventDefault();
      checking = true;
      try {
        let firstBad = null;
        const bad = [];
        const all = [];
        document.querySelectorAll(".check-media").forEach((container) => {
          const key = container.getAttribute("data-check-key");
          if (key) {
            syncInputs(container, key);
            const s = getState(key);
            s.shots.forEach((f) => all.push({ container, f }));
            s.clips.forEach((f) => all.push({ container, f }));
          }
        });
        for (const { container, f } of all) {
          if (!(await fileIsReadable(f))) {
            bad.push(f);
            if (!firstBad) firstBad = { container, f };
          }
        }
        if (bad.length) {
          const list = bad.map((f) => f.name).join(", ");
          const msg = t("media_lost", "The browser lost the contents of: {list}. Remove the broken entries (they show as empty thumbnails) and re-add them, then submit again.").replace("{list}", list);
          setStatus(firstBad.container, msg, true);
          if (firstBad) firstBad.container.scrollIntoView({ behavior: "smooth", block: "center" });
          checking = false;
          return;
        }
        // All files read back fine — submit for real. requestSubmit runs the
        // same submit-event path; the guard above lets this second pass pass.
        if (form.requestSubmit) form.requestSubmit();
        else form.submit();
      } catch (err) {
        console.error("pre-submit attachment check failed", err);
        // Never trap the tester in the form: submit anyway.
        if (form.requestSubmit) form.requestSubmit();
        else form.submit();
      }
    });
  }
})();
