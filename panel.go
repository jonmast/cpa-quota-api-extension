package main

import "net/http"

const panelDocument = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>CPA Quota</title>
<style>
:root{--bg:#f4f1e9;--surface:#fffdf7;--ink:#20201d;--muted:#68665f;--line:#d8d2c4;--accent:#b84e32;--accent2:#164e63;--ok:#26734d;--bad:#a9362a;--shadow:0 18px 60px rgba(49,43,32,.12);font-family:"Iowan Old Style","Palatino Linotype",Palatino,serif;color:var(--ink);background:var(--bg)}
@media(prefers-color-scheme:dark){:root{--bg:#171815;--surface:#22231f;--ink:#eeeadd;--muted:#aaa79e;--line:#41423b;--accent:#e27759;--accent2:#6fc0d2;--ok:#76c89a;--bad:#f28d7c;--shadow:0 18px 60px rgba(0,0,0,.28)}}
*{box-sizing:border-box}body{margin:0;background:radial-gradient(circle at 100% 0,rgba(184,78,50,.12),transparent 30rem),var(--bg);min-height:100vh}button,input,select{font:inherit}.shell{max-width:1240px;margin:auto;padding:28px}.mast{display:grid;grid-template-columns:1fr auto;gap:18px;align-items:end;margin-bottom:22px}.eyebrow{font:700 11px/1.2 ui-monospace,monospace;letter-spacing:.16em;text-transform:uppercase;color:var(--accent)}h1{font-size:clamp(28px,5vw,54px);line-height:.95;margin:8px 0 0;letter-spacing:-.04em}.live{display:flex;align-items:center;gap:8px;color:var(--muted);font:600 12px ui-monospace,monospace}.dot{width:9px;height:9px;border-radius:50%;background:var(--muted)}.dot.ok{background:var(--ok);box-shadow:0 0 0 5px color-mix(in srgb,var(--ok),transparent 82%)}.tabs{display:flex;gap:4px;border-bottom:1px solid var(--line);overflow:auto}.tab{border:0;background:transparent;color:var(--muted);padding:13px 18px;cursor:pointer;border-bottom:3px solid transparent;white-space:nowrap}.tab[aria-selected=true]{color:var(--ink);border-color:var(--accent);font-weight:700}.panel{display:none;padding-top:22px}.panel.active{display:block}.grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:16px}.card{background:var(--surface);border:1px solid var(--line);border-radius:16px;padding:20px;box-shadow:var(--shadow)}.card.wide{grid-column:1/-1}.card h2,.card h3{margin:0 0 12px}.field{display:grid;gap:6px;margin:13px 0}.field label{font-weight:700;font-size:14px}.hint{color:var(--muted);font-size:12px;line-height:1.45}.field input,.field select{width:100%;border:1px solid var(--line);background:var(--bg);color:var(--ink);padding:10px 12px;border-radius:9px;outline:none}.field input:focus,.field select:focus,.tab:focus-visible,.button:focus-visible{outline:3px solid color-mix(in srgb,var(--accent),transparent 55%);outline-offset:2px}.check{display:flex;gap:9px;align-items:center}.check input{width:auto}.actions{display:flex;flex-wrap:wrap;gap:10px;margin-top:18px}.button{border:1px solid var(--line);background:var(--surface);color:var(--ink);padding:10px 15px;border-radius:999px;cursor:pointer;font-weight:700}.button.primary{background:var(--accent);border-color:var(--accent);color:white}.button:disabled{opacity:.55;cursor:wait}.status{min-height:1.5em;color:var(--muted);font-size:13px;margin-top:10px}.status.error{color:var(--bad)}.status.success{color:var(--ok)}.explorer{display:grid;grid-template-columns:minmax(230px,.42fr) 1fr;gap:16px}.controls{display:grid;align-content:start}.output{margin:0;min-height:420px;max-height:70vh;overflow:auto;border-radius:12px;padding:16px;background:#111310;color:#dce7d4;font:12px/1.6 ui-monospace,SFMono-Regular,Consolas,monospace;white-space:pre-wrap;word-break:break-word}.doc{max-width:850px}.doc code{font-family:ui-monospace,monospace;color:var(--accent2)}.doc table{border-collapse:collapse;width:100%}.doc th,.doc td{text-align:left;border-bottom:1px solid var(--line);padding:10px 8px;vertical-align:top}.notice{border-left:4px solid var(--accent);padding:12px 15px;background:color-mix(in srgb,var(--accent),transparent 92%);border-radius:0 10px 10px 0}.auth-block{display:none;margin-top:20px}.auth-block.show{display:block}.usage-head{margin-bottom:16px}.win{padding:12px 0;border-top:1px solid var(--line)}.win:first-of-type{border-top:0}.winhead{display:flex;flex-wrap:wrap;gap:8px;justify-content:space-between;align-items:baseline}.projline{margin-top:6px;font-size:14px;line-height:1.5}.projline .bad{color:var(--bad);font-weight:700}.projline .warn{color:var(--accent);font-weight:700}.badge{display:inline-block;font:700 10px/1.4 ui-monospace,monospace;letter-spacing:.08em;text-transform:uppercase;padding:2px 8px;border-radius:999px;border:1px solid var(--line);color:var(--muted);margin-left:6px;vertical-align:middle}.badge.warn{color:var(--accent);border-color:var(--accent)}.heatmap{display:grid;grid-template-columns:max-content repeat(24,1fr);gap:2px;margin-top:10px;align-items:center}.hm-label{font:600 10px ui-monospace,monospace;color:var(--muted);padding-right:6px;text-transform:uppercase;letter-spacing:.06em}.hm-hour{font:600 8px ui-monospace,monospace;color:var(--muted);text-align:center}.hm-cell{aspect-ratio:1/1;border-radius:3px;min-width:0}@media(max-width:760px){.shell{padding:18px}.mast{grid-template-columns:1fr}.grid,.explorer{grid-template-columns:1fr}.card.wide{grid-column:auto}.output{min-height:300px}}
</style>
</head>
<body>
<main class="shell">
<header class="mast"><div><div class="eyebrow">CLIProxyAPI · Native plugin</div><h1>CPA Quota</h1></div><div class="live"><span id="connection-dot" class="dot"></span><span id="connection-text">Connecting to Management Center</span></div></header>
<div id="auth-block" class="card auth-block" role="alert"><h2>Management session unavailable</h2><p>This trusted plugin panel needs the same-origin CPAMGMT session. Sign in through <code>management.html</code>, enable remembering the management key, then reload this panel.</p></div>
<nav class="tabs" role="tablist" aria-label="CPA Quota panel">
<button class="tab" role="tab" aria-selected="true" aria-controls="configuration" id="tab-configuration">Configuration</button>
<button class="tab" role="tab" aria-selected="false" aria-controls="usage" id="tab-usage">Quota Outlook</button>
<button class="tab" role="tab" aria-selected="false" aria-controls="explorer" id="tab-explorer">API Explorer</button>
<button class="tab" role="tab" aria-selected="false" aria-controls="documentation" id="tab-documentation">Documentation</button>
</nav>
<section id="configuration" class="panel active" role="tabpanel" aria-labelledby="tab-configuration"><form id="config-form"><div class="grid">
<div class="card"><h2>Quota collection</h2><div id="quota-fields"></div></div>
<div class="card"><h2>Pool health</h2><div id="health-fields"></div></div>
<div class="card"><h2>Retention</h2><div id="retention-fields"></div></div>
<div class="card"><h2>Alerts</h2><div id="alert-fields"></div></div>
<div class="card wide"><div class="notice"><strong>Safe persistence.</strong> Save sends only changed, known fields to CLIProxyAPI's authenticated plugin configuration API. The plugin never edits the YAML file itself.</div><div class="actions"><button class="button primary" type="submit" id="save-config">Save changes</button><button class="button" type="button" id="reset-config">Reset unsaved</button><button class="button" type="button" id="reload-config">Reload values</button></div><div id="config-status" class="status" aria-live="polite"></div></div>
</div></form></section>
<section id="usage" class="panel" role="tabpanel" aria-labelledby="tab-usage"><div class="card wide usage-head"><h2>Quota outlook</h2><p class="hint">Fixed-cycle windows carry an end-of-cycle projection; sliding windows and credit pools report raw percentages only. The heatmap is the learned usage profile — cell intensity is each bucket's share of uncached tokens.</p><div class="actions"><button id="usage-refresh" class="button" type="button">Reload snapshot</button></div><div id="usage-status" class="status" aria-live="polite"></div></div><div id="usage-root" class="grid"></div></section>
<section id="explorer" class="panel" role="tabpanel" aria-labelledby="tab-explorer"><div class="explorer"><div class="card controls"><h2>Read-only request</h2><div class="field"><label for="endpoint">Endpoint</label><select id="endpoint"></select></div><div id="query-fields"></div><label class="check"><input id="force-refresh" type="checkbox"> Force provider refresh</label><div class="hint">Only available for quotas and health. Disabled by default to protect provider limits.</div><div class="actions"><button id="send-request" class="button primary" type="button">Send request</button><button id="copy-response" class="button" type="button">Copy JSON</button></div><div id="explorer-status" class="status" aria-live="polite"></div></div><pre id="response-output" class="output" tabindex="0">Select an endpoint and send a request.</pre></div></section>
<section id="documentation" class="panel" role="tabpanel" aria-labelledby="tab-documentation"><article class="card doc"><h2>API contract</h2><p>All data APIs are authenticated Management API routes. This browser document is a static plugin resource; it contains no credentials or operational data.</p><table><thead><tr><th>Endpoint</th><th>Purpose</th></tr></thead><tbody>
<tr><td><code>` + statusRoute + `</code></td><td>Effective plugin and health subsystem status.</td></tr>
<tr><td><code>` + quotaRoute + `</code></td><td>Provider quota snapshot. Use <code>refresh=true</code> deliberately.</td></tr>
<tr><td><code>` + accountRoute + `</code></td><td>Redacted runtime credential inventory.</td></tr>
<tr><td><code>` + healthRoute + `</code></td><td>Account-level total, routable, lost and degraded capacity.</td></tr>
<tr><td><code>` + incidentsRoute + `</code></td><td>Sanitized request failure incidents.</td></tr>
<tr><td><code>` + historyRoute + `</code></td><td>Bounded capacity history.</td></tr>
<tr><td><code>` + profileRoute + `</code></td><td>Per-provider usage profiles rolled up across auth indexes.</td></tr></tbody></table><h3>Health states</h3><p>Exclusive precedence: <code>disabled → unauthorized → forbidden → rate_limited → unavailable → degraded → healthy → unknown</code>. <code>routable = healthy + degraded</code>; <code>lost = total - routable</code>.</p><h3>Configuration</h3><p>The Configuration tab reads and shallow-patches <code>/plugins/` + pluginID + `/config</code>. Runtime status is reloaded by current CLIProxyAPI releases after a management save. If your host does not apply the new value immediately, reload or restart CLIProxyAPI and compare with the Status endpoint.</p><h3>Security</h3><p>The panel accepts no arbitrary URL, HTTP method, header or body. It never displays, copies or writes the management key. All scripts and styles are bundled in this plugin; no third-party assets are loaded.</p></article></section>
</main>
<script>
(()=>{"use strict";
const PLUGIN="` + pluginID + `",AUTH_KEY="cli-proxy-auth",PREFIX="enc::v1::",SALT="cli-proxy-api-webui::secure-storage";
const routes={status:"` + statusRoute + `",quotas:"` + quotaRoute + `",accounts:"` + accountRoute + `",health:"` + healthRoute + `",incidents:"` + incidentsRoute + `",history:"` + historyRoute + `",profile:"` + profileRoute + `"};
const groups={quota:[
 ["cache-ttl","Cache TTL","duration","30m","Request-triggered provider quota cache."],["request-timeout","Request timeout","duration","30s","Per-account upstream timeout."],["max-concurrency","Maximum concurrency","integer","8","Concurrent account quota requests."],["include-disabled","Include disabled accounts","boolean",false,"Include disabled credentials in provider quota scans."]],health:[
 ["database-path","Database path","text","./data/cpa-quota-api-extension.db","Private writable SQLite path."],["health-refresh-interval","Health refresh interval","duration","1m","Minimum 10 seconds."],["health-history-interval","History interval","duration","5m","Minimum 10 seconds."],["failure-window","Failure window","duration","10m","Window used to count recent failures."],["degraded-failure-threshold","Degraded threshold","integer","3","Zero disables degraded classification."],["usage-queue-size","Usage queue size","integer","1024","64 to 65536 events."],["profile-timezone","Profile timezone","text","","IANA zone for usage-profile buckets; blank uses server local."]],retention:[
 ["incident-retention","Incident retention","duration","168h","Minimum one hour."],["incident-max-rows","Incident max rows","integer","10000","100 to 1,000,000."],["history-retention","History retention","duration","720h","Minimum one hour."],["history-max-rows","History max rows","integer","10000","100 to 1,000,000."]],alerts:[
 ["webhook-url","Webhook URL","url","","Optional HTTP(S) aggregate alert destination."],["webhook-timeout","Webhook timeout","duration","10s","Delivery timeout."],["alert-lost-threshold","Lost threshold","integer","1","Zero disables lost-capacity alerts."],["alert-degraded-threshold","Degraded threshold","integer","1","Zero disables degraded-capacity alerts."],["alert-cooldown","Alert cooldown","duration","15m","Reminder interval during a breach."]]};
const fields=Object.values(groups).flat(),fieldMap=new Map(fields.map(f=>[f[0],f]));let auth=null,baseline={},lastResponse="",usageLoaded=false;
function decode(raw){if(!raw||!raw.startsWith(PREFIX))return raw;const bytes=Uint8Array.from(atob(raw.slice(PREFIX.length)),c=>c.charCodeAt(0));const key=new TextEncoder().encode(SALT+"|"+location.host+"|"+navigator.userAgent);for(let i=0;i<bytes.length;i++)bytes[i]^=key[i%key.length];return new TextDecoder().decode(bytes)}
function loadAuth(){const raw=localStorage.getItem(AUTH_KEY);if(!raw)throw new Error("No remembered CPAMGMT session");const parsed=JSON.parse(decode(raw)),state=parsed.state||parsed,key=state.managementKey;if(!key)throw new Error("Management key is not remembered");if(state.apiBase){const origin=new URL(state.apiBase,location.href).origin;if(origin!==location.origin)throw new Error("CPAMGMT API is on another origin")}return{key,base:new URL("../../../management/",location.href)}}
async function request(path,init={}){if(!auth)throw new Error("Management session unavailable");const controller=new AbortController(),timer=setTimeout(()=>controller.abort(),30000);try{const url=new URL(path.replace(/^\/+/,""),auth.base),response=await fetch(url,{...init,credentials:"same-origin",cache:"no-store",signal:controller.signal,headers:{Accept:"application/json",...(init.body?{"Content-Type":"application/json"}:{}),...(init.headers||{}),Authorization:"Bearer "+auth.key}}),text=await response.text();let data=text;try{data=text?JSON.parse(text):null}catch{}if(!response.ok){const message=data&&typeof data==="object"?(data.message||data.error):text;throw new Error("HTTP "+response.status+(message?": "+message:""))}return{status:response.status,data}}finally{clearTimeout(timer)}}
function status(id,message,kind=""){const el=document.getElementById(id);el.textContent=message;el.className="status"+(kind?" "+kind:"")}
function renderFields(){const roots={quota:"quota-fields",health:"health-fields",retention:"retention-fields",alerts:"alert-fields"};for(const [group,list] of Object.entries(groups)){const root=document.getElementById(roots[group]);for(const [name,label,type,def,hint] of list){const wrap=document.createElement("div");wrap.className="field";const lab=document.createElement("label");lab.htmlFor="cfg-"+name;lab.textContent=label;const input=document.createElement("input");input.id="cfg-"+name;input.dataset.name=name;input.dataset.type=type;if(type==="boolean"){input.type="checkbox";wrap.className="field check"}else{input.type=type==="integer"?"number":type==="url"?"url":"text";input.placeholder=String(def);if(type==="integer")input.step="1"}const small=document.createElement("div");small.className="hint";small.textContent=hint;wrap.append(lab,input,small);root.append(wrap)}}}
function applyValues(values){baseline={};for(const [name,,type,def] of fields){const value=Object.prototype.hasOwnProperty.call(values,name)?values[name]:def,el=document.getElementById("cfg-"+name);baseline[name]=value;if(type==="boolean")el.checked=Boolean(value);else el.value=value==null?"":String(value)}}
async function loadConfig(){status("config-status","Loading configuration…");try{const result=await request("plugins/"+PLUGIN+"/config");applyValues(result.data||{});status("config-status","Configuration loaded.","success")}catch(error){status("config-status",error.message,"error")}}
function durationMs(value){const match=value.match(/^((?:\d+(?:\.\d*)?|\.\d+)(?:ns|us|µs|ms|s|m|h))+$/);if(!match)return NaN;let total=0;const units={ns:1e-6,us:.001,"µs":.001,ms:1,s:1000,m:60000,h:3600000},part=/(\d+(?:\.\d*)?|\.\d+)(ns|us|µs|ms|s|m|h)/g;for(const item of value.matchAll(part))total+=Number(item[1])*units[item[2]];return total}
function values(){const out={};for(const [name,,type] of fields){const el=document.getElementById("cfg-"+name);if(type==="boolean")out[name]=el.checked;else if(type==="integer"){if(!/^\d+$/.test(el.value))throw new Error(name+" must be a non-negative integer");out[name]=Number(el.value)}else{out[name]=el.value.trim();if(type==="duration"&&!Number.isFinite(durationMs(out[name])))throw new Error(name+" must use a Go duration such as 30s, 1h30m or 24h");if(type==="url"&&out[name]){const u=new URL(out[name]);if(!["http:","https:"].includes(u.protocol)||!u.host)throw new Error(name+" must use a complete HTTP or HTTPS URL")}}}if(!out["database-path"])throw new Error("database-path is required");const bounds={"cache-ttl":[60000,Infinity],"request-timeout":[1000,300000],"health-refresh-interval":[10000,86400000],"health-history-interval":[10000,86400000],"failure-window":[1000,86400000],"incident-retention":[3600000,31536000000],"history-retention":[3600000,31536000000],"webhook-timeout":[1000,300000],"alert-cooldown":[1000,86400000]};for(const [key,[min,max]] of Object.entries(bounds)){const n=durationMs(out[key]);if(n<min||n>max)throw new Error(key+" is outside the supported range")}if(out["max-concurrency"]<1||out["max-concurrency"]>64)throw new Error("max-concurrency must be 1 to 64");if(out["degraded-failure-threshold"]<0||out["alert-lost-threshold"]<0||out["alert-degraded-threshold"]<0)throw new Error("thresholds must be non-negative");if(out["usage-queue-size"]<64||out["usage-queue-size"]>65536)throw new Error("usage-queue-size must be 64 to 65536");for(const key of ["incident-max-rows","history-max-rows"])if(out[key]<100||out[key]>1000000)throw new Error(key+" must be 100 to 1,000,000");return out}
async function saveConfig(event){event.preventDefault();let current;try{current=values()}catch(error){status("config-status",error.message,"error");return}const changed={};for(const [key,value] of Object.entries(current))if(JSON.stringify(value)!==JSON.stringify(baseline[key]))changed[key]=value;if(!Object.keys(changed).length){status("config-status","No changes to save.");return}const button=document.getElementById("save-config");button.disabled=true;status("config-status","Saving known configuration fields…");try{await request("plugins/"+PLUGIN+"/config",{method:"PATCH",body:JSON.stringify(changed)});await loadConfig();status("config-status","Saved. Current CLIProxyAPI releases reload plugin configuration automatically; verify effective values in Status.","success")}catch(error){status("config-status",error.message,"error")}finally{button.disabled=false}}
const queryDefs={quotas:[["provider","Provider"],["status","Status"],["limit","Limit"]],accounts:[["provider","Provider"],["limit","Limit"]],status:[],health:[["provider","Provider"],["state","State"],["limit","Limit"]],incidents:[["provider","Provider"],["state","State"],["status_code","Status code"],["from","From RFC3339"],["to","To RFC3339"],["limit","Limit"]],history:[["from","From RFC3339"],["to","To RFC3339"],["limit","Limit"]],profile:[]};
function renderQuery(){const name=document.getElementById("endpoint").value,root=document.getElementById("query-fields");root.textContent="";for(const [key,label] of queryDefs[name]){const wrap=document.createElement("div");wrap.className="field";const lab=document.createElement("label");lab.htmlFor="query-"+key;lab.textContent=label;const input=document.createElement("input");input.id="query-"+key;input.dataset.query=key;input.type="text";wrap.append(lab,input);root.append(wrap)}const refresh=document.getElementById("force-refresh");refresh.checked=false;refresh.disabled=!(name==="quotas"||name==="health")}
async function sendRequest(){const name=document.getElementById("endpoint").value,params=new URLSearchParams();document.querySelectorAll("[data-query]").forEach(input=>{if(input.value.trim())params.set(input.dataset.query,input.value.trim())});if(!document.getElementById("force-refresh").disabled&&document.getElementById("force-refresh").checked)params.set("refresh","true");const path=routes[name]+(params.size?"?"+params.toString():"");status("explorer-status","Sending authenticated GET…");const button=document.getElementById("send-request");button.disabled=true;try{const started=performance.now(),result=await request(path);lastResponse=JSON.stringify(result.data,null,2);document.getElementById("response-output").textContent=lastResponse;status("explorer-status",result.status+" OK · "+Math.round(performance.now()-started)+" ms","success")}catch(error){lastResponse="";document.getElementById("response-output").textContent=error.message;status("explorer-status",error.message,"error")}finally{button.disabled=false}}
function el(tag,cls,text){const node=document.createElement(tag);if(cls)node.className=cls;if(text!=null)node.textContent=text;return node}
function fmtPct(value){return value==null||!Number.isFinite(value)?"—":(Math.round(value*10)/10)+"%"}
function badge(text,kind){return el("span","badge"+(kind?" "+kind:""),text)}
const verdictNames={on_track:"on track",tight:"tight",will_exhaust:"will exhaust"};
function projectionNode(projection){
 const wrap=el("div","projline");
 const verdict=verdictNames[projection.verdict]||projection.verdict;
 let sentence=fmtPct(projection.projected_used_percent)+" projected by reset — "+verdict;
 if(projection.basis==="profile")sentence+=" (clock alone would say "+fmtPct(projection.naive_projected_percent)+")";
 else sentence+=" (clock method — no learned profile applied)";
 const kind=projection.verdict==="will_exhaust"?"bad":projection.verdict==="tight"?"warn":"";
 wrap.append(el("span",kind,sentence));
 if(projection.projected_exhaustion_at)wrap.append(el("span","hint"," · may cross 100% around "+new Date(projection.projected_exhaustion_at).toLocaleString()));
 if(projection.basis==="uniform")wrap.append(badge("uniform basis","warn"));
 if(projection.confidence==="low")wrap.append(badge("low confidence","warn"));
 else if(projection.confidence)wrap.append(badge(projection.confidence+" confidence"));
 return wrap}
function windowNode(window){
 const wrap=el("div","win"),head=el("div","winhead");
 head.append(el("strong",null,window.id));
 let raw=fmtPct(window.used_percent)+" used · "+fmtPct(window.remaining_percent)+" left";
 if(window.reset_at)raw+=" · resets "+new Date(window.reset_at).toLocaleString();
 head.append(el("span","hint",raw));
 wrap.append(head);
 if(window.projection)wrap.append(projectionNode(window.projection));
 return wrap}
function heatmapNode(scheme,profile){
 const wrap=el("div"),map=el("div","heatmap");
 map.append(el("span","hm-label",""));
 for(let hour=0;hour<24;hour++)map.append(el("span","hm-hour",String(hour)));
 const cells=new Map((profile.buckets||[]).map(bucket=>[bucket.day_type+"-"+bucket.hour,bucket]));
 let max=0;for(const bucket of cells.values())if(bucket.weight>max)max=bucket.weight;
 for(const dayType of scheme.day_types||["weekday","weekend"]){
  map.append(el("span","hm-label",dayType));
  for(let hour=0;hour<24;hour++){
   const bucket=cells.get(dayType+"-"+hour),weight=bucket?bucket.weight:0,cell=el("span","hm-cell");
   const strength=max>0?Math.round(weight/max*96):0;
   cell.style.background="color-mix(in srgb,var(--accent2) "+Math.max(strength,4)+"%,transparent)";
   cell.title=dayType+" "+hour+":00 — "+(weight*100).toFixed(1)+"% of tokens"+(bucket&&bucket.n?" · "+bucket.n+" events":"");
   map.append(cell)}}
 wrap.append(map);
 wrap.append(el("div","hint","Hour of day in "+(scheme.timezone||"server local time")+" · weekend share "+fmtPct((profile.weekend_share||0)*100)+" · token coverage "+fmtPct((profile.token_coverage||0)*100)+" · last "+(scheme.retention_days||30)+" days"));
 return wrap}
function providerCard(name,provider,profileData){
 const card=el("div","card");
 card.append(el("h2",null,name));
 if(provider.status)card.append(el("div","hint","status: "+provider.status));
 const windows=provider.windows||[];
 if(windows.length){const list=el("div");for(const window of windows)list.append(windowNode(window));card.append(list)}
 else card.append(el("div","hint","No quota windows reported."));
 const heading=el("h3",null,"Usage profile heatmap");
 card.append(heading);
 const providerProfile=profileData&&profileData.providers?profileData.providers[name]:null;
 if(providerProfile){
  if(providerProfile.confidence==="low")heading.append(badge("low confidence","warn"));
  else if(providerProfile.confidence)heading.append(badge(providerProfile.confidence+" confidence"));
  card.append(heatmapNode(profileData.bucket_scheme||{},providerProfile))}
 else card.append(el("div","hint","No usage profile observed yet — projections fall back to the uniform clock method."));
 return card}
function renderUsage(snapshot,profileData){
 const root=document.getElementById("usage-root");root.textContent="";
 const names=snapshot&&snapshot.providers?Object.keys(snapshot.providers).sort():[];
 if(!names.length){const card=el("div","card wide");card.append(el("h2",null,"No providers"));card.append(el("div","hint","No accounts produced a quota snapshot."));root.append(card);return}
 for(const name of names)root.append(providerCard(name,snapshot.providers[name],profileData))}
async function loadUsage(){
 status("usage-status","Loading quota snapshot and usage profile…");
 const button=document.getElementById("usage-refresh");button.disabled=true;
 try{
  const quotas=await request(routes.quotas);
  let profileData=null;
  try{profileData=(await request(routes.profile)).data}catch{}
  renderUsage(quotas.data,profileData);
  status("usage-status",profileData?"Snapshot and profile loaded.":"Snapshot loaded; usage profile unavailable (health monitoring off?).",profileData?"success":"error")
 }catch(error){status("usage-status",error.message,"error")}finally{button.disabled=false}}
function bind(){document.querySelectorAll("[role=tab]").forEach((tab,index)=>{tab.addEventListener("click",()=>activate(tab));tab.addEventListener("keydown",event=>{const tabs=[...document.querySelectorAll("[role=tab]")];let next;if(event.key==="Home")next=0;else if(event.key==="End")next=tabs.length-1;else if(["ArrowLeft","ArrowRight"].includes(event.key))next=(index+(event.key==="ArrowRight"?1:-1)+tabs.length)%tabs.length;else return;event.preventDefault();tabs[next].focus();activate(tabs[next])})});document.getElementById("config-form").addEventListener("submit",saveConfig);document.getElementById("reset-config").addEventListener("click",()=>{applyValues(baseline);status("config-status","Unsaved changes reset.")});document.getElementById("reload-config").addEventListener("click",loadConfig);document.getElementById("endpoint").addEventListener("change",renderQuery);document.getElementById("send-request").addEventListener("click",sendRequest);document.getElementById("copy-response").addEventListener("click",async()=>{if(!lastResponse)return;await navigator.clipboard.writeText(lastResponse);status("explorer-status","Response copied.","success")});document.getElementById("usage-refresh").addEventListener("click",loadUsage)}
function activate(tab){document.querySelectorAll("[role=tab]").forEach(t=>{const selected=t===tab;t.setAttribute("aria-selected",String(selected));t.tabIndex=selected?0:-1});document.querySelectorAll("[role=tabpanel]").forEach(p=>p.classList.toggle("active",p.id===tab.getAttribute("aria-controls")));if(tab.id==="tab-usage"&&auth&&!usageLoaded){usageLoaded=true;loadUsage()}}
function boot(){activate(document.querySelector('[role="tab"][aria-selected="true"]'));renderFields();const endpoint=document.getElementById("endpoint");for(const name of Object.keys(routes)){const option=document.createElement("option");option.value=name;option.textContent=name[0].toUpperCase()+name.slice(1);endpoint.append(option)}renderQuery();bind();try{auth=loadAuth();document.getElementById("connection-dot").classList.add("ok");document.getElementById("connection-text").textContent="Same-origin management session";loadConfig()}catch(error){document.getElementById("connection-text").textContent="Session unavailable";document.getElementById("auth-block").classList.add("show");status("config-status",error.message,"error")}}
boot();
})();
</script>
</body>
</html>`

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
