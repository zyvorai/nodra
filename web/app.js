// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

const $=s=>document.querySelector(s), $$=s=>[...document.querySelectorAll(s)];
let token=sessionStorage.getItem('nodra_token')||'';
let role=sessionStorage.getItem('nodra_role')||'admin';
const toast=m=>{const t=$('#toast');t.textContent=m;t.classList.add('show');setTimeout(()=>t.classList.remove('show'),2200)};
const asList=v=>Array.isArray(v)?v:(v==null?[]:[v]);
const isAdmin=()=>role!=='viewer';

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
  const gate=$('#loginGate');
  gate.classList.add('open');
  gate.setAttribute('aria-hidden','false');
  $('#appShell').hidden=true;
  document.body.classList.add('login-locked');
  fillLoginContext();
}
function showConsole(){
  const gate=$('#loginGate');
  gate.classList.remove('open');
  gate.setAttribute('aria-hidden','true');
  $('#appShell').hidden=false;
  document.body.classList.remove('login-locked');
  document.title='Nodra — Open edge runtime';
  applyRoleUI();
}
function signOut(toastMsg=true){
  token='';
  role='admin';
  sessionStorage.removeItem('nodra_token');
  sessionStorage.removeItem('nodra_role');
  showLogin();
  if(toastMsg)toast('Signed out');
}
function applyRoleUI(){
  $$('[data-admin-only]').forEach(el=>{el.hidden=!isAdmin()});
  const chip=$('#roleChip');
  if(chip){chip.textContent=isAdmin()?'Admin':'Viewer';chip.classList.toggle('ok',isAdmin())}
}

