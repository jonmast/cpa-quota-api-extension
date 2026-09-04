package main

import "net/http"

const panelResourcePath = "/panel"

// panelDocument is the embedded management panel: a single self-contained page
// (no external JS/CSS/fonts) served as the plugin's management resource behind
// the host's existing management auth, mirroring the CPA Quota API Extension's
// panel pattern. Landing view is the clickable session list (most recent
// activity first); clicking a session opens its bar chart — one bar per
// request in order, bar height = context tokens (input + cache_read +
// cache_creation), red = miss, neutral = session start, green = hit — with
// per-bar detail. Refresh-on-load; no live push.
const panelDocument = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>Session Cache</title>
<style>
:root{--bg:#f4f1e9;--surface:#fffdf7;--ink:#20201d;--muted:#68665f;--line:#d8d2c4;--accent:#164e63;--ok:#26734d;--bad:#a9362a;--neutral:#8a8578;--shadow:0 18px 60px rgba(49,43,32,.12);font-family:"Iowan Old Style","Palatino Linotype",Palatino,serif;color:var(--ink);background:var(--bg)}
@media(prefers-color-scheme:dark){:root{--bg:#171815;--surface:#22231f;--ink:#eeeadd;--muted:#aaa79e;--line:#41423b;--accent:#6fc0d2;--ok:#76c89a;--bad:#f28d7c;--neutral:#77746a;--shadow:0 18px 60px rgba(0,0,0,.28)}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);min-height:100vh}button{font:inherit}
.shell{max-width:1080px;margin:auto;padding:28px}
.mast{margin-bottom:20px}.eyebrow{font:700 11px/1.2 ui-monospace,monospace;letter-spacing:.16em;text-transform:uppercase;color:var(--accent)}
h1{font-size:clamp(26px,4.5vw,44px);line-height:.95;margin:8px 0 0;letter-spacing:-.03em}
.card{background:var(--surface);border:1px solid var(--line);border-radius:16px;padding:20px;box-shadow:var(--shadow);margin-bottom:16px}
.card h2{margin:0 0 12px}.hint{color:var(--muted);font-size:13px;line-height:1.5}
.status{min-height:1.4em;color:var(--muted);font-size:13px;margin:8px 0}.status.error{color:var(--bad)}
.auth-block{display:none}.auth-block.show{display:block}
.view{display:none}.view.active{display:block}
table{border-collapse:collapse;width:100%}th,td{text-align:left;border-bottom:1px solid var(--line);padding:10px 8px;font-size:14px}
th{font:700 11px/1.2 ui-monospace,monospace;letter-spacing:.1em;text-transform:uppercase;color:var(--muted)}
tbody tr{cursor:pointer}tbody tr:hover{background:color-mix(in srgb,var(--accent),transparent 92%)}
tbody tr:focus-visible{outline:3px solid color-mix(in srgb,var(--accent),transparent 55%);outline-offset:-2px}
code{font-family:ui-monospace,SFMono-Regular,Consolas,monospace;font-size:.92em;color:var(--accent)}
.button{border:1px solid var(--line);background:var(--surface);color:var(--ink);padding:8px 14px;border-radius:999px;cursor:pointer;font-weight:700}
.button:focus-visible{outline:3px solid color-mix(in srgb,var(--accent),transparent 55%);outline-offset:2px}
.detail-head{display:flex;align-items:center;gap:14px;flex-wrap:wrap;margin-bottom:12px}
.legend{display:flex;gap:16px;flex-wrap:wrap;font-size:13px;color:var(--muted);margin:0 0 10px}
.legend span{display:inline-flex;align-items:center;gap:6px}.swatch{width:12px;height:12px;border-radius:3px;display:inline-block}
.swatch.hit{background:var(--ok)}.swatch.miss{background:var(--bad)}.swatch.neutral{background:var(--neutral)}
.chart{display:flex;align-items:flex-end;gap:2px;height:260px;padding:8px;border:1px solid var(--line);border-radius:12px;background:var(--bg);overflow-x:auto}
.bar{flex:1 0 5px;max-width:36px;min-height:3px;border:0;padding:0;cursor:pointer;border-radius:3px 3px 0 0;background:var(--ok)}
.bar.miss{background:var(--bad)}.bar.neutral{background:var(--neutral)}
.bar.selected{outline:3px solid color-mix(in srgb,var(--accent),transparent 40%);outline-offset:1px}
dl.breakdown{display:grid;grid-template-columns:auto 1fr;gap:6px 18px;margin:0;font-size:14px}
dl.breakdown dt{color:var(--muted)}dl.breakdown dd{margin:0;font-family:ui-monospace,SFMono-Regular,Consolas,monospace}
@media(max-width:700px){.shell{padding:16px}.chart{height:200px}}
</style>
</head>
<body>
<main class="shell">
<header class="mast"><div class="eyebrow">CLIProxyAPI · Native plugin</div><h1>Session Cache</h1></header>
<div id="auth-block" class="card auth-block" role="alert"><h2>Management session unavailable</h2><p class="hint">This trusted plugin panel needs the same-origin CPAMGMT session. Sign in through <code>management.html</code>, enable remembering the management key, then reload this panel.</p></div>
<div id="status" class="status" aria-live="polite"></div>
<section id="sessions-view" class="view active" aria-label="Recent sessions">
<div class="card"><h2>Recent sessions</h2><p class="hint">Most recent activity first. Click a session to open its per-request cache chart. Data refreshes on page load.</p>
<table><thead><tr><th>Session</th><th>Requests</th><th>Last model</th><th>Last seen</th></tr></thead><tbody id="session-rows"></tbody></table>
<p id="sessions-empty" class="hint" hidden>No sessions captured yet.</p></div>
</section>
<section id="detail-view" class="view" aria-label="Session detail">
<div class="card">
<div class="detail-head"><button class="button" type="button" id="back-button">&#8592; Sessions</button><h2 id="detail-title" style="margin:0"></h2></div>
<p class="legend"><span><span class="swatch hit"></span>cache hit</span><span><span class="swatch miss"></span>cache miss</span><span><span class="swatch neutral"></span>session start</span></p>
<div id="chart" class="chart" role="list" aria-label="Requests in order; bar height is context tokens"></div>
<p class="hint">One bar per request in order. Height = context tokens (input + cache read + cache creation). A sudden drop in height is compaction; red is a cache miss.</p>
</div>
<div class="card"><h2>Request detail</h2><p id="bar-hint" class="hint">Click a bar to inspect its token breakdown.</p><dl id="bar-detail" class="breakdown"></dl></div>
</section>
</main>
<script>
(()=>{"use strict";
const AUTH_KEY="cli-proxy-auth",PREFIX="enc::v1::",SALT="cli-proxy-api-webui::secure-storage";
const LIST_ROUTE="` + sessionsRoute + `",DETAIL_ROUTE="` + sessionDetailRoute + `";
let auth=null;
function decode(raw){if(!raw||!raw.startsWith(PREFIX))return raw;const bytes=Uint8Array.from(atob(raw.slice(PREFIX.length)),c=>c.charCodeAt(0));const key=new TextEncoder().encode(SALT+"|"+location.host+"|"+navigator.userAgent);for(let i=0;i<bytes.length;i++)bytes[i]^=key[i%key.length];return new TextDecoder().decode(bytes)}
function loadAuth(){const raw=localStorage.getItem(AUTH_KEY);if(!raw)throw new Error("No remembered CPAMGMT session");const parsed=JSON.parse(decode(raw)),state=parsed.state||parsed,key=state.managementKey;if(!key)throw new Error("Management key is not remembered");if(state.apiBase){const origin=new URL(state.apiBase,location.href).origin;if(origin!==location.origin)throw new Error("CPAMGMT API is on another origin")}return{key,base:new URL("../../../management/",location.href)}}
async function request(path){if(!auth)throw new Error("Management session unavailable");const controller=new AbortController(),timer=setTimeout(()=>controller.abort(),30000);try{const url=new URL(path.replace(/^\/+/,""),auth.base),response=await fetch(url,{credentials:"same-origin",cache:"no-store",signal:controller.signal,headers:{Accept:"application/json",Authorization:"Bearer "+auth.key}}),text=await response.text();let data=null;try{data=text?JSON.parse(text):null}catch{}if(!response.ok){const message=data&&data.error?data.error.message:("HTTP "+response.status);throw new Error(message)}return data}finally{clearTimeout(timer)}}
function status(message,kind){const el=document.getElementById("status");el.textContent=message||"";el.className="status"+(kind?" "+kind:"")}
function show(view){for(const id of["sessions-view","detail-view"])document.getElementById(id).classList.toggle("active",id===view)}
function fmt(value){return Number(value||0).toLocaleString()}
function fmtTime(iso){const parsed=new Date(iso);return isNaN(parsed)?String(iso):parsed.toLocaleString()}
function shortID(id){return id.length>18?id.slice(0,10)+"\u2026"+id.slice(-4):id}
function clear(node){while(node.firstChild)node.removeChild(node.firstChild)}
async function loadSessions(){status("Loading sessions\u2026");const body=document.getElementById("session-rows");clear(body);try{const payload=await request(LIST_ROUTE),sessions=(payload&&payload.sessions)||[];document.getElementById("sessions-empty").hidden=sessions.length>0;for(const session of sessions){const row=document.createElement("tr");row.tabIndex=0;row.setAttribute("role","button");const id=document.createElement("td"),idCode=document.createElement("code");idCode.textContent=shortID(session.session_id);idCode.title=session.session_id;id.append(idCode);const count=document.createElement("td");count.textContent=fmt(session.request_count);const model=document.createElement("td");model.textContent=session.last_model||"\u2014";const seen=document.createElement("td");seen.textContent=fmtTime(session.last_seen);row.append(id,count,model,seen);const open=()=>openSession(session.session_id);row.addEventListener("click",open);row.addEventListener("keydown",event=>{if(event.key==="Enter"||event.key===" "){event.preventDefault();open()}});body.append(row)}status("")}catch(err){status(err.message,"error")}}
function detailRow(list,label,value){const term=document.createElement("dt");term.textContent=label;const def=document.createElement("dd");def.textContent=value;list.append(term,def)}
function selectBar(bar,req,index){for(const other of document.querySelectorAll(".bar.selected"))other.classList.remove("selected");bar.classList.add("selected");document.getElementById("bar-hint").hidden=true;const list=document.getElementById("bar-detail");clear(list);detailRow(list,"Request","#"+(index+1)+" \u00b7 "+(req.request_id||"\u2014"));detailRow(list,"Classification",req.classification);detailRow(list,"Model",req.model||"\u2014");detailRow(list,"Timestamp",fmtTime(req.at));detailRow(list,"Context tokens",fmt(req.context_tokens));detailRow(list,"\u2003input",fmt(req.input_tokens));detailRow(list,"\u2003cache read",fmt(req.cache_read_tokens));detailRow(list,"\u2003cache creation",fmt(req.cache_creation_tokens));detailRow(list,"Output tokens",fmt(req.output_tokens));detailRow(list,"Status",String(req.status_code)+(req.stream?" \u00b7 streamed":""))}
function renderChart(payload){const chart=document.getElementById("chart");clear(chart);document.getElementById("bar-hint").hidden=false;clear(document.getElementById("bar-detail"));const requests=payload.requests||[];let max=1;for(const req of requests)if(req.context_tokens>max)max=req.context_tokens;requests.forEach((req,index)=>{const bar=document.createElement("button");bar.type="button";bar.className="bar "+req.classification;bar.setAttribute("role","listitem");bar.style.height=Math.max(2,Math.round(req.context_tokens/max*100))+"%";bar.title="#"+(index+1)+" \u00b7 "+req.classification+" \u00b7 "+fmt(req.context_tokens)+" ctx \u00b7 "+(req.model||"");bar.addEventListener("click",()=>selectBar(bar,req,index));chart.append(bar)})}
async function openSession(sessionID){status("Loading session\u2026");try{const payload=await request(DETAIL_ROUTE+"?session_id="+encodeURIComponent(sessionID));const title=document.getElementById("detail-title");clear(title);const code=document.createElement("code");code.textContent=payload.session_id;title.append(code);renderChart(payload);show("detail-view");status("")}catch(err){status(err.message,"error")}}
document.getElementById("back-button").addEventListener("click",()=>{show("sessions-view");status("")});
try{auth=loadAuth()}catch(err){document.getElementById("auth-block").classList.add("show");status(err.message,"error");return}
loadSessions();
})();
</script>
</body>
</html>
`

func panelResponse() managementResponse {
	return managementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":                 {"text/html; charset=utf-8"},
			"Cache-Control":                {"no-store"},
			"Pragma":                       {"no-cache"},
			"X-Content-Type-Options":       {"nosniff"},
			"Referrer-Policy":              {"no-referrer"},
			"Content-Security-Policy":      {"default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; object-src 'none'; frame-ancestors 'self'"},
			"Permissions-Policy":           {"camera=(), microphone=(), geolocation=()"},
			"Cross-Origin-Resource-Policy": {"same-origin"},
		},
		Body: []byte(panelDocument),
	}
}
