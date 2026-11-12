// Small conveniences on top of htmx. The page works without them.

// The catalog editor and its switches are kept in this browser, so an edited
// catalog survives a reload. The SQL editor always starts with the sample.
const kept = ["catalog", "use_catalog", "expand"];

function field(name) {
  return document.querySelector(`#form [name="${name}"]`);
}

function save(el) {
  try {
    localStorage.setItem("sqlp." + el.name, el.type === "checkbox" ? String(el.checked) : el.value);
  } catch (e) {
    // Storage can be unavailable (private windows, blocked site data).
  }
}

function restore() {
  for (const name of kept) {
    const el = field(name);
    let value = null;
    try {
      value = localStorage.getItem("sqlp." + name);
    } catch (e) {
      return;
    }
    if (el && value !== null) {
      if (el.type === "checkbox") {
        el.checked = value === "true";
      } else {
        el.value = value;
      }
    }
  }
}

// Deferred scripts run before htmx starts on DOMContentLoaded, so the first
// run already uses the restored catalog.
restore();

// The SQL editor highlights SQL in the chosen dialect, with CodeMirror 5
// (static/codemirror, unchanged from its npm release 5.65.21). The textarea
// stays the form's field: the editor copies its text there on every change,
// so htmx sends it like any other field.
const sqlModes = { postgres: "text/x-pgsql", sqlite: "text/x-sqlite" };
let editor = null;
let resized = null;

// CodeMirror's PostgreSQL keywords include the standard's one-letter ones
// (C, P, T, ...), which would colour some of the one-letter aliases most
// queries use, and not others.
if (typeof CodeMirror !== "undefined") {
  for (const mime of Object.values(sqlModes)) {
    const words = CodeMirror.mimeModes[mime].keywords;
    for (const w of Object.keys(words)) {
      if (w.length === 1) {
        delete words[w];
      }
    }
  }
}

function sqlMode() {
  const el = field("dialect");
  return (el && sqlModes[el.value]) || "text/x-sql";
}

function startEditor() {
  const el = document.getElementById("sql");
  if (!el || typeof CodeMirror === "undefined") {
    return;
  }
  editor = CodeMirror.fromTextArea(el, {
    mode: sqlMode(),
    lineNumbers: true,
    indentUnit: 2,
    screenReaderLabel: "SQL",
  });
  editor.on("change", (cm) => cm.save());
  // It can be resized like the textarea, and must then measure itself again.
  resized = new ResizeObserver(() => editor && editor.refresh());
  resized.observe(editor.getWrapperElement());
}

function stopEditor() {
  if (editor) {
    const el = editor.getTextArea();
    resized.disconnect();
    editor.toTextArea();
    // CodeMirror leaves a style attribute behind. htmx would copy it onto the
    // textarea it swaps in, and the page's CSP refuses style attributes.
    el.removeAttribute("style");
    editor = null;
  }
}

startEditor();

document.addEventListener("change", (e) => {
  if (e.target.name === "dialect" && editor) {
    editor.setOption("mode", sqlMode());
  }
});

// "Sample query" swaps in a fresh textarea: take the editor off the old one
// first, and put it on the new one once it is in (htmx:afterSettle below).
document.addEventListener("htmx:beforeSwap", (e) => {
  if (e.detail.shouldSwap && e.detail.target && e.detail.target.id === "sql") {
    stopEditor();
  }
});

for (const type of ["input", "change"]) {
  document.addEventListener(type, (e) => {
    if (kept.includes(e.target.name)) {
      save(e.target);
    }
  });
}

// "Sample catalog" swaps in a fresh editor: remember it like an edit.
// "Sample query" does too: put the SQL editor on it.
document.addEventListener("htmx:afterSettle", (e) => {
  const path = e.detail && e.detail.pathInfo && e.detail.pathInfo.requestPath;
  const el = field("catalog");
  if (path === "/sample/catalog" && el) {
    save(el);
  }
  if (path === "/sample/sql" && !editor) {
    startEditor();
  }
});

// Format pretty-prints the catalog JSON, or says where it is broken.
document.addEventListener("click", (e) => {
  const button = e.target.closest("[data-format]");
  if (!button) {
    return;
  }
  const el = field(button.dataset.format);
  try {
    el.value = JSON.stringify(JSON.parse(el.value), null, 2) + "\n";
    el.setCustomValidity("");
    save(el);
  } catch (err) {
    el.setCustomValidity(err.message);
    el.reportValidity();
    el.addEventListener("input", () => el.setCustomValidity(""), { once: true });
  }
});

// Ctrl+Enter (Cmd+Enter on a Mac) runs the form from anywhere on the page.
document.addEventListener("keydown", (e) => {
  if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
    e.preventDefault();
    document.getElementById("form").requestSubmit();
  }
});

// htmx leaves the page as it was when a request fails, so say what happened.
function showFailure(message) {
  const box = document.createElement("div");
  box.className = "error-box";
  box.textContent = message;
  document.getElementById("result").replaceChildren(box);
}

document.addEventListener("htmx:responseError", (e) => {
  const status = e.detail.xhr.status;
  const known = {
    413: "The request is too large.",
    429: "Too many runs in a short time. Wait a moment and try again.",
  };
  showFailure(known[status] || `The server answered with an error (${status}). Try again in a moment.`);
});

document.addEventListener("htmx:sendError", () => {
  showFailure("The server could not be reached. Try again in a moment.");
});