function loginDest(){
  const{hostname,origin,port,protocol}=window.location;
  return{
    host:hostname||'localhost',
    origin:origin||'',
    port:port||(protocol==='https:'?'443':protocol==='http:'?'80':''),
    protocol:(protocol||'http:').replace(':','')
  };
}
function scrollLoginChapter(id){
  const el=document.getElementById(id);
  const sc=document.querySelector('.login-store-scroll');
  if(!el||!sc)return;
  const top=Math.max(0, el.getBoundingClientRect().top - sc.getBoundingClientRect().top + sc.scrollTop);
  sc.scrollTo({top, behavior:'auto'});
  if(Math.abs(sc.scrollTop-top)>8){
    try{el.scrollIntoView({block:'start', behavior:'auto'})}catch{}
  }
}
async function fillLoginContext(){
  const d=loginDest();
  const set=(id,v)=>{const el=$(id);if(el)el.textContent=v||'—'};
  set('#loginDestHost',(d.host||'localhost')+'.');
  set('#loginFactHost',d.host);
  set('#loginFactPort',d.port||'—');
  set('#loginFactOrigin',d.origin);
  set('#loginNavHost',d.host);
  set('#loginSignHost',d.host);
  document.title=`Sign in · Nodra · ${d.host||'edge'}`;
  try{
    const ready=await fetch('/readyz').then(r=>r.json()).catch(()=>null);
    set('#loginFactReady',(ready&&ready.status)||'—');
  }catch{set('#loginFactReady','—')}
  try{
    const ver=await fetch('/api/v1/version').then(r=>r.json()).catch(()=>null);
    set('#loginFactVersion',(ver&&(ver.version||ver.Version))||'—');
  }catch{set('#loginFactVersion','—')}
}
function wireLoginChapters(){
  $$('[data-login-chapter]').forEach(b=>{
    if(b.dataset.wired==='1')return;
    b.dataset.wired='1';
    b.addEventListener('click',e=>{
      e.preventDefault();
      const id=b.getAttribute('data-login-chapter')||((b.getAttribute('href')||'').replace(/^#/,''));
      if(id)scrollLoginChapter(id);
    });
  });
}

async function ensureSession(){
  if(!token){showLogin();return false}
  try{
    const me=await api('/api/v1/auth/me');
    if(!me.authenticated){signOut(false);return false}
    role=(me.user&&me.user.role)||'admin';
    sessionStorage.setItem('nodra_role',role);
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
    role=(data.user&&data.user.role)||'admin';
    sessionStorage.setItem('nodra_token',token);
    sessionStorage.setItem('nodra_role',role);
    $('#loginPass').value='';
    showConsole();
    await refresh();
    toast(role==='viewer'?'Signed in as viewer':'Welcome back');
  }catch(ex){
    err.textContent=ex.message||'Sign in failed';
    err.hidden=false;
    $('#loginCard')&&$('#loginCard').classList.add('login-shake');
    setTimeout(()=>{const c=$('#loginCard');if(c)c.classList.remove('login-shake')},500);
  }finally{
    btn.disabled=false;btn.textContent='Sign in to Nodra';
  }
});

const ago=s=>{if(!s)return'never';const n=(Date.now()-new Date(s).getTime())/1000;if(n<60)return`${Math.max(0,Math.floor(n))}s ago`;if(n<3600)return`${Math.floor(n/60)}m ago`;if(n<86400)return`${Math.floor(n/3600)}h ago`;return`${Math.floor(n/86400)}d ago`};
const bytes=n=>{if(!Number.isFinite(Number(n)))return'—';n=Number(n);for(const u of ['B','KB','MB','GB','TB']){if(n<1024||u==='TB')return`${n<10&&u!=='B'?n.toFixed(1):Math.round(n)} ${u}`;n/=1024}};
function E(tag,cls,text){const e=document.createElement(tag);if(cls)e.className=cls;if(text!==undefined)e.textContent=String(text);return e}
function empty(host,msg='Nothing here yet.'){host.replaceChildren(E('div','empty',msg))}
function actionCell(actions){
  const wrap=E('div','row-actions');
  (actions||[]).forEach(a=>{
    if(!a||(a.admin&&!isAdmin()))return;
    const b=E('button','action',a.label);
    if(a.danger)b.classList.add('danger');
    b.onclick=a.click;
    wrap.append(b);
  });
  return wrap;
}
function row(cols,status,actions){
  const r=E('div','row');
  const first=E('div','cell-main');
  first.append(E('b','',cols[0]?.[0]||'—'),E('small','',cols[0]?.[1]||''));
  r.append(first);
  if(status)r.append(E('span',`status ${String(status).toLowerCase()}`,status));
  else r.append(E('small','',cols[1]||'—'));
  for(let i=2;i<4;i++)r.append(E('small','',cols[i]||'—'));
  if(actions&&actions.length)r.append(actionCell(actions));
  return r;
}

function renderSites(items){
  const h=$('#sitesTable');h.replaceChildren();
  if(!items?.length)return empty(h);
  items.forEach(v=>{
    const q=v.metrics?.queue_depth??0, qb=bytes(v.metrics?.queue_bytes||0);
    const actions=[];
    if(!v.revoked)actions.push({label:'Revoke',admin:true,danger:true,click:async()=>{
      if(!confirm(`Revoke site ${v.name||v.id}?`))return;
      try{await api(`/api/v1/sites/${encodeURIComponent(v.id)}/revoke`,{method:'POST',body:'{}'});toast('Site revoked');refresh()}catch(e){toast(e.message||'Revoke failed')}
    }});
    h.append(row([[v.name,v.id],null,v.version||'—',`${ago(v.last_seen)} · ${q} queued / ${qb}`],v.status,actions));
  });
}
function renderDevices(items){
  const h=$('#devicesTable');h.replaceChildren();
  if(!items?.length)return empty(h,'No devices registered.');
  items.forEach(v=>h.append(row([[v.name,v.id],null,v.protocol||'—',ago(v.last_seen)],v.status)));
}
function renderTwins(items){
  const h=$('#twinsTable');h.replaceChildren();
  if(!items?.length)return empty(h,'No device twins yet.');
  items.forEach(v=>{
    const actions=[{label:'Set desired',admin:true,click:async()=>{
      const raw=prompt('Desired twin JSON',JSON.stringify(v.desired||{mode:'auto'},null,0));
      if(raw==null)return;
      let desired;try{desired=JSON.parse(raw)}catch{toast('Invalid JSON');return}
      try{await api(`/api/v1/twins/${encodeURIComponent(v.device_id)}/desired`,{method:'PUT',body:JSON.stringify({desired})});toast('Desired state updated');refresh()}catch(e){toast(e.message||'Update failed')}
    }}];
    h.append(row([[v.device_id,`desired v${v.desired_version||0} · reported v${v.reported_version||0}`],null,Object.keys(v.desired||{}).length+' desired keys',ago(v.updated_at)],v.desired_version===v.reported_version?'healthy':'syncing',actions));
  });
}
function renderRoutes(items){
  const h=$('#routesTable');h.replaceChildren();
  if(!items?.length)return empty(h,'Create a route to forward edge topics.');
  items.forEach(v=>{
    let host='invalid';try{host=new URL(v.target_url).host}catch{}
    const actions=[{label:'Delete',admin:true,danger:true,click:async()=>{
      if(!confirm(`Delete route ${v.name}?`))return;
      try{await api(`/api/v1/routes/${encodeURIComponent(v.id)}`,{method:'DELETE'});toast('Route deleted');refresh()}catch(e){toast(e.message||'Delete failed')}
    }}];
    h.append(row([[v.name,v.topic],null,v.method||'POST',host],v.enabled?'active':'paused',actions));
  });
}
function renderDeployments(items){
  const h=$('#deploymentsTable');h.replaceChildren();
  if(!items?.length)return empty(h,'No edge applications deployed.');
  items.forEach(v=>{
    const actions=[
      {label:v.desired_state==='stopped'?'Start':'Stop',admin:true,click:async()=>{
        const next=v.desired_state==='stopped'?'running':'stopped';
        try{await api(`/api/v1/deployments/${encodeURIComponent(v.id)}`,{method:'PATCH',body:JSON.stringify({desired_state:next})});toast(`Desired ${next}`);refresh()}catch(e){toast(e.message||'Update failed')}
      }},
      {label:'Delete',admin:true,danger:true,click:async()=>{
        if(!confirm(`Delete deployment ${v.name}?`))return;
        try{await api(`/api/v1/deployments/${encodeURIComponent(v.id)}`,{method:'DELETE'});toast('Deployment deleted');refresh()}catch(e){toast(e.message||'Delete failed')}
      }}
    ];
    h.append(row([[v.name,v.image],null,`${v.version} · desired ${v.desired_state||'running'}`,v.site_id],v.status||v.actual_state,actions));
  });
}
function renderAlerts(items){
  const h=$('#alertsTable');h.replaceChildren();
  items=(items||[]).filter(x=>!x.resolved).slice().reverse().slice(0,20);
  if(!items.length)return empty(h,'No open alerts.');
  items.forEach(v=>{
    const actions=[{label:'Resolve',admin:true,click:async()=>{
      try{await api(`/api/v1/alerts/${encodeURIComponent(v.id)}/resolve`,{method:'POST',body:'{}'});toast('Alert resolved');refresh()}catch(e){toast(e.message||'Resolve failed')}
    }}];
    h.append(row([[v.type,v.message],null,v.site_id||'control plane',ago(v.created_at)],v.severity,actions));
  });
}
function renderDLQ(items){
  const h=$('#dlqTable');h.replaceChildren();
  if(!items?.length)return empty(h,'No failed deliveries. That is exactly what you want.');
  items.forEach(v=>{
    const d=v.delivery||{};
    const actions=[
      {label:'Replay',admin:true,click:async()=>{try{await api(`/api/v1/deadletters/${encodeURIComponent(d.id)}/replay`,{method:'POST',body:'{}'});toast('Delivery replayed');refresh()}catch{toast('Replay failed')}}},
      {label:'Delete',admin:true,danger:true,click:async()=>{
        if(!confirm('Delete this dead letter?'))return;
        try{await api(`/api/v1/deadletters/${encodeURIComponent(d.id)}`,{method:'DELETE'});toast('Dead letter deleted');refresh()}catch(e){toast(e.message||'Delete failed')}
      }}
    ];
    h.append(row([[d.topic,`${d.event_id} · ${v.reason||d.last_error||'failed'}`],null,`${d.attempts||0}/${d.max_attempts||0} attempts`,ago(v.failed_at)],'failed',actions));
  });
}

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
    $('#fleetState').textContent='Connected';$('#fleetState').classList.add('ok');
    applyRoleUI();
  }catch(e){
    $('#fleetState').textContent='Disconnected';$('#fleetState').classList.remove('ok');
    if(token)toast(e.message||'Could not load control plane')
  }
}

