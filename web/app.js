const $=s=>document.querySelector(s), $$=s=>[...document.querySelectorAll(s)];
let token=sessionStorage.getItem('nodra_token')||'';
const toast=m=>{const t=$('#toast');t.textContent=m;t.classList.add('show');setTimeout(()=>t.classList.remove('show'),2200)};
const asList=v=>Array.isArray(v)?v:(v==null?[]:[v]);

async function api(path,opt={}){
  const h={...(opt.headers||{})};
  if(token)h.Authorization=`Bearer ${token}`;
  if(opt.body)h['Content-Type']='application/json';
  const r=await fetch(path,{...opt,headers:h});
  if(r.status===401){signOut(false);throw new Error('Session expired')};
  if(!r.ok){let msg=`${r.status}`;try{const j=await r.json();msg=j.error||j.message||msg}catch{try{msg=await r.text()||msg}catch{}}throw new Error(msg)}
  return r.status===204?null:r.json()
}

function showLogin(){
  $('#loginGate').hidden=false;
  $('#appShell').hidden=true;
  document.body.classList.add('login-mode');
}
function showConsole(){
  $('#loginGate').hidden=true;
  $('#appShell').hidden=false;
  document.body.classList.remove('login-mode');
}
function signOut(toastMsg=true){
  token='';
  sessionStorage.removeItem('nodra_token');
  showLogin();
  if(toastMsg)toast('Signed out');
}

async function ensureSession(){
  if(!token){showLogin();return false}
  try{
    const me=await api('/api/v1/auth/me');
    if(!me.authenticated){signOut(false);return false}
    showConsole();
    return true
  }catch{
    signOut(false);
    return false
  }
}

$('#loginForm').addEventListener('submit',async e=>{
  e.preventDefault();
  const err=$('#loginErr');
  err.hidden=true;err.textContent='';
  const btn=$('#loginBtn');
  btn.disabled=true;btn.textContent='Signing in…';
  try{
    const res=await fetch('/api/v1/auth/login',{
      method:'POST',
      headers:{'Content-Type':'application/json'},
      body:JSON.stringify({username:$('#loginUser').value.trim(),password:$('#loginPass').value})
    });
    const data=await res.json().catch(()=>({}));
    if(!res.ok)throw new Error(data.error||'Sign in failed');
    token=data.token||'';
    if(!token)throw new Error('No session token returned');
    sessionStorage.setItem('nodra_token',token);
    $('#loginPass').value='';
    showConsole();
    await refresh();
    toast('Welcome back');
  }catch(ex){
    err.textContent=ex.message||'Sign in failed';
    err.hidden=false;
  }finally{
    btn.disabled=false;btn.textContent='Sign in';
  }
});

const ago=s=>{if(!s)return'never';const n=(Date.now()-new Date(s).getTime())/1000;if(n<60)return`${Math.max(0,Math.floor(n))}s ago`;if(n<3600)return`${Math.floor(n/60)}m ago`;if(n<86400)return`${Math.floor(n/3600)}h ago`;return`${Math.floor(n/86400)}d ago`};
const bytes=n=>{if(!Number.isFinite(Number(n)))return'—';n=Number(n);for(const u of ['B','KB','MB','GB','TB']){if(n<1024||u==='TB')return`${n<10&&u!=='B'?n.toFixed(1):Math.round(n)} ${u}`;n/=1024}};
function E(tag,cls,text){const e=document.createElement(tag);if(cls)e.className=cls;if(text!==undefined)e.textContent=String(text);return e}
function empty(host,msg='Nothing here yet.'){host.replaceChildren(E('div','empty',msg))}
function row(cols,status,action){const r=E('div','row');const first=E('div','cell-main');first.append(E('b','',cols[0]?.[0]||'—'),E('small','',cols[0]?.[1]||''));r.append(first);if(status)r.append(E('span',`status ${String(status).toLowerCase()}`,status));else r.append(E('small','',cols[1]||'—'));for(let i=2;i<4;i++)r.append(E('small','',cols[i]||'—'));if(action){const b=E('button','action',action.label);b.onclick=action.click;r.lastChild.replaceWith(b)}return r}
function renderSites(items){const h=$('#sitesTable');h.replaceChildren();if(!items?.length)return empty(h);items.forEach(v=>{const q=v.metrics?.queue_depth??0, qb=bytes(v.metrics?.queue_bytes||0);h.append(row([[v.name,v.id],null,v.version||'—',`${ago(v.last_seen)} · ${q} queued / ${qb}`],v.status))})}
function renderDevices(items){const h=$('#devicesTable');h.replaceChildren();if(!items?.length)return empty(h,'No devices registered.');items.forEach(v=>h.append(row([[v.name,v.id],null,v.protocol||'—',ago(v.last_seen)],v.status)))}
function renderTwins(items){const h=$('#twinsTable');h.replaceChildren();if(!items?.length)return empty(h,'No device twins yet.');items.forEach(v=>h.append(row([[v.device_id,`desired v${v.desired_version||0} · reported v${v.reported_version||0}`],null,Object.keys(v.desired||{}).length+' desired keys',ago(v.updated_at)],v.desired_version===v.reported_version?'healthy':'syncing')))}
function renderRoutes(items){const h=$('#routesTable');h.replaceChildren();if(!items?.length)return empty(h,'Create a route to forward edge topics.');items.forEach(v=>{let host='invalid';try{host=new URL(v.target_url).host}catch{};h.append(row([[v.name,v.topic],null,v.method||'POST',host],v.enabled?'active':'paused'))})}
function renderDeployments(items){const h=$('#deploymentsTable');h.replaceChildren();if(!items?.length)return empty(h,'No edge applications deployed.');items.forEach(v=>h.append(row([[v.name,v.image],null,`${v.version} · desired ${v.desired_state||'running'}`,v.site_id],v.status||v.actual_state)))}
function renderAlerts(items){const h=$('#alertsTable');h.replaceChildren();items=(items||[]).filter(x=>!x.resolved).slice().reverse().slice(0,20);if(!items.length)return empty(h,'No open alerts.');items.forEach(v=>h.append(row([[v.type,v.message],null,v.site_id||'control plane',ago(v.created_at)],v.severity)))}
function renderDLQ(items){const h=$('#dlqTable');h.replaceChildren();if(!items?.length)return empty(h,'No failed deliveries. That is exactly what you want.');items.forEach(v=>{const d=v.delivery||{};h.append(row([[d.topic,`${d.event_id} · ${v.reason||d.last_error||'failed'}`],null,`${d.attempts||0}/${d.max_attempts||0} attempts`,ago(v.failed_at)],'failed',{label:'Replay',click:async()=>{try{await api(`/api/v1/deadletters/${encodeURIComponent(d.id)}/replay`,{method:'POST',body:'{}'});toast('Delivery replayed');refresh()}catch{toast('Replay failed')}}}))})}

