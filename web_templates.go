package main

// webTemplates is the whole dashboard: one login page and one home page, with
// two fragments the polling script swaps in place.
const webTemplates = `
{{define "head"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
{{if .Refresh}}<meta http-equiv="refresh" content="2">{{end}}
<style>
:root{color-scheme:dark;--bg:#0b0d10;--card:#12161c;--border:#242c36;--control:#5c6878;--text:#e6e9ee;--muted:#9aa6b5;--accent:#7cc4a4;--ink:#0b0d10;--ok:#6cc08b;--warn:#d9a441;--err:#e06c75}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);font-family:system-ui,-apple-system,"Segoe UI",sans-serif;font-size:16px;line-height:1.5}
.wrap{max-width:62rem;margin:0 auto;padding:24px 16px 32px}
@media(min-width:640px){.wrap{padding-left:24px;padding-right:24px}}
@media(min-width:900px){.wrap{padding-left:32px;padding-right:32px}}
header{display:flex;flex-wrap:wrap;gap:8px 16px;align-items:baseline;justify-content:space-between;border-bottom:1px solid var(--border);padding-bottom:16px;margin-bottom:24px}
.brand{font-size:1.25rem;font-weight:700;letter-spacing:.01em}
.sub,.muted{color:var(--muted)}
.mono,dt,dd,.metric{font-family:ui-monospace,"SF Mono",monospace;font-variant-numeric:tabular-nums}
.grid{display:grid;gap:24px;grid-template-columns:1fr;margin-bottom:24px;align-items:start}
@media(min-width:900px){.grid{grid-template-columns:1fr 1fr}}
.card{background:var(--card);border:1px solid var(--border);border-radius:10px;padding:24px}
.card h2{margin:0 0 16px;font-size:1.05rem;font-weight:700}
.stack{display:grid;gap:16px}
.btn{min-height:44px;display:inline-flex;align-items:center;justify-content:center;gap:8px;padding:12px 16px;border-radius:8px;border:1px solid var(--control);background:transparent;color:var(--text);font:inherit;font-weight:650;cursor:pointer;text-decoration:none}
.btn.primary{background:var(--accent);border-color:var(--accent);color:var(--ink)}
.btn:focus-visible,input:focus-visible,select:focus-visible,a:focus-visible{outline:2px solid var(--accent);outline-offset:2px}
.btn[disabled]{opacity:.6;cursor:progress}
.field{display:grid;gap:6px}
label{font-weight:650;font-size:.9rem}
input,select{min-height:44px;width:100%;background:#0e1217;color:var(--text);border:1px solid var(--control);border-radius:8px;padding:8px 12px;font:inherit}
input[type=checkbox]{min-height:auto;width:20px;height:20px;accent-color:var(--accent)}
.row{display:grid;gap:16px;grid-template-columns:1fr 1fr}
@media(max-width:480px){.row{grid-template-columns:1fr}}
.check{display:flex;align-items:center;gap:8px;min-height:44px}
.hint{color:var(--muted);font-size:.8rem;font-weight:400}
.err,.field-error{color:var(--err);font-size:.85rem}
.banner{border:1px solid var(--err);border-left-width:4px;border-radius:8px;padding:12px;margin:0 0 16px;background:#1a1214}
.pill{display:inline-block;padding:2px 10px;border:1px solid currentColor;border-radius:999px;font-size:.75rem;font-weight:700;letter-spacing:.05em;text-transform:uppercase}
.pill.ok{color:var(--ok)}
.pill.warn{color:var(--warn)}
.pill.err{color:var(--err)}
.metrics{display:grid;grid-template-columns:repeat(auto-fit,minmax(7rem,1fr));gap:8px 16px;margin:16px 0}
.metrics div{margin:0}
dt{color:var(--muted);font-size:.75rem;text-transform:uppercase;letter-spacing:.05em}
dd{margin:2px 0 0;font-size:1.1rem}
.meta{margin:8px 0;color:var(--muted);font-size:.85rem}
.parts{margin:0;padding-left:20px;font-size:.85rem;line-height:1.6}
.error-text{margin:12px 0 0;padding:12px;border:1px solid var(--err);border-radius:8px;background:#1a1214;white-space:pre-wrap;word-break:break-word;font-size:.85rem}
.plain{margin:0;padding:0;list-style:none}
.runs li{display:flex;flex-wrap:wrap;gap:4px 12px;align-items:center;padding:10px 0;border-bottom:1px solid var(--border)}
.runs li:last-child{border-bottom:0}
.login{max-width:26rem;margin:15vh auto 0}
@media(forced-colors:active){.card,.btn,input,select{border:1px solid ButtonText}.pill{border:1px solid currentColor}}
</style>
</head>
<body>
<div class="wrap">
{{end}}

{{define "header"}}<header><span class="brand">Kindle Digest</span><span class="sub">Daily digest · cron 06:45 IST</span></header>{{end}}

{{define "login"}}{{template "head" .}}
<main class="login">
<h1 class="brand">Kindle Digest</h1>
{{if .Error}}<p class="banner" role="alert">{{.Error}}</p>{{end}}
<form class="stack" method="post" action="/login">
<div class="field"><label for="token">Access token</label>
<input id="token" name="token" type="password" autocomplete="current-password" required></div>
<button class="btn primary" type="submit">Sign in</button>
</form>
</main>
</div></body></html>{{end}}

{{define "home"}}{{template "head" .}}
{{template "header" .}}
<main>
<div class="grid">
<section class="card">
<h2>Quick action</h2>
<form method="post" action="/run" data-run class="stack">
<input type="hidden" name="mode" value="daily">
<button class="btn primary" type="submit">Send today's digest now</button>
<p class="hint">Runs the same daily digest as cron and emails every part.</p>
</form>
</section>

<section class="card">
<h2>Custom run</h2>
<form method="post" action="/run" data-run class="stack">
<input type="hidden" name="mode" value="custom">
<div class="field"><label for="feed">Feed</label>
<select id="feed" name="feed">
<option value="">All feeds</option>
{{range .Feeds}}<option value="{{.ID}}"{{if eq (itoa .ID) $.Form.Feed}} selected{{end}}>{{.Title}}</option>{{end}}
</select></div>
<div class="row">
<div class="field"><label for="since">Since (IST)</label>
<input id="since" name="since" type="date" value="{{.Form.Since}}">
{{with index .Form.Errors "since"}}<p class="field-error">{{.}}</p>{{end}}</div>
<div class="field"><label for="until">Until (IST, exclusive)</label>
<input id="until" name="until" type="date" value="{{.Form.Until}}">
{{with index .Form.Errors "until"}}<p class="field-error">{{.}}</p>{{end}}</div>
</div>
<div class="field"><label for="limit">Limit</label>
<input id="limit" name="limit" type="number" min="1" max="500" value="{{.Form.Limit}}">
<span class="hint">1–500 articles, newest first.</span>
{{with index .Form.Errors "limit"}}<p class="field-error">{{.}}</p>{{end}}</div>
<label class="check"><input type="checkbox" name="send" value="1"{{if .Form.Send}} checked{{end}}> Send to Kindle</label>
<button class="btn" type="submit">Generate</button>
</form>
</section>
</div>

<section class="card" id="status" aria-live="polite">{{template "statusInner" .}}</section>

<section class="card" id="runs" style="margin-top:24px">{{template "runsInner" .}}</section>
</main>
</div>
<script>
(() => {
  const status = document.getElementById('status');
  const runs = document.getElementById('runs');
  function paint(d) {
    if (typeof d.status === 'string') status.innerHTML = d.status;
    if (typeof d.runs === 'string') runs.innerHTML = d.runs;
  }
  function enable(on) {
    document.querySelectorAll('form[data-run] button').forEach(b => { b.disabled = !on; });
  }
  function showError(text) {
    const h = document.createElement('h2');
    h.textContent = 'Run status';
    const p = document.createElement('p');
    p.className = 'banner';
    p.setAttribute('role', 'alert');
    p.textContent = text;
    status.replaceChildren(h, p);
  }
  async function poll() {
    const res = await fetch('/api/status');
    const d = await res.json();
    paint(d);
    if (d.running) { setTimeout(poll, 2000); } else { enable(true); }
  }
  document.addEventListener('submit', async (e) => {
    const form = e.target.closest('form[data-run]');
    if (!form) return;
    e.preventDefault();
    enable(false);
    const res = await fetch('/run', {method:'POST', body:new FormData(form), headers:{'Accept':'application/json'}});
    const d = await res.json();
    if (!res.ok) { showError(d.error); enable(true); return; }
    paint(d);
    poll();
  });
  if (document.querySelector('.pill.warn')) poll();
})();
</script>
</body></html>{{end}}

{{define "statusInner"}}
<h2>Run status</h2>
{{if .Error}}<p class="banner" role="alert">{{.Error}}</p>{{end}}
{{if .Record}}
<p><span class="pill {{.Record.StatusClass}}">{{.Record.StatusText}}</span>
{{with .Record.ModeText}} <span class="muted">{{.}}</span>{{end}}</p>
<p class="meta">{{.Record.Label}}</p>
{{if eq .Record.Outcome "running"}}
<p class="muted">Working… this card updates itself.</p>
{{else}}
<dl class="metrics">
<div><dt>Entries</dt><dd>{{.Record.Entries}}</dd></div>
<div><dt>Parts</dt><dd>{{len .Record.Parts}}</dd></div>
<div><dt>Bytes</dt><dd>{{bytes .Record.TotalBytes}}</dd></div>
<div><dt>Duration</dt><dd>{{duration .Record.DurationMS}}</dd></div>
</dl>
{{if .Record.Parts}}<ul class="parts mono">{{range .Record.Parts}}<li>{{.Name}} · {{bytes .Bytes}}</li>{{end}}</ul>{{end}}
{{with .Record.Error}}<pre class="error-text">{{.}}</pre>{{end}}
{{end}}
{{else}}
<p class="muted">No runs yet — send today's digest or start a custom run.</p>
{{end}}
{{end}}

{{define "runsInner"}}
<h2>Last 5 runs</h2>
{{if .Runs}}
<ul class="plain runs">
{{range .Runs}}<li>
<span class="pill {{.StatusClass}}">{{.StatusText}}</span>
<span class="mono">{{time .Started}}</span>
<span>{{.Label}}</span>
{{if ne .Outcome "running"}}<span class="muted mono">{{.Entries}} entries · {{len .Parts}} parts · {{bytes .TotalBytes}} · {{duration .DurationMS}}</span>{{end}}
</li>{{end}}
</ul>
{{else}}
<p class="muted">No runs yet — send today's digest or start a custom run.</p>
{{end}}
{{end}}
`