function page(name){$$('[data-page]').forEach(p=>p.hidden=p.dataset.page!==name);$$('#navTabs button').forEach(b=>b.classList.toggle('active',b.dataset.tab===name));history.replaceState(null,'',`#${name}`)}
$$('#navTabs button').forEach(b=>b.onclick=()=>page(b.dataset.tab));
$('#signOutBtn').onclick=()=>signOut(true);
$('#refreshBtn').onclick=refresh;
const refreshLogsBtn=$('#refreshLogsBtn'); if(refreshLogsBtn) refreshLogsBtn.onclick=refresh;
const seedRouteBtn=$('#seedRouteBtn');
if(seedRouteBtn) seedRouteBtn.onclick=async()=>{try{await api('/api/v1/routes',{method:'POST',body:JSON.stringify({name:'Telemetry webhook',topic:'factory/+/telemetry',target_url:'http://example.invalid/events',method:'POST',enabled:false,retry_max:5,timeout_seconds:5})});toast('Demo route created');refresh()}catch(e){toast(e.message||'Could not create route')}};

const routeForm=$('#routeForm');
if(routeForm)routeForm.addEventListener('submit',async e=>{
  e.preventDefault();
  if(!isAdmin())return;
  try{
    await api('/api/v1/routes',{method:'POST',body:JSON.stringify({
      name:$('#routeName').value.trim(),
      topic:$('#routeTopic').value.trim(),
      target_url:$('#routeTarget').value.trim(),
      method:'POST',
      enabled:true,
      retry_max:5,
      timeout_seconds:10
    })});
    routeForm.reset();
    toast('Route created');
    refresh();
  }catch(ex){toast(ex.message||'Create failed')}
});

const depForm=$('#depForm');
if(depForm)depForm.addEventListener('submit',async e=>{
  e.preventDefault();
  if(!isAdmin())return;
  try{
    await api('/api/v1/deployments',{method:'POST',body:JSON.stringify({
      site_id:$('#depSite').value.trim(),
      name:$('#depName').value.trim(),
      version:$('#depVersion').value.trim(),
      image:$('#depImage').value.trim(),
      desired_state:$('#depDesired').value||'running'
    })});
    depForm.reset();
    toast('Deployment created');
    refresh();
  }catch(ex){toast(ex.message||'Create failed')}
});

(async()=>{
  wireLoginChapters();
  const ok=await ensureSession();
  if(!ok)fillLoginContext();
  const initial=location.hash.slice(1);
  if(['overview','sites','devices','streams','apps','dlq','logs'].includes(initial))page(initial);
  if(ok){await refresh();setInterval(refresh,5000)}
})();
