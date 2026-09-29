package server

import _ "embed"

//go:embed assets/daisyui.css
var daisyuiCSS string

//go:embed assets/daisyui-themes.css
var daisyuiThemesCSS string

// panelHTML is the embedded management panel: a single file with no external
// dependencies. DaisyUI v5 CSS is embedded via go:embed for offline use.
// It only talks JSON to /v0/management/* with the management key
// kept in sessionStorage. Layout reflows for phone, tablet and desktop.
var panelHTML = `<!DOCTYPE html>
<html lang="zh-CN" data-theme="light">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<meta name="color-scheme" content="dark light">
<meta http-equiv="Cache-Control" content="no-cache, no-store, must-revalidate">
<meta http-equiv="Pragma" content="no-cache">
<meta http-equiv="Expires" content="0">
<title>discovery2api v1.1.0</title>
<style id="daisyui-css">` + daisyuiCSS + `</style>
<style id="daisyui-themes">` + daisyuiThemesCSS + `</style>
<style>
  :root {
    --bg:#eef1f6; --panel:rgba(255,255,255,.72); --panel-solid:#fff; --panel-2:rgba(255,255,255,.5);
    --border:rgba(0,0,0,.06); --border-2:rgba(0,0,0,.1);
    --text:#1e293b; --dim:#64748b; --dim-2:#94a3b8; --accent:#3b82f6; --accent-2:#2563eb;
    --green:#10b981; --red:#ef4444; --amber:#f59e0b; --code:#f1f5f9;
    --shadow:0 1px 3px rgba(0,0,0,.04),0 4px 16px rgba(0,0,0,.04);
    --shadow-lg:0 4px 24px rgba(0,0,0,.06);
    --pad:16px; --radius:12px; --radius-sm:8px;
    --sidebar-w:200px;
    --btn-primary:linear-gradient(135deg,#3b82f6,#60a5fa);
    --glass:blur(16px) saturate(160%);
    --glow:0 0 0 3px rgba(59,130,246,.08);
  }
  [data-theme="dark"] {
    --bg:#0c0f17; --panel:rgba(20,25,35,.72); --panel-solid:#141923; --panel-2:rgba(30,38,52,.5);
    --border:rgba(255,255,255,.06); --border-2:rgba(255,255,255,.1);
    --text:#e2e8f0; --dim:#94a3b8; --dim-2:#64748b; --accent:#60a5fa; --accent-2:#3b82f6;
    --green:#34d399; --red:#f87171; --amber:#fbbf24; --code:#1e293b;
    --shadow:0 1px 3px rgba(0,0,0,.2),0 4px 16px rgba(0,0,0,.15);
    --shadow-lg:0 4px 24px rgba(0,0,0,.2);
    --btn-primary:linear-gradient(135deg,#3b82f6,#60a5fa);
    --glass:blur(16px) saturate(140%);
    --glow:0 0 0 3px rgba(96,165,250,.12);
  }
  * { box-sizing:border-box; margin:0; padding:0; }
  html, body { background:var(--bg); color:var(--text); }
  body {
    font:13px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif;
    padding:env(safe-area-inset-top) env(safe-area-inset-right) env(safe-area-inset-bottom) env(safe-area-inset-left);
    transition:background .3s,color .3s;
    overflow-x:hidden;
  }
  body::before {
    content:''; position:fixed; inset:0; z-index:-1; pointer-events:none;
    background:
      radial-gradient(800px 400px at 10% -10%, rgba(59,130,246,.06), transparent 60%),
      radial-gradient(600px 300px at 90% 100%, rgba(96,165,250,.04), transparent 60%);
  }
  [data-theme="dark"] body::before {
    background:
      radial-gradient(800px 400px at 10% -10%, rgba(59,130,246,.1), transparent 60%),
      radial-gradient(600px 300px at 90% 100%, rgba(96,165,250,.06), transparent 60%);
  }
  button, input, select, textarea { font:inherit; color:inherit; }

  /* ── Sidebar ── */
  .sidebar {
    position:fixed; top:0; left:0; bottom:0; width:var(--sidebar-w); z-index:30;
    background:var(--panel); backdrop-filter:var(--glass); -webkit-backdrop-filter:var(--glass);
    border-right:1px solid var(--border-2);
    display:flex; flex-direction:column; overflow-y:auto;
    transition:transform .3s ease;
  }
  .sidebar .logo {
    display:flex; align-items:center; gap:8px; padding:14px var(--pad);
    border-bottom:1px solid var(--border); font-weight:600; font-size:14px; white-space:nowrap;
  }
  .sidebar .logo .icon {
    width:24px; height:24px; border-radius:6px; flex:none;
    background:var(--btn-primary); display:flex; align-items:center; justify-content:center;
    color:#fff; font-size:12px; font-weight:700;
  }
  .sidebar nav { padding:4px 0; flex:1; }
  .nav-item {
    display:flex; align-items:center; gap:8px; padding:8px var(--pad);
    cursor:pointer; color:var(--dim); font-size:12.5px; white-space:nowrap;
    border-left:2px solid transparent; transition:all .15s; position:relative;
  }
  .nav-item:hover { color:var(--text); background:var(--panel-2); }
  .nav-item.active {
    color:var(--accent); border-left-color:var(--accent); background:var(--panel-2);
    font-weight:500;
  }
  .nav-item .nav-icon { width:16px; text-align:center; flex:none; font-size:14px; opacity:.8; }
  .nav-item.active .nav-icon { opacity:1; }

  /* ── Main layout ── */
  .main-wrap { margin-left:var(--sidebar-w); transition:margin .3s; min-height:100vh; }
  header {
    position:sticky; top:0; z-index:20;
    display:flex; align-items:center; gap:10px; padding:10px var(--pad);
    background:var(--panel); backdrop-filter:var(--glass); -webkit-backdrop-filter:var(--glass);
    border-bottom:1px solid var(--border-2);
  }
  .brand { display:flex; align-items:center; gap:8px; min-width:0; }
  .brand h1 { font-size:14px; font-weight:600; letter-spacing:.2px; white-space:nowrap; }
  .brand .sub { color:var(--dim-2); font-size:11px; white-space:nowrap; overflow:hidden; text-overflow:ellipsis; max-width:30vw; }
  .dot { width:7px; height:7px; border-radius:50%; background:var(--green); flex:none; animation:pulse 2s ease-in-out infinite; }
  .dot.off { background:var(--red); }
  @keyframes pulse { 0%,100%{opacity:1} 50%{opacity:.5} }
  .sp { flex:1; }
  .tools { display:flex; gap:6px; align-items:center; }
  .btn {
    background:var(--panel-2); color:var(--text); border:1px solid var(--border-2);
    border-radius:var(--radius-sm); padding:5px 10px; cursor:pointer; min-height:30px;
    font-size:12px; transition:all .15s; white-space:nowrap;
  }
  .btn:hover { border-color:var(--accent); background:var(--panel); }
  .btn.primary { background:var(--btn-primary); border-color:transparent; color:#fff; box-shadow:0 1px 4px rgba(59,130,246,.2); }
  .btn.primary:hover { box-shadow:0 2px 8px rgba(59,130,246,.3); }
  .btn.danger { border-color:transparent; background:rgba(239,68,68,.08); color:var(--red); }
  .btn.danger:hover { background:rgba(239,68,68,.12); }
  .btn.sm { padding:4px 8px; min-height:26px; font-size:11.5px; }
  .btn.icon-btn { padding:5px; min-height:30px; min-width:30px; display:flex; align-items:center; justify-content:center; }

  main { padding:14px var(--pad) 40px; }

  /* ── Sections ── */
  .section {
    display:none;
  }
  .section.active { display:block; animation:fadeIn .25s ease-out; }
  @keyframes fadeIn { from{opacity:0; transform:translateY(6px)} to{opacity:1; transform:translateY(0)} }
  section {
    background:var(--panel); backdrop-filter:var(--glass); -webkit-backdrop-filter:var(--glass);
    border:1px solid var(--border-2); border-radius:var(--radius);
    padding:14px; margin-bottom:12px; box-shadow:var(--shadow);
  }
  section h2 {
    font-size:13px; margin-bottom:2px; font-weight:600; display:flex; align-items:center; gap:6px; flex-wrap:wrap;
  }
  .hint { color:var(--dim); font-size:11.5px; margin-bottom:10px; }

  /* ── Cards ── */
  .cards { display:grid; grid-template-columns:repeat(auto-fill,minmax(160px,1fr)); gap:8px; }
  .card {
    background:var(--panel); backdrop-filter:var(--glass); -webkit-backdrop-filter:var(--glass);
    border:1px solid var(--border-2); border-radius:var(--radius); padding:10px 12px;
    min-width:0; position:relative; overflow:hidden; box-shadow:var(--shadow);
    transition:transform .15s, border-color .15s;
  }
  .card:hover { border-color:var(--accent); transform:translateY(-1px); }
  .card .k { color:var(--dim-2); font-size:10.5px; margin-bottom:2px; text-transform:uppercase; letter-spacing:.3px; }
  .card .v { font-size:13px; font-weight:600; overflow-wrap:anywhere; }
  .card .v.mono { font-family:"JetBrains Mono","Cascadia Code",ui-monospace,monospace; font-size:12px; }

  /* ── Pills ── */
  .pill {
    display:inline-flex; align-items:center; padding:1px 7px; border-radius:99px;
    font-size:10.5px; border:1px solid var(--border-2); white-space:nowrap; font-weight:500;
  }
  .pill.ok { color:var(--green); border-color:rgba(16,185,129,.3); background:rgba(16,185,129,.06); }
  .pill.bad { color:var(--red); border-color:rgba(239,68,68,.3); background:rgba(239,68,68,.06); }
  .pill.warn { color:var(--amber); border-color:rgba(245,158,11,.3); background:rgba(245,158,11,.06); }
  .pill.dim { color:var(--dim-2); }

  /* ── Tables ── */
  .scroll { overflow-x:auto; -webkit-overflow-scrolling:touch; margin:0 -4px; padding:0 4px; }
  table { width:100%; border-collapse:collapse; font-size:11.5px; min-width:600px; }
  table.slim { min-width:0; }
  th, td { padding:6px 6px; text-align:left; border-bottom:1px solid var(--border); vertical-align:middle; }
  th { color:var(--dim-2); font-weight:500; white-space:nowrap; font-size:10.5px; text-transform:uppercase; letter-spacing:.3px; }
  td { overflow-wrap:anywhere; }
  tr:last-child td { border-bottom:none; }
  tr:hover td { background:var(--panel-2); }
  .mono, code { font-family:"JetBrains Mono","Cascadia Code","Fira Code",ui-monospace,SFMono-Regular,Menlo,Consolas,monospace; font-size:11px; }
  .mono { background:var(--code); padding:1px 5px; border-radius:4px; }

  /* ── Forms ── */
  form.row, .row { display:flex; gap:6px; flex-wrap:wrap; align-items:center; margin-top:10px; }
  input, select, textarea {
    background:var(--panel-solid); border:1px solid var(--border-2); border-radius:var(--radius-sm);
    padding:6px 9px; outline:none; min-height:32px; width:100%; font-size:12px;
    transition:border-color .15s;
  }
  input:focus, select:focus, textarea:focus { border-color:var(--accent); box-shadow:var(--glow); }
  .grow { flex:1 1 200px; min-width:0; }
  .narrow { flex:0 1 90px; }
  textarea { min-height:80px; resize:vertical; }
  label.chk { display:flex; align-items:center; gap:6px; color:var(--dim); min-height:32px; font-size:12px; }
  .muted { color:var(--dim); }
  .err { color:var(--red); }
  .right { text-align:right; }
  .num { font-variant-numeric:tabular-nums; white-space:nowrap; }
  .actions { display:flex; gap:4px; justify-content:flex-end; flex-wrap:wrap; }
  .subhead { margin:12px 0 6px; font-size:11.5px; color:var(--dim); font-weight:600; text-transform:uppercase; letter-spacing:.3px; }

  /* ── Toast ── */
  #toast {
    position:fixed; left:50%; bottom:max(16px, env(safe-area-inset-bottom));
    transform:translateX(-50%); max-width:min(90vw, 400px);
    background:var(--panel-solid); border:1px solid var(--accent); color:var(--text);
    padding:8px 14px; border-radius:var(--radius-sm); opacity:0; transition:opacity .2s;
    pointer-events:none; z-index:99; text-align:center; box-shadow:var(--shadow-lg); font-size:12px;
  }
  #toast.show { opacity:1; }
  #toast.err { border-color:var(--red); }

  /* ── Login ── */
  #login {
    position:fixed; inset:0; z-index:50; display:flex; align-items:center; justify-content:center;
    padding:20px; background:var(--bg);
  }
  #login::before {
    content:''; position:absolute; inset:0;
    background:
      radial-gradient(600px 300px at 30% 0%, rgba(59,130,246,.1), transparent 60%),
      radial-gradient(500px 250px at 70% 100%, rgba(96,165,250,.06), transparent 60%);
    pointer-events:none;
  }
  #login .box {
    position:relative; width:min(360px, 100%);
    background:var(--panel); backdrop-filter:var(--glass); -webkit-backdrop-filter:var(--glass);
    border:1px solid var(--border-2); border-radius:16px; padding:24px; box-shadow:var(--shadow-lg);
  }
  #login h2 { margin-bottom:4px; font-size:16px; font-weight:600; }
  #login p { color:var(--dim); font-size:11.5px; margin-bottom:14px; }
  #login input { margin-bottom:10px; }
  details { margin-top:10px; }
  summary { cursor:pointer; color:var(--dim); font-size:12px; }

  /* ── Theme toggle ── */
  .theme-toggle { font-size:14px; line-height:1; }

  /* ── Mobile ── */
  .sidebar-toggle { display:none; }
  .sidebar-overlay { display:none; position:fixed; inset:0; z-index:29; background:rgba(0,0,0,.2); }

  @media (max-width: 900px) {
    .sidebar { transform:translateX(-100%); }
    .sidebar.open { transform:translateX(0); }
    .main-wrap { margin-left:0; }
    .sidebar-toggle { display:flex; }
    .sidebar-overlay.show { display:block; }
  }
  @media (max-width: 600px) {
    :root { --pad:12px; }
    .brand .sub { display:none; }
    .cards { grid-template-columns:1fr 1fr; }
    table { min-width:400px; }
  }
  .kv { display:grid; gap:6px; }
  .kv .item {
    display:grid; grid-template-columns:80px 1fr; gap:6px; align-items:start;
    padding:8px 0; border-bottom:1px solid var(--border); font-size:12px;
  }
  .kv .item:last-child { border-bottom:none; }
  .kv .item b { color:var(--dim-2); font-weight:500; }

  /* ── Hero banner (two independent panels) ── */
  .hero-head { display:flex; align-items:center; gap:14px; flex-wrap:wrap; justify-content:space-between; padding:14px 16px; margin-bottom:10px; background:var(--panel); backdrop-filter:var(--glass); -webkit-backdrop-filter:var(--glass); border:1px solid var(--border-2); border-radius:var(--radius); box-shadow:var(--shadow); }
  .hero-left { display:flex; flex-direction:column; gap:8px; min-width:0; flex:1 1 200px; }
  .hero-title { font-size:22px; font-weight:700; letter-spacing:.3px; line-height:1.35; }
  .hero-sub { color:var(--dim); font-size:13px; display:inline-flex; align-items:center; gap:6px; line-height:1.5; }
  .hero-sub::before { content:''; width:7px; height:7px; border-radius:50%; background:var(--accent); flex:none; opacity:.5; }
  .hero-actions { display:flex; gap:8px; flex-wrap:wrap; align-items:center; }
  .hero-stats { display:flex; justify-content:space-between; flex-wrap:wrap; gap:18px; padding:14px 16px; margin-bottom:10px; background:var(--panel); backdrop-filter:var(--glass); -webkit-backdrop-filter:var(--glass); border:1px solid var(--border-2); border-radius:var(--radius); box-shadow:var(--shadow); }
  @media (max-width: 600px) { .hero-stats { gap:16px; } }
  .hero-stat { display:flex; flex-direction:column; gap:6px; }
  .hero-stat .hs-k { color:var(--dim-2); font-size:10px; text-transform:uppercase; letter-spacing:.5px; }
  .hero-stat .hs-v { font-size:18px; font-weight:700; font-variant-numeric:tabular-nums; line-height:1.2; }
  .hero-stat .hs-v.ok { color:var(--green); }
  .hero-stat .hs-v.bad { color:var(--red); }
  .hero-stat .hs-v.warn { color:var(--amber); }

  /* ── Section stat chips ── */
  .sec-stats { display:flex; gap:6px; flex-wrap:wrap; margin-bottom:10px; }
  .chip {
    display:inline-flex; align-items:center; gap:4px; padding:3px 9px;
    border-radius:99px; font-size:11px; font-weight:500;
    background:var(--panel-2); border:1px solid var(--border-2); white-space:nowrap;
  }
  .chip.ok { color:var(--green); border-color:rgba(16,185,129,.25); background:rgba(16,185,129,.06); }
  .chip.bad { color:var(--red); border-color:rgba(239,68,68,.25); background:rgba(239,68,68,.06); }
  .chip.warn { color:var(--amber); border-color:rgba(245,158,11,.25); background:rgba(245,158,11,.06); }
  .chip.dim { color:var(--dim-2); }
  .chip .chip-dot { width:5px; height:5px; border-radius:50%; background:currentColor; flex:none; }
  .chip .chip-num { font-variant-numeric:tabular-nums; font-weight:700; }

  /* ── Overview bottom grid ── */
  .ov-grid { display:grid; grid-template-columns:1fr 1fr; gap:10px; margin-top:10px; }
  @media (max-width: 768px) { .ov-grid { grid-template-columns:1fr; } }
  .ov-panel {
    background:var(--panel); backdrop-filter:var(--glass); -webkit-backdrop-filter:var(--glass);
    border:1px solid var(--border-2); border-radius:var(--radius); padding:12px; box-shadow:var(--shadow);
  }
  .panel-title { font-size:11px; font-weight:600; color:var(--dim-2); text-transform:uppercase; letter-spacing:.4px; margin-bottom:8px; }
  .activity-list { display:flex; flex-direction:column; gap:2px; max-height:220px; overflow-y:auto; }
  .activity-item { display:flex; gap:8px; align-items:center; font-size:11px; padding:4px 0; border-bottom:1px solid var(--border); }
  .activity-item:last-child { border-bottom:none; }
  .activity-item .a-time { color:var(--dim-2); font-variant-numeric:tabular-nums; min-width:48px; font-size:10.5px; }
  .activity-item .a-path { font-family:"JetBrains Mono",ui-monospace,monospace; color:var(--text); flex:1; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; font-size:10.5px; }
  .activity-item .a-status { font-variant-numeric:tabular-nums; font-weight:700; min-width:24px; text-align:right; font-size:10.5px; }
  .activity-item .a-status.s2xx { color:var(--green); }
  .activity-item .a-status.s4xx { color:var(--amber); }
  .activity-item .a-status.s5xx { color:var(--red); }
  .quick-list { display:flex; flex-direction:column; gap:5px; }
  .quick-btn {
    display:flex; align-items:center; gap:8px; padding:7px 10px; border-radius:var(--radius-sm);
    background:var(--panel-2); border:1px solid var(--border-2); cursor:pointer; font-size:12px;
    transition:all .15s; color:var(--text);
  }
  .quick-btn:hover { border-color:var(--accent); background:var(--panel); transform:translateX(2px); }
  .quick-btn .qb-icon { font-size:13px; flex:none; }

  /* ── Sidebar footer ── */
  .sidebar-footer {
    padding:10px var(--pad); border-top:1px solid var(--border);
    font-size:10.5px; color:var(--dim-2); display:flex; flex-direction:column; gap:5px; flex:none;
  }
  .sf-row { display:flex; align-items:center; gap:6px; }
  .sf-dot { width:6px; height:6px; border-radius:50%; flex:none; }
  .sf-row .sf-num { font-variant-numeric:tabular-nums; font-weight:600; color:var(--dim); }

  /* ── Stat grid (4 col) ── */
  .stat-grid { display:grid; grid-template-columns:repeat(4,1fr); gap:8px; margin-bottom:10px; }
  @media (max-width: 600px) { .stat-grid { grid-template-columns:repeat(2,1fr); } }
  .stat-cell {
    background:var(--panel); backdrop-filter:var(--glass); -webkit-backdrop-filter:var(--glass);
    border:1px solid var(--border-2); border-radius:var(--radius); padding:10px 12px; box-shadow:var(--shadow);
    display:flex; flex-direction:column; gap:2px; position:relative; overflow:hidden;
  }
  .stat-cell .sc-icon { position:absolute; top:8px; right:8px; opacity:.15; }
  .stat-cell .sc-k { color:var(--dim-2); font-size:10px; text-transform:uppercase; letter-spacing:.3px; }
  .stat-cell .sc-v { font-size:18px; font-weight:700; font-variant-numeric:tabular-nums; }
  .stat-cell .sc-sub { color:var(--dim-2); font-size:10px; font-variant-numeric:tabular-nums; }
  .stat-cell .sc-v.ok { color:var(--green); }
  .stat-cell .sc-v.bad { color:var(--red); }
  .stat-cell .sc-v.warn { color:var(--amber); }

  /* ── Account quota bar ── */
  .account-quota { display:flex; flex-direction:column; gap:10px; padding:4px 0; }
  .aq-bar-wrap { position:relative; height:28px; background:var(--bg-2); border-radius:var(--radius); overflow:hidden; border:1px solid var(--border-2); }
  .aq-bar-fill { position:absolute; left:0; top:0; bottom:0; background:linear-gradient(90deg,var(--blue),var(--accent)); border-radius:var(--radius); transition:width .4s ease; }
  .aq-bar-label { position:absolute; right:10px; top:0; bottom:0; display:flex; align-items:center; font-size:12px; font-weight:700; color:var(--fg); font-variant-numeric:tabular-nums; z-index:1; }
  .aq-stats { display:grid; grid-template-columns:repeat(4,1fr); gap:8px; }
  @media (max-width:600px){ .aq-stats{grid-template-columns:repeat(2,1fr);} }
  .aq-stat { background:var(--panel); border:1px solid var(--border-2); border-radius:var(--radius); padding:8px 10px; display:flex; flex-direction:column; gap:2px; }
  .aq-k { color:var(--dim-2); font-size:10px; text-transform:uppercase; letter-spacing:.3px; }
  .aq-v { font-size:16px; font-weight:700; font-variant-numeric:tabular-nums; }
  .aq-v.ok { color:var(--green); }
  .aq-formula { color:var(--dim-2); font-size:11px; line-height:1.5; padding:4px 0; }

  /* ── Bar chart (request distribution) ── */
  .bar-chart { display:flex; flex-direction:column; gap:6px; margin:8px 0; }
  .bar-row { display:flex; align-items:center; gap:8px; font-size:11px; }
  .bar-row .bar-label { font-family:"JetBrains Mono",ui-monospace,monospace; color:var(--dim); min-width:120px; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; font-size:10.5px; }
  .bar-row .bar-track { flex:1; height:16px; background:var(--panel-2); border-radius:4px; overflow:hidden; position:relative; }
  .bar-row .bar-fill { height:100%; border-radius:4px; background:var(--btn-primary); transition:width .4s ease; }
  .bar-row .bar-fill.err { background:linear-gradient(135deg,#ef4444,#f87171); }
  .bar-row .bar-num { font-variant-numeric:tabular-nums; font-weight:600; min-width:36px; text-align:right; font-size:10.5px; }

  /* ── Mini token usage (in overview) ── */
  .usage-mini { display:grid; grid-template-columns:1fr 1fr; gap:10px; }
  @media (max-width: 600px) { .usage-mini { grid-template-columns:1fr; } }
  .usage-mini table { min-width:0; font-size:10.5px; }
  .usage-mini th, .usage-mini td { padding:4px 4px; }
  .usage-mini .scroll { max-height:200px; overflow-y:auto; overflow-x:auto; -webkit-overflow-scrolling:touch; }
  .usage-mini .chart-zone { margin-bottom:8px; }

  /* ── Treemap (token usage) ── */
  .treemap-zone { position:relative; width:100%; height:170px; margin-bottom:8px; overflow:hidden; border-radius:5px; background:var(--panel-2); }
  .tm-cell { position:absolute; display:flex; flex-direction:column; justify-content:center; align-items:center; border:1px solid var(--bg); box-sizing:border-box; overflow:hidden; transition:opacity .2s; }
  .tm-cell:hover { opacity:0.8; }
  .tm-label { font-size:9px; color:#fff; text-align:center; padding:0 3px; word-break:break-all; line-height:1.15; max-height:2.3em; overflow:hidden; opacity:0.92; }
  .tm-num { font-size:13px; color:#fff; font-weight:700; margin-top:2px; }

  /* ── Donut chart ── */
  .donut-wrap { display:flex; align-items:center; gap:12px; }
  .donut-wrap svg { flex:none; }
  .donut-legend { display:flex; flex-direction:column; gap:4px; font-size:10.5px; }
  .donut-legend .lg-item { display:flex; align-items:center; gap:5px; }
  .donut-legend .lg-dot { width:8px; height:8px; border-radius:2px; flex:none; }

  /* ── Form group (settings) ── */
  .form-group { margin-bottom:16px; }
  .form-group .fg-title { font-size:11px; font-weight:600; color:var(--dim-2); text-transform:uppercase; letter-spacing:.4px; margin-bottom:8px; padding-bottom:6px; border-bottom:1px solid var(--border); }
  .form-group .fg-body { display:grid; grid-template-columns:1fr 1fr; gap:10px; }
  @media (max-width: 600px) { .form-group .fg-body { grid-template-columns:1fr; } }
  .fg-item { display:flex; flex-direction:column; gap:4px; }
  .fg-item label { font-size:11px; color:var(--dim); font-weight:500; }
  .fg-item input, .fg-item select { min-height:34px; }
  .fg-item .fg-hint { font-size:10px; color:var(--dim-2); }

  /* ── Date picker ── */
  .date-bar { display:flex; align-items:center; gap:8px; margin-bottom:10px; }
  .date-bar label { font-size:11px; color:var(--dim-2); text-transform:uppercase; letter-spacing:.3px; }
  .date-bar input[type="date"] { width:auto; min-height:30px; font-size:11.5px; }

  /* ── Batch ops bar ── */
  .batch-bar { display:flex; gap:6px; align-items:center; margin-bottom:8px; padding:6px 10px; background:var(--panel-2); border-radius:var(--radius-sm); border:1px solid var(--border-2); }
  .batch-bar .bb-count { font-size:11px; color:var(--dim); margin-right:auto; }

  /* ── Copy button ── */
  .copy-btn { cursor:pointer; opacity:.6; transition:opacity .15s; }
  .copy-btn:hover { opacity:1; }

  /* ── Section header with sub ── */
  .sec-head { display:flex; align-items:center; gap:8px; flex-wrap:wrap; margin-bottom:2px; }
  .sec-head .sec-sub { color:var(--dim-2); font-size:11px; font-weight:400; }
</style>
</head>
<body>

<!-- Login -->
<div id="login">
  <div class="box">
    <h2>discovery2api</h2>
    <p>输入面板密码登录。密码只存在于当前浏览器会话。</p>
    <input id="mgmtKey" type="password" placeholder="面板密码" autocomplete="current-password" onkeydown="if(event.key==='Enter')doLogin()">
    <button class="btn primary" style="width:100%" onclick="doLogin()">登录</button>
    <div id="loginErr" class="err" style="margin-top:8px;font-size:11.5px"></div>
  </div>
</div>

<!-- Sidebar -->
<aside class="sidebar" id="sidebar">
  <div class="logo">
    <span class="icon">A</span>
    <span>discovery2api</span>
  </div>
  <nav>
    <div class="nav-item active" data-target="overview" onclick="navTo('overview')">
      <span class="nav-icon"><svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="3" width="7" height="9" rx="1"/><rect x="14" y="3" width="7" height="5" rx="1"/><rect x="14" y="12" width="7" height="9" rx="1"/><rect x="3" y="16" width="7" height="5" rx="1"/></svg></span> 概览
    </div>
    <div class="nav-item" data-target="upstream-keys" onclick="navTo('upstream-keys')">
      <span class="nav-icon"><svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="8" cy="15" r="4"/><path d="M10.85 12.15L19 4"/><path d="M18 6l2 2"/><path d="M15 4l2 2"/></svg></span> 账号管理
    </div>
    <div class="nav-item" data-target="stats-credits" onclick="navTo('stats-credits')">
      <span class="nav-icon"><svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M3 3v18h18"/><path d="M7 14l4-4 4 4 4-6"/></svg></span> 统计
    </div>
    <div class="nav-item" data-target="downstream-keys" onclick="navTo('downstream-keys')">
      <span class="nav-icon"><svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 2l-2 2m-2 2l-2 2m-2 2l-2 2"/><path d="M3 11l6 6"/><path d="M14 4l6 6-3 3-6-6z"/><path d="M3 21l4-1 9-9-3-3-9 9z"/></svg></span> APIKey
    </div>
    <div class="nav-item" data-target="settings" onclick="navTo('settings')">
      <span class="nav-icon"><svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 00.33 1.82l.06.06a2 2 0 11-2.83 2.83l-.06-.06a1.65 1.65 0 00-1.82-.33 1.65 1.65 0 00-1 1.51V21a2 2 0 11-4 0v-.09A1.65 1.65 0 009 19.4a1.65 1.65 0 00-1.82.33l-.06.06a2 2 0 11-2.83-2.83l.06-.06a1.65 1.65 0 00.33-1.82 1.65 1.65 0 00-1.51-1H3a2 2 0 110-4h.09A1.65 1.65 0 004.6 9a1.65 1.65 0 00-.33-1.82l-.06-.06a2 2 0 112.83-2.83l.06.06a1.65 1.65 0 001.82.33H9a1.65 1.65 0 001-1.51V3a2 2 0 114 0v.09a1.65 1.65 0 001 1.51 1.65 1.65 0 001.82-.33l.06-.06a2 2 0 112.83 2.83l-.06.06a1.65 1.65 0 00-.33 1.82V9a1.65 1.65 0 001.51 1H21a2 2 0 110 4h-.09a1.65 1.65 0 00-1.51 1z"/></svg></span> 系统设置
    </div>
    <div class="nav-item" data-target="proxies" onclick="navTo('proxies')">
      <span class="nav-icon"><svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><path d="M2 12h20"/><path d="M12 2a15.3 15.3 0 014 10 15.3 15.3 0 01-4 10 15.3 15.3 0 01-4-10 15.3 15.3 0 014-10z"/></svg></span> SOCKS5 代理
    </div>
  </nav>
  <div class="sidebar-footer">
    <div class="sf-row"><span class="sf-dot" id="sfDot" style="background:var(--green)"></span><span id="sfStatus">运行中</span></div>
    <div class="sf-row"><span>密钥</span><span class="sf-num" id="sfKeys">0/0</span></div>
    <div class="sf-row"><span>代理</span><span class="sf-num" id="sfProxies">直连</span></div>
    <div class="sf-row" style="font-size:10px;opacity:0.5">v1.1.0</div>
  </div>
</aside>
<div class="sidebar-overlay" id="sidebarOverlay" onclick="toggleSidebar()"></div>

<!-- Main -->
<div class="main-wrap">
<header>
  <button class="btn sm sidebar-toggle" onclick="toggleSidebar()">☰</button>
  <div class="brand">
    <span class="dot" id="healthDot"></span>
    <div>
      <h1>discovery2api</h1>
      <div class="sub mono" id="upstreamLbl"></div>
    </div>
  </div>
  <span class="sp"></span>
  <div class="tools">
    <span class="muted" id="refreshLbl" style="font-size:11px"></span>
    <button class="btn sm icon-btn theme-toggle" onclick="toggleTheme()" title="切换主题">☀</button>
    <button class="btn sm" id="autoBtn" onclick="toggleAuto()">自动</button>
    <button class="btn sm" onclick="refresh()">刷新</button>
    <button class="btn sm" onclick="logout()">退出</button>
  </div>
</header>

<main>
  <!-- Overview -->
  <div class="section active" id="sec-overview">
    <div class="hero-stats" id="heroStats">
      <div class="hero-stat"><span class="hs-k">状态</span><span class="hs-v ok" id="hsStatus">运行中</span></div>
      <div class="hero-stat"><span class="hs-k">总请求</span><span class="hs-v" id="hsReq">0</span></div>
      <div class="hero-stat"><span class="hs-k">错误率</span><span class="hs-v" id="hsErrRate">0%</span></div>
      <div class="hero-stat"><span class="hs-k">上游账号</span><span class="hs-v" id="hsKeys">0 / 0</span></div>
      <div class="hero-stat"><span class="hs-k">代理</span><span class="hs-v" id="hsProxies">直连</span></div>
      <div class="hero-stat"><span class="hs-k">默认模型</span><span class="hs-v" id="hsModel">—</span></div>
    </div>
    <div class="hero-head">
      <div class="hero-left">
        <div class="hero-title">discovery2api 网关</div>
        <div class="hero-sub mono" id="heroUpstream">—</div>
      </div>
      <div class="hero-actions">
        <button class="btn sm" onclick="navTo('upstream-keys')">添加账号</button>
        <button class="btn sm" onclick="navTo('settings')">系统设置</button>
        <button class="btn sm" onclick="navTo('proxies')">代理</button>
      </div>
    </div>
    <div class="date-bar">
      <label>日期</label>
      <input type="date" id="ovDate" onchange="filterOverview()">
      <button class="btn sm" onclick="resetDate()">全部</button>
      <span class="sp"></span>
      <span class="muted" style="font-size:11px" id="ovDateHint">显示全部数据</span>
    </div>
    <div class="stat-grid" id="ovStatGrid"></div>
    <div class="ov-panel" id="ovAccountPanel" style="margin-top:10px;display:none">
      <div class="panel-title">上游账户额度</div>
      <div class="account-quota">
        <div class="aq-bar-wrap">
          <div class="aq-bar-fill" id="aqBarFill"></div>
          <span class="aq-bar-label" id="aqBarLabel">0%</span>
        </div>
        <div class="aq-stats">
          <div class="aq-stat"><span class="aq-k">总额度</span><span class="aq-v" id="aqQuota">—</span></div>
          <div class="aq-stat"><span class="aq-k">已用</span><span class="aq-v" id="aqUsed">—</span></div>
          <div class="aq-stat"><span class="aq-k">剩余</span><span class="aq-v ok" id="aqRemain">—</span></div>
          <div class="aq-stat"><span class="aq-k">消耗</span><span class="aq-v" id="aqPct">0%</span></div>
        </div>
        <div class="aq-formula" id="aqFormula"></div>
      </div>
    </div>
    <div class="ov-panel" style="margin-top:10px">
      <div class="panel-title">Token 用量</div>
      <div class="usage-mini">
        <div>
          <div class="subhead" style="margin-top:0">按密钥</div>
          <div class="treemap-zone" id="usageKeysChart"></div>
          <div class="scroll">
            <table class="slim"><thead><tr><th>编号</th><th>请求</th><th>输入</th><th>输出</th><th>错误</th></tr></thead>
              <tbody id="usageKeysBody"></tbody></table>
          </div>
        </div>
        <div>
          <div class="subhead" style="margin-top:0">按模型</div>
          <div class="treemap-zone" id="usageModelsChart"></div>
          <div class="scroll">
            <table class="slim"><thead><tr><th>模型</th><th>请求</th><th>输入</th><th>输出</th></tr></thead>
              <tbody id="usageModelsBody"></tbody></table>
          </div>
        </div>
      </div>
    </div>
    <div class="ov-grid">
      <div class="ov-panel">
        <div class="panel-title">请求分布</div>
        <div class="bar-chart" id="ovBarChart"></div>
      </div>
      <div class="ov-panel">
        <div class="panel-title">状态码分布</div>
        <div class="donut-wrap" id="ovDonut"></div>
      </div>
    </div>
    <div class="ov-grid" style="margin-top:10px">
      <div class="ov-panel">
        <div class="panel-title">账号调用量 Top 5</div>
        <div class="bar-chart" id="ovKeyChart"></div>
      </div>
      <div class="ov-panel">
        <div class="panel-title">最近活动</div>
        <div class="activity-list" id="activityList"></div>
      </div>
    </div>
    <div class="ov-panel" style="margin-top:10px">
      <div class="panel-title">接口明细</div>
      <div class="scroll">
        <table>
          <thead><tr>
            <th>路径</th><th>请求</th><th>错误</th><th>进行中</th><th>最近状态</th><th>最近时间</th><th>最近错误</th>
          </tr></thead>
          <tbody id="statsBody"></tbody>
        </table>
      </div>
    </div>
  </div>

  <!-- Upstream Keys -->
  <div class="section" id="sec-upstream-keys">
    <section>
      <div class="sec-head"><h2>账号管理</h2><span class="sec-sub">上游 Intern Discovery 密钥</span><span class="pill dim" id="keysPill"></span></div>
      <div class="sec-stats" id="keysChips"></div>
      <p class="hint">用来调用 Intern Discovery 的密钥。列表只显示哈希编号，不显示原文。同一账户下的密钥共享每分钟额度。</p>
      <div class="batch-bar" id="keysBatchBar">
        <label class="chk" style="min-height:auto"><input type="checkbox" id="keysSelectAll" onchange="keysToggleAll()"> 全选</label>
        <span class="bb-count" id="keysSelCount">未选中</span>
        <button class="btn sm" onclick="keysBatchAct('enable')">批量启用</button>
        <button class="btn sm" onclick="keysBatchAct('disable')">批量禁用</button>
        <button class="btn sm danger" onclick="keysBatchAct('del')">批量删除</button>
      </div>
      <div class="scroll">
        <table>
          <thead><tr>
            <th style="width:28px"></th>
            <th>编号</th><th>权重</th><th>代理</th><th>状态</th><th>原因</th><th>恢复</th>
            <th>余量</th><th>墨点</th><th>进行中</th><th>成功/失败</th>
            <th>请求</th><th>输入</th><th>输出</th><th>合计</th><th class="right">操作</th>
          </tr></thead>
          <tbody id="keysBody"></tbody>
        </table>
      </div>
      <form onsubmit="return addKey(event)">
        <div class="subhead" style="margin-top:0">添加上游账号</div>
        <div class="row">
          <input id="newKey" class="grow" placeholder="上游账号密钥" autocomplete="off">
          <input id="newWeight" class="narrow" type="number" value="1" min="1" title="权重" inputmode="numeric">
        </div>
        <div class="row">
          <input id="newProxy" class="grow" placeholder="单独代理 socks5://地址:端口，留空则用共享代理池">
          <button class="btn primary" type="submit">添加</button>
        </div>
      </form>
      <details id="bulkBox">
        <summary>批量导入</summary>
        <p class="hint">每行一条密钥。也支持 email------password----xxx 格式，自动提取  密钥。空行和 # 开头的行会忽略，重复的会去掉。</p>
        <textarea id="bulkText" placeholder="每行一个账号密钥，或 email------password----xxx&#10;# 开头的行会被忽略"></textarea>
        <div class="row">
          <input type="file" id="bulkFile" accept=".txt,text/plain" class="grow">
          <button class="btn sm" type="button" onclick="loadBulkFile()">读取文件</button>
          <button class="btn sm" type="button" onclick="previewBulk()">预览</button>
          <span id="bulkPreview" class="muted" style="font-size:11px"></span>
        </div>
        <div class="row">
          <span class="muted" style="font-size:11.5px">权重</span>
          <input id="bulkWeight" class="narrow" type="number" value="1" min="1" inputmode="numeric">
          <input id="bulkProxy" class="grow" placeholder="统一代理，可留空">
          <button class="btn primary" type="button" onclick="importBulk()">导入</button>
        </div>
      </details>
    </section>
  </div>

  <!-- Stats / Credits (墨点) -->
  <div class="section" id="sec-stats-credits">
    <section>
      <div class="sec-head"><h2>墨点统计</h2><span class="sec-sub">上游账户余额探测</span></div>
      <p class="hint">墨点余额由上游账户共享，所有密钥共享同一账户的墨点。1墨点 ≈ 20,000,000 tokens。检测方式：向上游发送轻量请求探测账户状态。</p>
      <div class="hero-stats" id="creditsHeroStats">
        <div class="hero-stat"><span class="hs-k">可用账号</span><span class="hs-v ok" id="csWithCredits">—</span></div>
        <div class="hero-stat"><span class="hs-k">墨点耗尽</span><span class="hs-v" id="csExhausted" style="color:var(--red)">—</span></div>
        <div class="hero-stat"><span class="hs-k">未检测</span><span class="hs-v" id="csUnchecked" style="color:var(--dim)">—</span></div>
        <div class="hero-stat"><span class="hs-k">总账号</span><span class="hs-v" id="csTotal">—</span></div>
      </div>
      <div class="row" style="margin:10px 0">
        <button class="btn primary" onclick="refreshCredits()">刷新墨点</button>
        <span class="muted" style="font-size:11px" id="creditsRefreshLbl"></span>
      </div>
      <div class="scroll">
        <table>
          <thead><tr>
            <th>编号</th><th>墨点状态</th><th>检测时间</th><th>状态码</th><th>错误信息</th>
          </tr></thead>
          <tbody id="creditsBody"></tbody>
        </table>
      </div>
    </section>
  </div>

  <!-- Downstream Keys -->
  <div class="section" id="sec-downstream-keys">
    <section>
      <div class="sec-head"><h2>APIKey</h2><span class="sec-sub">客户端调用密钥</span></div>
      <p class="hint">客户端访问这个网关时使用。和面板密码无关，删掉后对应客户端会立刻无法调用。账号自动生成，只需填写名称。</p>
      <div class="scroll">
        <table class="slim">
          <thead><tr><th>名称</th><th>账号（已掩码）</th><th class="right">操作</th></tr></thead>
          <tbody id="apiKeysBody"></tbody>
        </table>
      </div>
      <form class="row" onsubmit="return addAPIKey(event)">
        <input id="newAPIKeyName" class="grow" placeholder="APIKey 名称（例如：生产环境）" autocomplete="off">
        <button class="btn primary" type="submit">生成 APIKey</button>
      </form>
    </section>
  </div>

  <!-- Settings -->
  <div class="section" id="sec-settings">
    <section>
      <div class="sec-head"><h2>系统设置</h2><span class="sec-sub">网关参数与运行配置</span></div>
      <div class="stat-grid">
        <div class="stat-cell"><div class="sc-k">上游地址</div><div class="sc-v" id="setBaseUrlV" style="font-size:12px">—</div></div>
        <div class="stat-cell"><div class="sc-k">默认模型</div><div class="sc-v" id="setModelV" style="font-size:12px">—</div></div>
        <div class="stat-cell"><div class="sc-k">代理策略</div><div class="sc-v" id="setPolicyV" style="font-size:12px">—</div></div>
      </div>
      <form onsubmit="return saveSettings(event)">
        <div class="form-group">
          <div class="fg-title">上游连接</div>
          <div class="fg-body">
            <div class="fg-item">
              <label>上游地址</label>
              <input id="setBaseUrl" placeholder="https://discovery-api.intern-ai.org.cn">
              <span class="fg-hint">Intern Discovery API 的基础 URL</span>
            </div>
            <div class="fg-item">
              <label>默认模型</label>
              <input id="setModel" placeholder="例如 auto">
              <span class="fg-hint">客户端未指定模型时使用</span>
            </div>
          </div>
        </div>
        <div class="form-group">
          <div class="fg-title">代理与路由</div>
          <div class="fg-body">
            <div class="fg-item">
              <label>代理策略</label>
              <select id="setPolicy">
                <option value="round-robin">轮询</option>
                <option value="random">随机</option>
                <option value="sticky-key">按密钥固定</option>
              </select>
              <span class="fg-hint">密钥选取策略</span>
            </div>
          </div>
        </div>
        <div class="form-group">
          <div class="fg-title">上游账户额度</div>
          <div class="fg-body">
            <div class="fg-item">
              <label>总额度 (Token Quota)</label>
              <input id="setTokenQuota" type="number" min="0" placeholder="例如 100000000">
              <span class="fg-hint">从 Intern Discovery web console /console/usage 填入</span>
            </div>
            <div class="fg-item">
              <label>已用 (Token Used)</label>
              <input id="setTokenUsed" type="number" min="0" placeholder="例如 814289">
              <span class="fg-hint">从 web console 用量页面填入</span>
            </div>
            <div class="fg-item">
              <label>计费公式</label>
              <input id="setFormula" placeholder="未缓存输入 × 20% + 输出，每次请求向上取整；缓存输入免费">
              <span class="fg-hint">计费规则说明（可选）</span>
            </div>
          </div>
        </div>
        <div class="row">
          <button class="btn primary" type="submit">保存设置</button>
        </div>
      </form>
    </section>
  </div>

  <!-- Proxies -->
  <div class="section" id="sec-proxies">
    <section>
      <div class="sec-head"><h2>SOCKS5 代理</h2><span class="sec-sub">出口代理池</span><span class="pill dim" id="proxiesPill"></span></div>
      <div class="sec-stats" id="proxiesChips"></div>
      <div class="scroll">
        <table class="slim">
          <thead><tr><th>地址（已掩码）</th><th>权重</th><th>状态</th><th>失败</th><th>成功</th><th>恢复</th></tr></thead>
          <tbody id="proxiesBody"></tbody>
        </table>
      </div>
      <textarea id="proxyText" rows="3" placeholder="socks5://用户:密码@1.2.3.4:1080"></textarea>
      <div class="row">
        <button class="btn primary" type="button" onclick="saveProxies()">保存代理池</button>
        <span id="proxyHint" class="muted" style="font-size:11px"></span>
      </div>
    </section>

    <section>
      <div class="sec-head"><h2>代理统计</h2><span class="sec-sub">可视化</span></div>
      <div class="ov-grid">
        <div class="ov-panel">
          <div class="panel-title">代理健康度</div>
          <div class="bar-chart" id="proxyChart"></div>
        </div>
        <div class="ov-panel">
          <div class="panel-title">成功 / 失败</div>
          <div class="donut-wrap" id="proxyDonut"></div>
        </div>
      </div>
    </section>
  </div>

</main>
</div>

<div id="toast"></div>

<script>
var KEY = sessionStorage.getItem('discovery2api_mgmt') || '';
var AUTO = true, TIMER = null;
var currentSection = 'overview';

/* ── Theme ── */
function initTheme() {
  var t = localStorage.getItem('discovery2api_theme') || 'light';
  setTheme(t);
}
function setTheme(t) {
  document.documentElement.setAttribute('data-theme', t);
  var btn = document.querySelector('.theme-toggle');
  if (btn) btn.textContent = t === 'light' ? '☀' : '🌙';
  localStorage.setItem('discovery2api_theme', t);
}
function toggleTheme() {
  var cur = document.documentElement.getAttribute('data-theme') || 'light';
  setTheme(cur === 'light' ? 'dark' : 'light');
}
initTheme();

/* ── Sidebar nav: show only the target section ── */
function navTo(target) {
  currentSection = target;
  document.querySelectorAll('.nav-item').forEach(function(n){
    n.classList.toggle('active', n.dataset.target === target);
  });
  document.querySelectorAll('.section').forEach(function(s){
    s.classList.toggle('active', s.id === 'sec-' + target);
  });
  if (window.innerWidth <= 900) closeSidebar();
  window.scrollTo(0, 0);
}
function toggleSidebar() {
  var sb = document.getElementById('sidebar');
  var ov = document.getElementById('sidebarOverlay');
  sb.classList.toggle('open');
  ov.classList.toggle('show', sb.classList.contains('open'));
}
function closeSidebar() {
  document.getElementById('sidebar').classList.remove('open');
  document.getElementById('sidebarOverlay').classList.remove('show');
}

function doLogin() {
  KEY = document.getElementById('mgmtKey').value.trim();
  fetch('/v0/management/stats', {headers: hdr()})
    .then(function(r){
      if (!r.ok) {
        var msg = '面板密码不正确';
        if (r.status === 403) msg = '不允许远程登录。请在服务器本机打开，或把 allow-remote 设为 true';
        else if (r.status === 404) msg = '管理功能未启用';
        else if (r.status === 429) msg = '尝试次数过多，已锁定 15 分钟';
        document.getElementById('loginErr').textContent = msg;
        throw 0;
      }
      sessionStorage.setItem('discovery2api_mgmt', KEY);
      document.getElementById('login').style.display = 'none';
      start();
    })
    .catch(function(){});
}
function logout() { sessionStorage.removeItem('discovery2api_mgmt'); location.reload(); }
function hdr() { return {'X-Management-Key': KEY, 'Content-Type':'application/json'}; }

function api(path, method, body) {
  var opts = {method: method || 'GET', headers: hdr()};
  if (body) opts.body = JSON.stringify(body);
  return fetch(path, opts).then(function(r){
    return r.text().then(function(text){
      var j = {};
      if (text) { try { j = JSON.parse(text); } catch (e) { j = {}; } }
      if (!r.ok) throw (j && j.error && j.error.message) || ('请求失败 ' + r.status);
      return j;
    });
  });
}

function toast(msg, isErr) {
  var t = document.getElementById('toast');
  t.textContent = msg;
  t.className = 'show' + (isErr ? ' err' : '');
  clearTimeout(toast._t);
  toast._t = setTimeout(function(){ t.className = ''; }, 2400);
}

function esc(s) {
  return String(s == null ? '' : s).replace(/[&<>"]/g, function(c){
    return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c];
  });
}
function attr(s) { return esc(s).replace(/'/g, '&#39;'); }

function fmtTime(t) {
  if (!t || String(t).indexOf('0001-') === 0) return '—';
  var d = new Date(t);
  if (isNaN(d.getTime())) return '—';
  return d.toLocaleString('zh-CN', {hour12:false, month:'2-digit', day:'2-digit', hour:'2-digit', minute:'2-digit', second:'2-digit'});
}
var STATE_TEXT = {
  healthy:'可用', ok:'正常', enabled:'已启用', disabled:'已停用',
  cooldown:'冷却中', manual_off:'已禁用', rpm_reserve:'额度不足'
};
function stateText(state) { return STATE_TEXT[state] || state || '未知'; }
function pill(state, text) {
  var cls = 'dim';
  if (state === 'healthy' || state === 'ok' || state === 'enabled') cls = 'ok';
  else if (state === 'disabled') cls = 'bad';
  else if (state === 'cooldown' || state === 'manual_off' || state === 'rpm_reserve') cls = 'warn';
  return '<span class="pill ' + cls + '">' + esc(text || stateText(state)) + '</span>';
}
function fmtTokens(n) { return String(n || 0).replace(/\B(?=(\d{3})+(?!\d))/g, ','); }
function emptyRow(cols, text) {
  return '<tr><td colspan="' + cols + '" class="muted" style="text-align:center;padding:16px">' + esc(text) + '</td></tr>';
}

/* ── Overview: build once, update in place to avoid flicker ── */

function updateOverview(s, usage, st) {
  var p = s.ports || {};
  var upEl = document.getElementById('upstreamLbl');
  if (upEl && upEl.textContent !== (p.upstream || '')) upEl.textContent = p.upstream || '';
  var lbl = document.getElementById('refreshLbl');
  var txt = AUTO ? '2s 刷新' : '已暂停';
  if (lbl.textContent !== txt) lbl.textContent = txt;

  /* Hero stats */
  var hs = function(id, v) { var e = document.getElementById(id); if (e && e.textContent !== String(v)) e.textContent = v; };
  hs('heroUpstream', p.upstream || '—');
  hs('hsKeys', (s.keys_healthy || 0) + ' / ' + (s.keys_total || 0));
  hs('hsProxies', (s.proxies_total || 0) ? ((s.proxies_total || 0) + ' 个') : '直连');
  hs('hsModel', p.default_model || '—');
  /* Aggregate request stats from endpoints */
  var eps0 = s.endpoints || {};
  var epNames = Object.keys(eps0);
  var totalReq = 0, totalErr = 0, totalInflight = 0;
  epNames.forEach(function(ep) { var c = eps0[ep]; totalReq += c.requests || 0; totalErr += c.errors || 0; totalInflight += c.inflight || 0; });
  var errRate = totalReq ? (totalErr / totalReq * 100).toFixed(1) : '0.0';
  hs('hsReq', totalReq);
  hs('hsErrRate', errRate + '%');
  var hsStatus = document.getElementById('hsStatus');
  if (hsStatus) {
    var ok = (s.keys_healthy || 0) > 0;
    hsStatus.textContent = ok ? '运行中' : '无可用账号';
    hsStatus.className = 'hs-v ' + (ok ? 'ok' : 'bad');
  }

  /* Sidebar footer */
  var sfDot = document.getElementById('sfDot');
  if (sfDot) sfDot.style.background = (s.keys_healthy || 0) > 0 ? 'var(--green)' : 'var(--red)';
  hs('sfStatus', (s.keys_healthy || 0) > 0 ? '运行中' : '无可用账号');
  hs('sfKeys', (s.keys_healthy || 0) + '/' + (s.keys_total || 0));
  hs('sfProxies', (s.proxies_total || 0) ? ((s.proxies_total || 0) + ' 个') : '直连');

  /* Activity list from endpoints */
  var eps = s.endpoints || {};
  var acts = [];
  Object.keys(eps).forEach(function(path){
    var c = eps[path];
    if (c.last_call && String(c.last_call).indexOf('0001-') !== 0) {
      acts.push({path: path, time: c.last_call, status: c.last_status || 0});
    }
  });
  acts.sort(function(a, b) { return new Date(b.time) - new Date(a.time); });
  acts = acts.slice(0, 12);
  var alEl = document.getElementById('activityList');
  if (alEl) {
    var alHtml = acts.length ? acts.map(function(a) {
      var sc = a.status || 0;
      var cls = sc >= 500 ? 's5xx' : (sc >= 400 ? 's4xx' : 's2xx');
      var t = new Date(a.time);
      var tStr = isNaN(t.getTime()) ? '—' : t.toLocaleTimeString('zh-CN', {hour12:false, hour:'2-digit', minute:'2-digit', second:'2-digit'});
      return '<div class="activity-item"><span class="a-time">' + esc(tStr) + '</span><span class="a-path">' + esc(a.path) + '</span><span class="a-status ' + cls + '">' + sc + '</span></div>';
    }).join('') : '<div class="muted" style="padding:8px;font-size:11px">还没有调用记录</div>';
    if (alEl.innerHTML !== alHtml) alEl.innerHTML = alHtml;
  }

  /* Overview mini bar chart */
  var ovEl = document.getElementById('ovBarChart');
  if (ovEl) {
    var eps2 = s.endpoints || {};
    var paths2 = Object.keys(eps2).sort();
    var maxR = 1;
    paths2.forEach(function(p) { var c = eps2[p]; if ((c.requests || 0) > maxR) maxR = c.requests; });
    var barHtml2 = paths2.length ? paths2.slice(0, 6).map(function(p) {
      var c = eps2[p];
      var pct = (c.requests || 0) / maxR * 100;
      return '<div class="bar-row"><span class="bar-label">' + esc(p) + '</span><div class="bar-track"><div class="bar-fill" style="width:' + pct + '%"></div></div><span class="bar-num">' + (c.requests || 0) + '</span></div>';
    }).join('') : '<div class="muted" style="font-size:11px;padding:8px">还没有调用记录</div>';
    if (ovEl.innerHTML !== barHtml2) ovEl.innerHTML = barHtml2;
  }

  /* Token totals for stat grid */
  var totIn = 0, totOut = 0;
  if (usage && usage.keys) {
    Object.keys(usage.keys).forEach(function(id) {
      var k = usage.keys[id];
      totIn += k.prompt_tokens || 0;
      totOut += k.completion_tokens || 0;
    });
  }
  /* Overview stat grid (6 cells) */
  var sgHtml = '';
  sgHtml += '<div class="stat-cell"><div class="sc-k">总请求</div><div class="sc-v">' + totalReq + '</div></div>';
  sgHtml += '<div class="stat-cell"><div class="sc-k">总错误</div><div class="sc-v ' + (totalErr ? 'bad' : 'ok') + '">' + totalErr + '</div></div>';
  sgHtml += '<div class="stat-cell"><div class="sc-k">错误率</div><div class="sc-v ' + (parseFloat(errRate) > 5 ? 'bad' : 'ok') + '">' + errRate + '%</div></div>';
  sgHtml += '<div class="stat-cell"><div class="sc-k">接口数</div><div class="sc-v">' + epNames.length + '</div></div>';
  sgHtml += '<div class="stat-cell"><div class="sc-k">输入Token</div><div class="sc-v">' + fmtTokens(totIn) + '</div></div>';
  sgHtml += '<div class="stat-cell"><div class="sc-k">输出Token</div><div class="sc-v">' + fmtTokens(totOut) + '</div></div>';
  var sgEl = document.getElementById('ovStatGrid');
  if (sgEl && sgEl.innerHTML !== sgHtml) sgEl.innerHTML = sgHtml;

  /* Account quota panel */
  var acct = (st && st.account) || {};
  var acEl = document.getElementById('ovAccountPanel');
  if (acEl) {
    var quota = (acct.token_quota || 0) * (s.keys_total || 1);
    var used = totIn + totOut;
    if (quota > 0) {
      acEl.style.display = '';
      var pct = used / quota * 100;
      var remain = quota - used;
      var fillEl = document.getElementById('aqBarFill');
      if (fillEl) fillEl.style.width = Math.min(pct, 100).toFixed(2) + '%';
      var lblEl = document.getElementById('aqBarLabel');
      if (lblEl) lblEl.textContent = pct.toFixed(2) + '%';
      hs('aqQuota', fmtTokens(quota));
      hs('aqUsed', fmtTokens(used));
      hs('aqRemain', fmtTokens(remain));
      hs('aqPct', pct.toFixed(2) + '%');
      var fEl = document.getElementById('aqFormula');
      if (fEl) fEl.textContent = acct.formula || '';
    } else {
      acEl.style.display = 'none';
    }
  }

  /* Donut — status code distribution */
  var codes = {};
  epNames.forEach(function(ep) { var c = eps0[ep]; var sc = c.last_status || 0; if (sc) codes[sc] = (codes[sc] || 0) + 1; });
  var codeNames = Object.keys(codes).sort();
  var codeTotal = 0;
  codeNames.forEach(function(sc) { codeTotal += codes[sc]; });
  var donutHtml = '';
  if (codeTotal && codeNames.length) {
    var r = 28, cx = 32, cy = 32, circ = 2 * Math.PI * r;
    var offset = 0;
    var colors = {'2':'#10b981','4':'#f59e0b','5':'#ef4444'};
    var segs = '', legend = '';
    codeNames.forEach(function(sc) {
      var pct = codes[sc] / codeTotal;
      var dash = pct * circ;
      var color = sc >= 500 ? colors['5'] : (sc >= 400 ? colors['4'] : colors['2']);
      segs += '<circle cx="' + cx + '" cy="' + cy + '" r="' + r + '" fill="none" stroke="' + color + '" stroke-width="6" stroke-dasharray="' + dash + ' ' + (circ - dash) + '" stroke-dashoffset="' + (-offset) + '" transform="rotate(-90 ' + cx + ' ' + cy + ')"/>';
      offset += dash;
      legend += '<div class="lg-item"><span class="lg-dot" style="background:' + color + '"></span>' + sc + ' × ' + codes[sc] + '</div>';
    });
    donutHtml = '<svg width="64" height="64" viewBox="0 0 64 64"><circle cx="32" cy="32" r="28" fill="none" stroke="var(--panel-2)" stroke-width="6"/>' + segs + '</svg><div class="donut-legend">' + legend + '</div>';
  } else {
    donutHtml = '<div class="muted" style="font-size:11px;padding:8px">还没有状态码记录</div>';
  }
  var dnEl = document.getElementById('ovDonut');
  if (dnEl && dnEl.innerHTML !== donutHtml) dnEl.innerHTML = donutHtml;

  /* Key chart — top 5 accounts by request count */
  var kcEl = document.getElementById('ovKeyChart');
  if (kcEl) {
    var uk = (usage && usage.keys) || {};
    var keyEntries = Object.keys(uk).map(function(id) { return {id: id, req: uk[id].requests || 0}; });
    keyEntries.sort(function(a, b) { return b.req - a.req; });
    keyEntries = keyEntries.slice(0, 5);
    var maxK = 1;
    keyEntries.forEach(function(e) { if (e.req > maxK) maxK = e.req; });
    var kcHtml = keyEntries.length ? keyEntries.map(function(e) {
      var pct = e.req / maxK * 100;
      return '<div class="bar-row"><span class="bar-label">' + esc(e.id) + '</span><div class="bar-track"><div class="bar-fill" style="width:' + pct + '%"></div></div><span class="bar-num">' + e.req + '</span></div>';
    }).join('') : '<div class="muted" style="font-size:11px;padding:8px">还没有调用记录</div>';
    if (kcEl.innerHTML !== kcHtml) kcEl.innerHTML = kcHtml;
  }

  /* Interface detail table */
  var sortedEp = epNames.sort();
  var statsRows = sortedEp.map(function(ep) {
    var c = eps0[ep];
    return '<tr><td class="mono">' + esc(ep) + '</td><td class="num">' + (c.requests || 0) + '</td>'
      + '<td class="num ' + (c.errors ? 'err' : '') + '">' + (c.errors || 0) + '</td><td class="num">' + (c.inflight || 0) + '</td>'
      + '<td class="num">' + (c.last_status || '—') + '</td><td class="muted">' + fmtTime(c.last_call) + '</td>'
      + '<td class="err" title="' + esc(c.last_error || '') + '">' + esc(c.last_error || '—') + '</td></tr>';
  }).join('');
  var sbEl = document.getElementById('statsBody');
  if (sbEl) sbEl.innerHTML = statsRows || emptyRow(7, '还没有调用记录');
}

var ovDateFilter = '';
function filterOverview() {
  ovDateFilter = document.getElementById('ovDate').value || '';
  var hint = document.getElementById('ovDateHint');
  if (hint) hint.textContent = ovDateFilter ? ('显示 ' + ovDateFilter + ' 的数据') : '显示全部数据';
  refresh();
}
function resetDate() {
  ovDateFilter = '';
  var dEl = document.getElementById('ovDate');
  if (dEl) dEl.value = '';
  var hint = document.getElementById('ovDateHint');
  if (hint) hint.textContent = '显示全部数据';
  refresh();
}

var REFRESHING = false, SLOW_TICK = 0, _slowCache = [null, null, null];

function refresh() {
  if (REFRESHING) return;
  REFRESHING = true;
  SLOW_TICK++;
  var doSlow = (SLOW_TICK % 5 === 1);
  var reqs = [
    api('/v0/management/stats'),
    api('/v0/management/keys'),
    doSlow ? api('/v0/management/api-keys') : Promise.resolve(_slowCache[0]),
    api('/v0/management/usage').catch(function(){ return {enabled:false}; }),
    doSlow ? api('/v0/management/proxies').catch(function(){ return {proxies:[]}; }) : Promise.resolve(_slowCache[1]),
    doSlow ? api('/v0/management/settings').catch(function(){ return null; }) : Promise.resolve(_slowCache[2]),
    api('/v0/management/credits').catch(function(){ return {keys:[],summary:{}}; })
  ];
  Promise.all(reqs).then(function(all){
    if (doSlow) _slowCache = [all[2], all[4], all[5]];
    updateOverview(all[0], all[3], all[5]);
    renderKeys(all[0], all[1], all[3], all[5], all[6]);
    renderCredits(all[6]);
    if (doSlow) renderAPIKeys(all[2]);
    if (doSlow) renderProxies(all[4]);
    renderUsage(all[3]);
    if (doSlow) renderSettings(all[5]);
    var dot = document.getElementById('healthDot');
    var cls = 'dot' + (all[0].keys_healthy > 0 ? '' : ' off');
    if (dot.className !== cls) dot.className = cls;
    REFRESHING = false;
  }).catch(function(e){ toast('刷新失败：' + e, true); REFRESHING = false; });
}

function start() {
  refresh();
  if (TIMER) clearInterval(TIMER);
  TIMER = setInterval(function(){ if (AUTO && !document.hidden) refresh(); }, 2000);
}
function toggleAuto() {
  AUTO = !AUTO;
  document.getElementById('autoBtn').textContent = AUTO ? '自动' : '手动';
}
function renderKeys(s, keys, usage, st, credits) {
  var total = s.keys_total || 0, healthy = s.keys_healthy || 0;
  var perKeyQuota = 0;
  if (st && st.account && st.account.token_quota) {
    perKeyQuota = st.account.token_quota;
  }
  var creditsByID = {};
  if (credits && credits.keys) {
    credits.keys.forEach(function(c) { creditsByID[c.id] = c; });
  }
  document.getElementById('keysPill').textContent = healthy + ' / ' + total;
  /* Chips */
  var cooled = 0, disabled = 0;
  (keys.keys || []).forEach(function(k) {
    if (k.state === 'cooldown' || k.state === 'rpm_reserve') cooled++;
    if (k.state === 'disabled' || k.state === 'manual_off') disabled++;
  });
  var chips = '<span class="chip ok"><span class="chip-dot"></span>可用 <span class="chip-num">' + healthy + '</span></span>';
  if (cooled) chips += '<span class="chip warn"><span class="chip-dot"></span>冷却 <span class="chip-num">' + cooled + '</span></span>';
  if (disabled) chips += '<span class="chip bad"><span class="chip-dot"></span>禁用 <span class="chip-num">' + disabled + '</span></span>';
  chips += '<span class="chip dim">共 <span class="chip-num">' + total + '</span></span>';
  var kcEl = document.getElementById('keysChips');
  if (kcEl) kcEl.innerHTML = chips;
  var byID = (usage && usage.keys) || {};
  var rows = (keys.keys || []).map(function(k){
    var until = (k.until && String(k.until).indexOf('0001-') !== 0) ? fmtTime(k.until) : '—';
    var on = k.state === 'disabled' || k.state === 'manual_off';
    var act = on
      ? '<button class="btn sm" onclick="keyAct(\'' + attr(k.id) + '\',\'enable\')">启用</button>'
      : '<button class="btn sm" onclick="keyAct(\'' + attr(k.id) + '\',\'disable\')">禁用</button>';
    act += '<button class="btn sm danger" onclick="keyAct(\'' + attr(k.id) + '\',\'del\')">删除</button>';
    var u = byID[k.id] || {};
    var keyUsed = (u.prompt_tokens || 0) + (u.completion_tokens || 0);
    var proxyLbl = k.proxy ? (k.proxy === 'none' ? '直连' : '已设') : '共享';
    return '<tr><td style="width:28px"><input type="checkbox" class="key-chk" data-id="' + attr(k.id) + '" onchange="keysSelUpdate()"></td>'
      + '<td class="mono">' + esc(k.id) + '</td><td class="num">' + esc(k.weight) + '</td>'
      + '<td class="muted" title="' + attr(k.proxy || '') + '">' + proxyLbl + '</td>'
      + '<td>' + pill(k.state) + '</td><td class="muted">' + esc(k.reason || '—') + '</td><td>' + until + '</td>'
      + '<td class="num">' + (perKeyQuota > 0 ? fmtTokens(Math.max(perKeyQuota - keyUsed, 0)) + ' / ' + fmtTokens(perKeyQuota) : (k.rpm_limit ? (k.rpm_remaining + '/' + k.rpm_limit) : '—')) + '</td>'
      + '<td>' + creditBadge(creditsByID[k.id]) + '</td>'
      + '<td class="num">' + esc(k.inflight) + '</td>'
      + '<td class="num">' + esc(k.success_count) + '/<span class="' + (k.error_count ? 'err' : '') + '">' + esc(k.error_count) + '</span></td>'
      + '<td class="num">' + (u.requests || 0) + '</td>'
      + '<td class="num">' + fmtTokens(u.prompt_tokens) + '</td>'
      + '<td class="num">' + fmtTokens(u.completion_tokens) + '</td>'
      + '<td class="num">' + fmtTokens(keyUsed) + '</td>'
      + '<td class="right"><div class="actions">' + act + '</div></td></tr>';
  }).join('');
  document.getElementById('keysBody').innerHTML = rows || emptyRow(16, '还没有上游账号，在下面添加');
  keysSelUpdate();
}

function keysToggleAll() {
  var master = document.getElementById('keysSelectAll');
  var boxes = document.querySelectorAll('.key-chk');
  for (var i = 0; i < boxes.length; i++) boxes[i].checked = master.checked;
  keysSelUpdate();
}
function keysSelUpdate() {
  var boxes = document.querySelectorAll('.key-chk');
  var n = 0;
  for (var i = 0; i < boxes.length; i++) if (boxes[i].checked) n++;
  var el = document.getElementById('keysSelCount');
  if (el) el.textContent = n ? ('已选 ' + n + ' 个') : '未选中';
  var master = document.getElementById('keysSelectAll');
  if (master) master.checked = boxes.length > 0 && n === boxes.length;
}

function creditBadge(c) {
  if (!c) return '<span class="muted">—</span>';
  if (c.exhausted) return '<span style="color:var(--red);font-weight:600">✗</span>';
  if (c.has_credits) return '<span style="color:var(--green);font-weight:600">✓</span>';
  return '<span class="muted">—</span>';
}

function renderCredits(res) {
  if (!res) return;
  var sm = res.summary || {};
  var el;
  if (el = document.getElementById('csWithCredits')) el.textContent = sm.with_credits || 0;
  if (el = document.getElementById('csExhausted')) el.textContent = sm.exhausted || 0;
  if (el = document.getElementById('csUnchecked')) el.textContent = sm.unchecked || 0;
  if (el = document.getElementById('csTotal')) el.textContent = sm.total || 0;
  var rows = (res.keys || []).map(function(c) {
    var statusLbl;
    if (c.exhausted) statusLbl = '<span style="color:var(--red)">耗尽</span>';
    else if (c.has_credits) statusLbl = '<span style="color:var(--green)">可用</span>';
    else statusLbl = '<span class="muted">未检测</span>';
    var lastCheck = c.last_check ? fmtTime(c.last_check) : '—';
    var errInfo = c.last_error ? esc(c.last_error) : '—';
    return '<tr><td class="mono">' + esc(c.id) + '</td>'
      + '<td>' + statusLbl + '</td>'
      + '<td class="muted">' + lastCheck + '</td>'
      + '<td class="num">' + esc(c.status_code || '—') + '</td>'
      + '<td class="muted">' + errInfo + '</td></tr>';
  }).join('');
  if (el = document.getElementById('creditsBody')) el.innerHTML = rows || emptyRow(5, '还没有墨点数据，点击上方刷新');
}

function refreshCredits() {
  var lbl = document.getElementById('creditsRefreshLbl');
  if (lbl) lbl.textContent = '检测中…';
  api('/v0/management/credits', 'POST', {})
    .then(function(res) {
      renderCredits(res);
      if (lbl) lbl.textContent = '已更新 ' + new Date().toLocaleTimeString();
      toast('墨点检测完成');
    })
    .catch(function(e) {
      if (lbl) lbl.textContent = '';
      toast('墨点检测失败：' + e, true);
    });
}
function keysBatchAct(action) {
  var boxes = document.querySelectorAll('.key-chk:checked');
  var ids = [];
  for (var i = 0; i < boxes.length; i++) ids.push(boxes[i].getAttribute('data-id'));
  if (!ids.length) { toast('请先勾选要操作的密钥', true); return; }
  var label = action === 'del' ? '删除' : (action === 'disable' ? '禁用' : '启用');
  if (action === 'del' && !confirm('确认删除选中的 ' + ids.length + ' 条密钥？')) return;
  api('/v0/management/keys/batch', 'POST', {ids: ids, action: action})
    .then(function(res){
      toast(label + '完成：成功 ' + (res.affected || ids.length) + ' 条');
      var master = document.getElementById('keysSelectAll');
      if (master) master.checked = false;
      refresh();
    })
    .catch(function(e){ toast(label + '失败：' + e, true); });
}

function keyAct(id, action) {
  if (action === 'del') {
    if (!confirm('删除这条上游账号？')) return;
    api('/v0/management/keys/' + encodeURIComponent(id), 'DELETE')
      .then(function(){ toast('已删除'); refresh(); })
      .catch(function(e){ toast('删除失败：' + e, true); });
    return;
  }
  api('/v0/management/keys/' + encodeURIComponent(id) + '/' + action, 'POST')
    .then(function(){ toast(action === 'disable' ? '已禁用' : '已启用'); refresh(); })
    .catch(function(e){ toast('操作失败：' + e, true); });
}

function addKey(e) {
  e.preventDefault();
  var key = document.getElementById('newKey').value.trim();
  if (!key) { toast('请填写上游账号', true); return false; }
  api('/v0/management/keys', 'POST', {
    key: key,
    weight: parseInt(document.getElementById('newWeight').value, 10) || 1,
    proxy: document.getElementById('newProxy').value.trim()
  }).then(function(){
    document.getElementById('newKey').value = '';
    document.getElementById('newProxy').value = '';
    toast('已添加');
    refresh();
  }).catch(function(err){ toast('添加失败：' + err, true); });
  return false;
}

function parseBulkText(text) {
  var seen = {}, out = [];
  (text || '').split(/\r?\n/).forEach(function(line){
    var k = line.trim();
    if (!k || k.charAt(0) === '#') return;
    // 支持 email------password----xxx 格式：优先提取  开头的密钥段
    var idx = k.indexOf('');
    if (idx >= 0) {
      k = k.slice(idx).trim();
    } else if (k.indexOf('----') >= 0) {
      var parts = k.split('----');
      k = parts[parts.length - 1].trim();
    }
    if (!k || seen[k]) return;
    seen[k] = true;
    out.push(k);
  });
  return out;
}
function loadBulkFile() {
  var f = document.getElementById('bulkFile').files[0];
  if (!f) { toast('请先选择文件', true); return; }
  var rd = new FileReader();
  rd.onload = function(){ document.getElementById('bulkText').value = rd.result; previewBulk(); toast('已读取 ' + f.name); };
  rd.onerror = function(){ toast('读取失败', true); };
  rd.readAsText(f, 'UTF-8');
}
function previewBulk() {
  var keys = parseBulkText(document.getElementById('bulkText').value);
  document.getElementById('bulkPreview').textContent = keys.length
    ? ('可导入 ' + keys.length + ' 条')
    : '没有可导入的密钥';
}
function importBulk() {
  var keys = parseBulkText(document.getElementById('bulkText').value);
  if (!keys.length) { toast('没有可导入的密钥', true); return; }
  api('/v0/management/keys/bulk', 'POST', {
    keys: keys,
    weight: parseInt(document.getElementById('bulkWeight').value, 10) || 1,
    proxy: document.getElementById('bulkProxy').value.trim()
  }).then(function(res){
    toast('导入：新增 ' + res.added + '，更新 ' + res.updated + '，跳过 ' + res.skipped);
    document.getElementById('bulkText').value = '';
    document.getElementById('bulkPreview').textContent = '';
    refresh();
  }).catch(function(err){ toast('导入失败：' + err, true); });
}

function renderAPIKeys(res) {
  var rows = (res.keys || []).map(function(k){
    var plain = apiKeyPlain[k.id] || '';
    var display = plain ? esc(plain) : esc(k.key);
    var copyBtn = plain
      ? '<button class="btn sm copy-btn" onclick="copyText(\'' + attr(plain) + '\')" title="复制完整密钥">复制</button>'
      : '<button class="btn sm copy-btn" onclick="copyText(\'' + attr(k.key) + '\')" title="复制">复制</button>';
    return '<tr><td>' + esc(k.name || '—') + '</td><td class="mono">' + display + '</td><td class="right"><div class="actions">'
      + copyBtn + '<button class="btn sm danger" onclick="delAPIKey(\'' + attr(k.id) + '\')">删除</button></div></td></tr>';
  }).join('');
  document.getElementById('apiKeysBody').innerHTML = rows || emptyRow(3, '还没有 APIKey');
}
var apiKeyPlain = {};
function copyText(text) {
  if (navigator.clipboard) {
    navigator.clipboard.writeText(text).then(function(){ toast('已复制'); }).catch(function(){ fallbackCopy(text); });
  } else { fallbackCopy(text); }
}
function fallbackCopy(text) {
  var ta = document.createElement('textarea');
  ta.value = text; ta.style.position = 'fixed'; ta.style.opacity = '0';
  document.body.appendChild(ta); ta.select();
  try { document.execCommand('copy'); toast('已复制'); } catch(e) { toast('复制失败', true); }
  document.body.removeChild(ta);
}
function addAPIKey(e) {
  e.preventDefault();
  var name = document.getElementById('newAPIKeyName').value.trim();
  if (!name) { toast('请填写 APIKey 名称', true); return false; }
  api('/v0/management/api-keys', 'POST', {name: name}).then(function(res){
    if (res && res.id && res.key) apiKeyPlain[res.id] = res.key;
    document.getElementById('newAPIKeyName').value = '';
    toast('已生成，可点击复制按钮复制完整密钥');
    refresh();
  }).catch(function(err){ toast('生成失败：' + err, true); });
  return false;
}
function delAPIKey(id) {
  if (!confirm('删除这条 APIKey？')) return;
  api('/v0/management/api-keys/' + encodeURIComponent(id), 'DELETE')
    .then(function(){ toast('已删除'); refresh(); })
    .catch(function(e){ toast('删除失败：' + e, true); });
}

function renderSettings(st) {
  if (!st) return;
  var model = document.getElementById('setModel');
  var policy = document.getElementById('setPolicy');
  var baseUrl = document.getElementById('setBaseUrl');
  if (document.activeElement !== model) model.value = st.default_model || '';
  if (document.activeElement !== policy && st.proxy_policy) policy.value = st.proxy_policy;
  if (baseUrl && document.activeElement !== baseUrl) baseUrl.value = st.base_url || '';
  var setV = function(id, v) { var e = document.getElementById(id); if (e) e.textContent = v; };
  setV('setBaseUrlV', st.base_url || '—');
  setV('setModelV', st.default_model || '—');
  setV('setPolicyV', ({'round-robin':'轮询','random':'随机','sticky-key':'按密钥固定'})[st.proxy_policy] || st.proxy_policy || '—');
  var acct = st.account || {};
  var tq = document.getElementById('setTokenQuota');
  var tu = document.getElementById('setTokenUsed');
  var sf = document.getElementById('setFormula');
  if (tq && document.activeElement !== tq) tq.value = acct.token_quota || '';
  if (tu && document.activeElement !== tu) tu.value = acct.token_used || '';
  if (sf && document.activeElement !== sf) sf.value = acct.formula || '';
}
function changeRefreshRate() {
  var v = parseInt(document.getElementById('setRefreshRate').value, 10);
  if (TIMER) clearInterval(TIMER);
  if (v > 0) { AUTO = true; TIMER = setInterval(function(){ if (AUTO) refresh(); }, v); }
  else { AUTO = false; }
  var btn = document.getElementById('autoBtn');
  if (btn) btn.textContent = AUTO ? '自动' : '手动';
}
function saveSettings(e) {
  e.preventDefault();
  var model = document.getElementById('setModel').value.trim();
  if (!model) { toast('默认模型不能为空', true); return false; }
  var body = {
    default_model: model,
    proxy_policy: document.getElementById('setPolicy').value
  };
  var baseUrl = document.getElementById('setBaseUrl');
  if (baseUrl && baseUrl.value.trim()) body.base_url = baseUrl.value.trim();
  var tq = document.getElementById('setTokenQuota');
  var tu = document.getElementById('setTokenUsed');
  var sf = document.getElementById('setFormula');
  if (tq && tq.value.trim()) body.token_quota = parseInt(tq.value.trim(), 10);
  if (tu && tu.value.trim()) body.token_used = parseInt(tu.value.trim(), 10);
  if (sf && sf.value.trim()) body.formula = sf.value.trim();
  api('/v0/management/settings', 'PUT', body).then(function(){ toast('已保存'); refresh(); })
    .catch(function(err){ toast('保存失败：' + err, true); });
  return false;
}

function parseProxyText(text) {
  var out = [];
  (text || '').split(/\r?\n/).forEach(function(line){
    var u = line.trim();
    if (!u || u.charAt(0) === '#') return;
    out.push({url: u, weight: 1});
  });
  return out;
}
function saveProxies() {
  var list = parseProxyText(document.getElementById('proxyText').value);
  var msg = list.length ? ('用这 ' + list.length + ' 个节点替换当前代理池？') : '改为直连？';
  if (!confirm(msg)) return;
  api('/v0/management/proxies', 'PUT', {proxies: list}).then(function(res){
    toast((res.count || 0) ? ('已更新，共 ' + res.count + ' 个节点') : '已改为直连');
    document.getElementById('proxyText').value = '';
    refresh();
  }).catch(function(err){ toast('保存失败：' + err, true); });
}
function renderProxies(res) {
  var list = (res && res.proxies) || [];
  document.getElementById('proxiesPill').textContent = list.length ? (list.length + ' 个') : '直连';
  /* Chips */
  var healthy = 0, bad = 0;
  list.forEach(function(px) { if (px.healthy) healthy++; else bad++; });
  var pch = '';
  if (healthy) pch += '<span class="chip ok"><span class="chip-dot"></span>正常 <span class="chip-num">' + healthy + '</span></span>';
  if (bad) pch += '<span class="chip bad"><span class="chip-dot"></span>不可用 <span class="chip-num">' + bad + '</span></span>';
  if (!list.length) pch = '<span class="chip dim">直连</span>';
  var pcEl = document.getElementById('proxiesChips');
  if (pcEl) pcEl.innerHTML = pch;
  var rows = list.map(function(px){
    return '<tr><td class="mono">' + esc(px.url) + '</td><td class="num">' + esc(px.weight) + '</td>'
      + '<td>' + (px.healthy ? pill('ok', '正常') : pill('bad', '不可用')) + '</td>'
      + '<td class="num ' + (px.fail_count ? 'err' : '') + '">' + esc(px.fail_count) + '</td>'
      + '<td class="num">' + esc(px.success_count) + '</td><td>' + fmtTime(px.fail_until) + '</td></tr>';
  }).join('');
  document.getElementById('proxiesBody').innerHTML = rows || emptyRow(6, '没有配置代理，当前直连');
  /* Proxy health bar chart */
  var pcEl2 = document.getElementById('proxyChart');
  if (pcEl2) {
    var pchHtml = list.length ? list.map(function(px, i) {
      var sc = px.success_count || 0, fc = px.fail_count || 0;
      var total = sc + fc || 1;
      var pct = sc / total * 100;
      var cls = px.healthy ? '' : 'err';
      return '<div class="bar-row"><span class="bar-label">节点' + (i+1) + '</span><div class="bar-track"><div class="bar-fill ' + cls + '" style="width:' + pct + '%"></div></div><span class="bar-num">' + sc + '/' + total + '</span></div>';
    }).join('') : '<div class="muted" style="font-size:11px;padding:8px">没有配置代理</div>';
    if (pcEl2.innerHTML !== pchHtml) pcEl2.innerHTML = pchHtml;
  }
  /* Proxy donut — success vs fail */
  var pdEl = document.getElementById('proxyDonut');
  if (pdEl) {
    var tSc = 0, tFc = 0;
    list.forEach(function(px) { tSc += px.success_count || 0; tFc += px.fail_count || 0; });
    var tAll = tSc + tFc;
    var pdHtml = '';
    if (tAll) {
      var r2 = 28, circ2 = 2 * Math.PI * r2;
      var scDash = tSc / tAll * circ2;
      var fcDash = tFc / tAll * circ2;
      pdHtml = '<svg width="64" height="64" viewBox="0 0 64 64">' +
        '<circle cx="32" cy="32" r="28" fill="none" stroke="var(--panel-2)" stroke-width="6"/>' +
        '<circle cx="32" cy="32" r="28" fill="none" stroke="#10b981" stroke-width="6" stroke-dasharray="' + scDash + ' ' + (circ2 - scDash) + '" transform="rotate(-90 32 32)"/>' +
        '<circle cx="32" cy="32" r="28" fill="none" stroke="#ef4444" stroke-width="6" stroke-dasharray="' + fcDash + ' ' + (circ2 - fcDash) + '" stroke-dashoffset="' + (-scDash) + '" transform="rotate(-90 32 32)"/>' +
        '</svg><div class="donut-legend"><div class="lg-item"><span class="lg-dot" style="background:#10b981"></span>成功 ' + tSc + '</div><div class="lg-item"><span class="lg-dot" style="background:#ef4444"></span>失败 ' + tFc + '</div></div>';
    } else {
      pdHtml = '<div class="muted" style="font-size:11px">还没有调用记录</div>';
    }
    if (pdEl.innerHTML !== pdHtml) pdEl.innerHTML = pdHtml;
  }
  var box = document.getElementById('proxyText');
  if (document.activeElement !== box && !box.value) {
    document.getElementById('proxyHint').textContent = list.length
      ? '上面是掩码地址，要修改请重新粘贴完整地址'
      : '当前直连';
  }
}

var TM_COLORS = ['#1a73e8','#10b981','#f59e0b','#ef4444','#8b5cf6','#ec4899','#14b8a6','#f97316','#6366f1','#84cc16','#06b6d4','#a855f7'];

/* 二叉切分 treemap：递归按面积比例切矩形，方向随宽高比切换 */
function treemap(items, x, y, w, h) {
  if (!items.length) return [];
  if (items.length === 1) return [{x:x, y:y, w:w, h:h, label:items[0].label, value:items[0].value}];
  var total = 0; items.forEach(function(i){ total += i.value; });
  if (total <= 0) return [];
  var half = total / 2, acc = 0, split = 0;
  for (var i = 0; i < items.length; i++) { acc += items[i].value; if (acc >= half) { split = i + 1; break; } }
  if (split < 1) split = 1; if (split >= items.length) split = items.length - 1;
  var g1 = items.slice(0, split), g2 = items.slice(split);
  var t1 = 0; g1.forEach(function(i){ t1 += i.value; });
  var frac = t1 / total, rects = [];
  if (w >= h) {
    var w1 = w * frac;
    rects = rects.concat(treemap(g1, x, y, w1, h));
    rects = rects.concat(treemap(g2, x + w1, y, w - w1, h));
  } else {
    var h1 = h * frac;
    rects = rects.concat(treemap(g1, x, y, w, h1));
    rects = rects.concat(treemap(g2, x, y + h1, w, h - h1));
  }
  return rects;
}

function renderTreemap(elId, entries, labelFn) {
  var el = document.getElementById(elId);
  if (!el) return;
  if (!entries.length) { el.innerHTML = '<div class="muted" style="font-size:11px;padding:4px">还没有用量</div>'; return; }
  var top = entries.slice(0, 12);
  var items = top.map(function(e){ return {label: labelFn(e), value: e.req}; });
  items.sort(function(a,b){ return b.value - a.value; });
  var rects = treemap(items, 0, 0, 100, 100);
  var html = rects.map(function(r, i) {
    var color = TM_COLORS[i % TM_COLORS.length];
    var showLabel = r.w > 12 && r.h > 12;
    var inner = showLabel ? '<div class="tm-label">' + esc(r.label) + '</div><div class="tm-num">' + r.value + '</div>' : '';
    return '<div class="tm-cell" style="left:' + r.x + '%;top:' + r.y + '%;width:' + r.w + '%;height:' + r.h + '%;background:' + color + '">' + inner + '</div>';
  }).join('');
  if (el.innerHTML !== html) el.innerHTML = html;
}

function renderUsage(u) {
  if (!u || !u.keys) {
    document.getElementById('usageKeysBody').innerHTML = emptyRow(5, '用量统计未开启');
    document.getElementById('usageModelsBody').innerHTML = emptyRow(4, '—');
    var kc0 = document.getElementById('usageKeysChart'); if (kc0) kc0.innerHTML = '';
    var mc0 = document.getElementById('usageModelsChart'); if (mc0) mc0.innerHTML = '';
    return;
  }
  var keyEntries = Object.keys(u.keys).map(function(id){
    var k = u.keys[id];
    return {id: id, req: k.requests || 0};
  });
  keyEntries.sort(function(a, b) { return b.req - a.req; });
  /* 按密钥树状图 */
  renderTreemap('usageKeysChart', keyEntries, function(e){ return e.id; });
  /* 按密钥表格 */
  var rows = keyEntries.map(function(e) {
    var k = u.keys[e.id];
    return '<tr><td class="mono">' + esc(e.id) + '</td><td class="num">' + k.requests + '</td>'
      + '<td class="num">' + fmtTokens(k.prompt_tokens) + '</td><td class="num">' + fmtTokens(k.completion_tokens) + '</td>'
      + '<td class="num ' + (k.errors ? 'err' : '') + '">' + k.errors + '</td></tr>';
  }).join('');
  document.getElementById('usageKeysBody').innerHTML = rows || emptyRow(5, '还没有用量');
  /* 按模型树状图 */
  var modelEntries = Object.keys(u.models || {}).map(function(m){
    var v = u.models[m];
    return {m: m, req: v.requests || 0};
  });
  modelEntries.sort(function(a, b) { return b.req - a.req; });
  renderTreemap('usageModelsChart', modelEntries, function(e){ return e.m; });
  /* 按模型表格 */
  var models = modelEntries.map(function(e) {
    var v = u.models[e.m];
    return '<tr><td class="mono">' + esc(e.m) + '</td><td class="num">' + v.requests + '</td>'
      + '<td class="num">' + fmtTokens(v.prompt_tokens) + '</td><td class="num">' + fmtTokens(v.completion_tokens) + '</td></tr>';
  }).join('');
  document.getElementById('usageModelsBody').innerHTML = models || emptyRow(4, '还没有用量');
}

if (KEY) {
  document.getElementById('login').style.display = 'none';
  start();
} else {
  document.getElementById('mgmtKey').focus();
}
</script>
</body>
</html>`