function esc(s){return String(s??'').replace(/[&<>"']/g,c=>({ '&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;' }[c]))}
function fmtTS(t){try{const d=new Date(t);return d.toISOString().replace('T',' ').replace('Z','Z')}catch{return String(t||'')}}
function renderActivity(items,host,limit){
  const h=host||$('#activityLog');
  if(!h)return;
  items=asList(items).slice().reverse();
  if(limit)items=items.slice(0,limit);
  if(!items.length){h.innerHTML='<span class="muted">No activity yet. Start nodra-sim or wait for edge heartbeats.</span>';return}
  h.innerHTML=items.map(e=>{
    const lvl=esc(e.level||'info');
    const chap=e.chapter?`<span class="chap">[${esc(e.chapter)}]</span> `:'';
    const src=e.source?`<span class="detail">${esc(e.source)}</span> `:'';
    const act=e.action?`<span class="detail">${esc(e.action)}</span> · `:'';
    let detail='';
    if(e.detail&&typeof e.detail==='object'){
      try{detail=` <span class="detail">${esc(JSON.stringify(e.detail))}</span>`}catch{}
    }
    return `<span class="log-line"><span class="ts">${esc(fmtTS(e.time))}</span> <span class="lvl-${lvl}">${lvl.toUpperCase().padEnd(7)}</span> ${chap}${src}${act}<span class="msg">${esc(e.message||'')}</span>${detail}</span>`
  }).join('\n');
  h.scrollTop=h.scrollHeight
}

async function refresh(){
  if(!token)return;
  try{
    const [o,s,d,t,r,p,a,q,act]=await Promise.all([
      api('/api/v1/overview'),api('/api/v1/sites'),api('/api/v1/devices'),api('/api/v1/twins'),
      api('/api/v1/routes'),api('/api/v1/deployments'),api('/api/v1/alerts'),api('/api/v1/deadletters'),
      api('/api/v1/activity?limit=300')
    ]);
    $('#sitesCount').textContent=o.sites;$('#onlineCount').textContent=o.online_sites;$('#queueCount').textContent=o.pending_deliveries;$('#dlqCount').textContent=o.dead_letters;$('#queueBytes').textContent=`${bytes(o.delivery_queue_bytes)} durable WAL`;$('#siteHint').textContent=`${o.devices} devices · ${o.twins} twins`;
    renderSites(asList(s));renderDevices(asList(d));renderTwins(asList(t));renderRoutes(asList(r));renderDeployments(asList(p));renderAlerts(asList(a));renderDLQ(asList(q));
    renderActivity(act,$('#activityLog'));
    renderActivity(act,$('#liveLogPreview'),14);
    $('#fleetState').textContent='Connected';$('#fleetState').classList.add('ok')
  }catch(e){
    $('#fleetState').textContent='Disconnected';$('#fleetState').classList.remove('ok');
    if(token)toast(e.message||'Could not load control plane')
  }
}

function page(name){$$('[data-page]').forEach(p=>p.hidden=p.dataset.page!==name);$$('#navTabs button').forEach(b=>b.classList.toggle('active',b.dataset.tab===name));history.replaceState(null,'',`#${name}`)}
$$('#navTabs button').forEach(b=>b.onclick=()=>page(b.dataset.tab));
$('#signOutBtn').onclick=()=>signOut(true);
$('#refreshBtn').onclick=refresh;
$('#refreshLogsBtn').onclick=refresh;
$('#seedRouteBtn').onclick=async()=>{try{await api('/api/v1/routes',{method:'POST',body:JSON.stringify({name:'Telemetry webhook',topic:'factory/+/telemetry',target_url:'http://example.invalid/events',method:'POST',enabled:false,retry_max:5,timeout_seconds:5})});toast('Demo route created');refresh()}catch(e){toast(e.message||'Could not create route')}};

(async()=>{
  const ok=await ensureSession();
  const initial=location.hash.slice(1);
  if(['overview','sites','devices','streams','apps','dlq','logs'].includes(initial))page(initial);
  if(ok){await refresh();setInterval(refresh,5000)}
})();
