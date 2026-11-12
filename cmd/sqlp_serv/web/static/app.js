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

// htmx copies an element's class and style onto the one that replaces it, for
// CSS transitions. The page's CSP refuses style attributes, and CodeMirror
// leaves one on the SQL textarea, so htmx must not copy style.
if (typeof htmx !== "undefined") {
  htmx.config.attributesToSettle = ["class", "width", "height"];
}

// The SQL editor highlights SQL in the chosen dialect, with CodeMirror 5
// (static/codemirror, unchanged from its npm release 5.65.21). The textarea
// stays the form's field: the editor copies its text there on every change,
// so htmx sends it like any other field.
const sqlModes = { postgres: "text/x-pgsql", sqlite: "text/x-sqlite", bigquery: "text/x-bigquery" };
let editor = null;
let resized = null;

// CodeMirror has no BigQuery mode. This one is its SQL mode with BigQuery's
// reserved words and types, "..." strings, # comments, and MySQL's handling of
// `quoted` names and @parameters.
if (typeof CodeMirror !== "undefined") {
  const words = (s) => Object.fromEntries(s.split(" ").map((w) => [w, true]));
  const mysql = CodeMirror.mimeModes["text/x-mysql"];
  CodeMirror.defineMIME("text/x-bigquery", {
    name: "sql",
    keywords: words(
      "all and any array as asc assert_rows_modified at between by case cast collate contains create cross " +
        "cube current default define desc distinct else end enum escape except exclude exists extract false " +
        "fetch following for from full graph_table group grouping groups hash having if ignore in inner " +
        "intersect interval into is join lateral left like limit lookup merge natural new no not null nulls of " +
        "on or order outer over partition preceding proto qualify range recursive respect right rollup rows " +
        "select set some struct tablesample then to treat true unbounded union unnest using when where window " +
        "with within offset ordinal replace options insert update delete truncate drop alter table view " +
        "function procedure schema temp temporary returns declare begin commit rollback",
    ),
    builtin: words(
      "int64 float64 numeric bignumeric decimal bigdecimal bool string bytes date datetime time timestamp json geography",
    ),
    atoms: words("false true null"),
    operatorChars: /^[*+\-\/<>!=&|^~]/,
    dateSQL: words("date datetime time timestamp"),
    support: words("doubleQuote commentHash zerolessFloat hexNumber"),
    hooks: { "@": mysql.hooks["@"], "`": mysql.hooks["`"] },
  });
}

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

// Each dialect has its own sample query and catalog (the samples block in
// the page). On a dialect change, an editor that still holds one of them gets
// the new dialect's; an edited one is left alone. This listens in the capture
// phase, so the run the change starts already sends the new sample.
let samples = {};
try {
  samples = JSON.parse(document.getElementById("samples").textContent);
} catch (e) {
  // Without the block, dialect changes keep the editors as they are.
}

function swapSample(name, dialect) {
  const el = field(name);
  const own = samples[dialect];
  if (!el || !own || el.value === own[name] || !Object.values(samples).some((s) => s[name] === el.value)) {
    return;
  }
  if (name === "sql" && editor) {
    editor.setValue(own.sql); // its change handler copies the text to the textarea
  } else {
    el.value = own[name];
  }
  if (kept.includes(name)) {
    save(el);
  }
}

document.addEventListener(
  "change",
  (e) => {
    if (e.target.name !== "dialect") {
      return;
    }
    if (editor) {
      editor.setOption("mode", sqlMode());
    }
    swapSample("sql", e.target.value);
    swapSample("catalog", e.target.value);
  },
  true,
);

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
// "Sample query" does too: put the SQL editor on it. Both ask for the sample
// of the chosen dialect, so the path carries a query string.
document.addEventListener("htmx:afterSettle", (e) => {
  const path = (e.detail && e.detail.pathInfo && e.detail.pathInfo.requestPath) || "";
  const el = field("catalog");
  if (path.startsWith("/sample/catalog") && el) {
    save(el);
  }
  if (path.startsWith("/sample/sql") && !editor) {
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
