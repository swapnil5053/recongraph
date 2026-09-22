package export

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"sort"
	"strings"

	"github.com/swapnil5053/recongraph/pkg/sitegraph"
)

// Self-contained interactive view: no CDN, no build step, nothing to install.
// Unlike the SVG export it doesn't need Graphviz.

type htmlNode struct {
	ID    int    `json:"id"`
	URL   string `json:"u"`
	Label string `json:"l"`
	Kind  string `json:"k"`
	Host  string `json:"h"`
	Deg   int    `json:"d"`
	Code  int    `json:"c"`
	Ext   bool   `json:"x"`
	Techs string `json:"t,omitempty"`
}

type htmlEdge struct {
	S   int    `json:"s"`
	T   int    `json:"t"`
	Rel string `json:"r"`
}

type htmlPayload struct {
	Target   string      `json:"target"`
	Status   string      `json:"status"`
	Started  string      `json:"started"`
	Nodes    []htmlNode  `json:"nodes"`
	Edges    []htmlEdge  `json:"edges"`
	Findings [][3]string `json:"findings"`
	Orphans  []string    `json:"orphans"`
	Hosts    [][2]any    `json:"hosts"`
	Techs    [][2]any    `json:"techs"`
}

// WriteHTML renders the interactive report.
func WriteHTML(w io.Writer, g *sitegraph.Graph) error {
	p := htmlPayload{
		Target:  g.Target,
		Status:  g.Status,
		Started: g.StartedAt.Format("2006-01-02 15:04 UTC"),
	}

	idx := make(map[sitegraph.NodeID]int, g.NumNodes())
	for i, n := range g.Nodes() {
		idx[n.ID] = i
		label := shortLabel(n)
		p.Nodes = append(p.Nodes, htmlNode{
			ID: i, URL: n.URL, Label: label, Kind: string(n.Kind),
			Host: n.Host, Deg: g.InDegree(n.ID), Code: n.StatusCode,
			Ext: n.External, Techs: techList(n.Techs),
		})
	}
	for _, e := range g.Edges() {
		s, sok := idx[e.Src]
		t, tok := idx[e.Dst]
		if !sok || !tok {
			continue
		}
		p.Edges = append(p.Edges, htmlEdge{S: s, T: t, Rel: string(e.Rel)})
	}
	for _, f := range g.Findings() {
		u := ""
		if n := g.Node(f.NodeID); n != nil {
			u = n.URL
		}
		p.Findings = append(p.Findings, [3]string{f.Kind, f.Value, u})
	}
	for _, o := range g.Orphans() {
		p.Orphans = append(p.Orphans, o.URL)
	}
	for h, c := range g.ExternalHosts() {
		p.Hosts = append(p.Hosts, [2]any{h, c})
	}
	sort.Slice(p.Hosts, func(i, j int) bool {
		return toInt(p.Hosts[i][1]) > toInt(p.Hosts[j][1])
	})
	for t, c := range g.TechSummary() {
		p.Techs = append(p.Techs, [2]any{t, c})
	}
	sort.Slice(p.Techs, func(i, j int) bool {
		return toInt(p.Techs[i][1]) > toInt(p.Techs[j][1])
	})

	data, err := json.Marshal(p)
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(w, htmlTemplate, html.EscapeString(g.Target), data)
	return err
}

// shortLabel names a node the way you'd say it: the last meaningful path
// segment rather than the middle-truncated full path, which on real sites is
// mostly the same prefix repeated.
func shortLabel(n *sitegraph.Node) string {
	if n.External {
		return n.Host
	}
	p := strings.TrimSuffix(n.Path, "index.html")
	if p == "" || p == "/" {
		return n.Host + "/"
	}
	if len(p) <= 32 {
		return p
	}
	trail := strings.HasSuffix(p, "/")
	segs := strings.Split(strings.Trim(p, "/"), "/")
	last := segs[len(segs)-1]
	if len(last) > 30 {
		last = last[:27] + "..."
	}
	if trail {
		last += "/"
	}
	return ".../" + last
}

func toInt(v any) int {
	if i, ok := v.(int); ok {
		return i
	}
	return 0
}

const htmlTemplate = `<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>ReconGraph: %s</title>
<style>
  :root{
    --paper:#E6E8E2; --sheet:#F1F2EE; --ink:#1B1E1C; --dim:#4D534D; --rule:#A3A9A1;
    --mark:#A8321F;
  }
  *{box-sizing:border-box}
  body{margin:0;background:var(--paper);color:var(--ink);
       font:13px/1.5 ui-sans-serif,system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
  header{padding:12px 16px;border-bottom:1px solid var(--ink);display:flex;
         gap:8px 20px;align-items:center;flex-wrap:wrap;background:var(--sheet)}
  h1{font-size:14px;margin:0;font-weight:700;letter-spacing:.06em;text-transform:uppercase}
  .meta{color:var(--dim);font-size:12px;font-variant-numeric:tabular-nums}
  .kinds{display:flex;gap:6px;flex-wrap:wrap;margin-left:auto}
  .kinds button{font:12px/1 inherit;font-family:inherit;display:flex;align-items:center;gap:6px;
       padding:5px 9px;border:1px solid var(--rule);background:var(--sheet);color:var(--dim);cursor:pointer}
  .kinds button[aria-pressed=true]{border-color:var(--ink);color:var(--ink)}
  .kinds button[aria-pressed=false] .sw{opacity:.25}
  .kinds .n{font-variant-numeric:tabular-nums;color:var(--dim)}
  .sw{display:inline-block;width:9px;height:9px;border-radius:50%%}
  .wrap{display:flex;height:calc(100vh - 51px);flex-wrap:wrap}
  #cv{flex:1 1 560px;min-width:300px;display:block;cursor:grab;background:var(--sheet)}
  #cv:active{cursor:grabbing}
  aside{width:340px;max-width:100%%;border-left:1px solid var(--ink);
        overflow-y:auto;padding:14px;background:var(--paper)}
  .tabs{display:flex;gap:4px;margin-bottom:12px;flex-wrap:wrap}
  .tab{padding:4px 10px;border:1px solid var(--rule);cursor:pointer;color:var(--dim);
       font:12px inherit;font-family:inherit;background:var(--sheet)}
  .tab.on{background:var(--ink);border-color:var(--ink);color:var(--sheet)}
  .row{padding:6px 0;border-bottom:1px solid var(--rule);word-break:break-all;font-size:12px}
  .row:last-child{border:0}
  .k{color:var(--dim);font-size:11px;text-transform:uppercase;letter-spacing:.05em}
  .n{color:var(--dim)}
  input[type=search]{width:100%%;padding:6px 8px;margin-bottom:10px;background:var(--sheet);
       border:1px solid var(--rule);color:var(--ink);font:12px inherit;font-family:inherit}
  .empty{color:var(--dim);font-style:italic;padding:10px 0}
  .hint{color:var(--dim);font-size:11px;margin-top:14px}
  a{color:var(--ink)}
  :focus-visible{outline:2px solid var(--mark);outline-offset:1px}
  @media (max-width:820px){ .wrap{height:auto} #cv{height:62vh} aside{width:100%%;border-left:0;border-top:1px solid var(--ink)} .kinds{margin-left:0} }
</style>
</head><body>
<header>
  <h1>ReconGraph</h1>
  <span class="meta" id="hdr"></span>
  <div class="kinds" id="kinds" aria-label="Show node kinds"></div>
</header>
<div class="wrap">
  <canvas id="cv" aria-label="Site graph"></canvas>
  <aside>
    <div class="tabs">
      <button class="tab on" data-t="sel">Selected</button>
      <button class="tab" data-t="orph">Orphans</button>
      <button class="tab" data-t="hosts">Third party</button>
      <button class="tab" data-t="tech">Tech</button>
      <button class="tab" data-t="find">Findings</button>
    </div>
    <div id="panel"></div>
    <p class="hint">Drag to pan, scroll to zoom, click a node for its links. Hollow circles were seen but not fetched.</p>
  </aside>
</div>
<script>
const D = %s;

// Kinds are grouped into the few a person actually filters by. Colours are
// muted and far enough apart to tell on a light ground; red is kept for API
// endpoints and error responses.
const GROUPS = [
  {id:'page',  label:'pages',       color:'#2F5D8C', match:n=>!n.x && (n.k==='page'||n.k==='document'||n.k==='form'||n.k==='other')},
  {id:'api',   label:'endpoints',   color:'#A8321F', match:n=>!n.x && n.k==='api'},
  {id:'script',label:'scripts',     color:'#B7791F', match:n=>!n.x && n.k==='script'},
  {id:'style', label:'stylesheets', color:'#7A5195', match:n=>!n.x && n.k==='stylesheet'},
  {id:'image', label:'images',      color:'#3D8B6E', match:n=>!n.x && (n.k==='image'||n.k==='media'||n.k==='bucket')},
  {id:'ext',   label:'third party', color:'#6E746D', match:n=>n.x},
];
const N = D.nodes.length;
const groupOf = new Array(N);
for (let i=0;i<N;i++){ groupOf[i] = GROUPS.findIndex(g=>g.match(D.nodes[i])); if (groupOf[i]<0) groupOf[i]=0; }
const counts = GROUPS.map((g,gi)=>groupOf.filter(x=>x===gi).length);
// On a big crawl, images and stylesheets outnumber pages and every page links
// the same ones, so they start hidden.
const shown = GROUPS.map(g => !(N > 250 && (g.id==='image' || g.id==='style')));
// Same for outbound links to other sites when there are more of them than
// pages; the Third party tab still lists every host.
const gi = id => GROUPS.findIndex(g=>g.id===id);
if (counts[gi('ext')] > 150 && counts[gi('ext')] > counts[gi('page')]) shown[gi('ext')] = false;
// Pages seen but not fetched (over the page budget) can outnumber the ones
// that were; they form a ring around the real crawl and hide it.
const isPageLike = i => groupOf[i]===0 || groupOf[i]===1;
const unfetched = D.nodes.filter((n,i)=>isPageLike(i) && n.c===0).length;
const fetchedCount = D.nodes.filter((n,i)=>isPageLike(i) && n.c!==0).length;
let showUnfetched = !(unfetched > 100 && unfetched > fetchedCount);

document.getElementById('hdr').textContent =
  D.target + ' | ' + N + ' nodes, ' + D.edges.length + ' edges | ' + D.status + ' | ' + D.started;

const kinds = document.getElementById('kinds');
GROUPS.forEach((g,gi)=>{
  if (!counts[gi]) return;
  const b = document.createElement('button');
  b.type='button'; b.setAttribute('aria-pressed', shown[gi]);
  b.innerHTML = '<i class="sw" style="background:'+g.color+'"></i>'+g.label+' <span class="n">'+counts[gi]+'</span>';
  b.onclick = ()=>{ shown[gi]=!shown[gi]; b.setAttribute('aria-pressed', shown[gi]); relayout(); };
  kinds.appendChild(b);
});
if (unfetched){
  const b = document.createElement('button');
  b.type='button'; b.setAttribute('aria-pressed', showUnfetched);
  b.innerHTML = '<i class="sw" style="background:none;border:1.5px solid #4D534D"></i>not fetched <span class="n">'+unfetched+'</span>';
  b.onclick = ()=>{ showUnfetched=!showUnfetched; b.setAttribute('aria-pressed', showUnfetched); relayout(); };
  kinds.appendChild(b);
}

// --- force-directed layout over the visible nodes -------------------------
const pos = new Float64Array(N*2), vel = new Float64Array(N*2);
for (let i=0;i<N;i++){
  const a = (i/N)*Math.PI*2, r = 120 + (i%%17)*14;
  pos[i*2] = Math.cos(a)*r; pos[i*2+1] = Math.sin(a)*r;
}
let vis = [], visEdges = [], isVis = new Uint8Array(N);
function computeVisible(){
  vis = []; isVis.fill(0);
  for (let i=0;i<N;i++){
    const n = D.nodes[i];
    if (!shown[groupOf[i]]) continue;
    if (!showUnfetched && isPageLike(i) && n.c===0) continue;
    vis.push(i); isVis[i]=1;
  }
  visEdges = D.edges.filter(e=>isVis[e.s] && isVis[e.t]);
  linked.fill(0);
  for (const e of visEdges){ linked[e.s]=1; linked[e.t]=1; }
}
const linked = new Uint8Array(N);
function step(temp){
  const k = 42;
  for (const i of vis){ vel[i*2]=0; vel[i*2+1]=0; }
  // O(n^2) repulsion over visible nodes; fine into the low thousands.
  for (let a=0;a<vis.length;a++){
    const i = vis[a];
    for (let b=a+1;b<vis.length;b++){
      const j = vis[b];
      let dx = pos[i*2]-pos[j*2], dy = pos[i*2+1]-pos[j*2+1];
      let d2 = dx*dx+dy*dy; if (d2 < 0.01) { dx = Math.random()-0.5; dy = Math.random()-0.5; d2 = 0.01; }
      const f = (k*k)/d2;
      vel[i*2]+=dx*f; vel[i*2+1]+=dy*f; vel[j*2]-=dx*f; vel[j*2+1]-=dy*f;
    }
  }
  for (const e of visEdges){
    const s=e.s, t=e.t, dx = pos[s*2]-pos[t*2], dy = pos[s*2+1]-pos[t*2+1];
    const d = Math.sqrt(dx*dx+dy*dy)||0.01, f = d/k;
    vel[s*2]-=dx*f; vel[s*2+1]-=dy*f; vel[t*2]+=dx*f; vel[t*2+1]+=dy*f;
  }
  for (const i of vis){
    const vx=vel[i*2], vy=vel[i*2+1], m = Math.sqrt(vx*vx+vy*vy)||1, lim = Math.min(m, 14*temp);
    pos[i*2] += vx/m*lim - pos[i*2]*0.004; pos[i*2+1] += vy/m*lim - pos[i*2+1]*0.004;
  }
}
function relayout(){
  computeVisible();
  // Nodes with no visible edges (pages found only in a sitemap, say) take no
  // part in the force layout; left in, they drift away and shrink the view.
  // They're parked in rows under the graph instead.
  const loose = vis.filter(i=>!linked[i]);
  vis = vis.filter(i=>linked[i]);
  let temp = 1, steps = vis.length > 600 ? 120 : 260;
  for (let s=0;s<steps;s++){ step(temp); temp *= 0.985; }
  park(loose);
  vis = vis.concat(loose);
  fit(); draw();
}
let parked = [];
function park(loose){
  parked = loose;
  if (!loose.length) return;
  let x0=-100,x1=100,y1=0;
  if (vis.length){
    x0=Infinity; x1=-Infinity; y1=-Infinity;
    for (const i of vis){ x0=Math.min(x0,pos[i*2]); x1=Math.max(x1,pos[i*2]); y1=Math.max(y1,pos[i*2+1]); }
  }
  const gap = 22, perRow = Math.max(1, Math.floor((x1-x0)/gap)+1);
  loose.forEach((i,k)=>{
    pos[i*2] = x0 + (k %% perRow)*gap;
    pos[i*2+1] = y1 + 60 + Math.floor(k/perRow)*gap;
  });
}

// --- rendering --------------------------------------------------------------
const cv = document.getElementById('cv'), ctx = cv.getContext('2d');
let view = {x:0,y:0,z:1}, sel = null, hover = null, placed = [], labelMin = 3;

function fit(){
  if (!vis.length) return;
  let x0=Infinity,y0=Infinity,x1=-Infinity,y1=-Infinity;
  for (const i of vis){ x0=Math.min(x0,pos[i*2]); x1=Math.max(x1,pos[i*2]); y0=Math.min(y0,pos[i*2+1]); y1=Math.max(y1,pos[i*2+1]); }
  const r = cv.getBoundingClientRect(), pad = 40;
  view.z = Math.min(3, Math.max(0.1, Math.min((r.width-2*pad)/((x1-x0)||1), (r.height-2*pad)/((y1-y0)||1))));
  view.x = -((x0+x1)/2)*view.z; view.y = -((y0+y1)/2)*view.z;
  const degs = vis.filter(i=>groupOf[i]===0).map(i=>D.nodes[i].d).sort((a,b)=>b-a);
  labelMin = Math.max(3, degs[14] || 0);
}
function resize(){
  const r = cv.getBoundingClientRect(), dpr = devicePixelRatio||1;
  cv.width = r.width*dpr; cv.height = r.height*dpr;
  ctx.setTransform(dpr,0,0,dpr,0,0);
  draw();
}
function toScreen(i){
  const r = cv.getBoundingClientRect();
  return [pos[i*2]*view.z + r.width/2 + view.x, pos[i*2+1]*view.z + r.height/2 + view.y];
}
function radius(i){
  return Math.max(2.5, Math.min(10, 2.5 + Math.sqrt(D.nodes[i].d)*1.3));
}
function draw(){
  const r = cv.getBoundingClientRect();
  ctx.clearRect(0,0,r.width,r.height);
  placed = [];
  ctx.lineWidth = 1;
  ctx.strokeStyle = 'rgba(27,30,28,.10)';
  ctx.beginPath();
  for (const e of visEdges){
    if (sel!==null && (e.s===sel||e.t===sel)) continue;
    const [x1,y1]=toScreen(e.s), [x2,y2]=toScreen(e.t);
    ctx.moveTo(x1,y1); ctx.lineTo(x2,y2);
  }
  ctx.stroke();
  if (sel!==null){
    ctx.strokeStyle = '#A8321F'; ctx.lineWidth = 1.4; ctx.beginPath();
    for (const e of visEdges){
      if (e.s!==sel && e.t!==sel) continue;
      const [x1,y1]=toScreen(e.s), [x2,y2]=toScreen(e.t);
      ctx.moveTo(x1,y1); ctx.lineTo(x2,y2);
    }
    ctx.stroke(); ctx.lineWidth = 1;
  }
  const queue = [];
  for (const i of vis){
    const n = D.nodes[i], [x,y] = toScreen(i), rad = radius(i) * (sel===i?1.6:1);
    const color = n.c>=400 ? '#A8321F' : GROUPS[groupOf[i]].color;
    ctx.beginPath(); ctx.arc(x,y,rad,0,Math.PI*2);
    if (!n.x && n.c===0) { ctx.fillStyle='#F1F2EE'; ctx.fill(); ctx.strokeStyle=color; ctx.lineWidth=1.2; ctx.stroke(); ctx.lineWidth=1; }
    else { ctx.fillStyle=color; ctx.fill(); }
    if (sel===i||hover===i){ ctx.strokeStyle='#1B1E1C'; ctx.lineWidth=1.5; ctx.stroke(); ctx.lineWidth=1; }
    const busy = groupOf[i]===0 && n.d >= labelMin/Math.max(view.z,0.2);
    if (sel===i || hover===i || busy) queue.push([n.l, x+rad+4, y+4, sel===i||hover===i, n.d]);
  }
  // Labels go on last so nodes never cover them. Selected and hovered first,
  // then the busiest pages; anything that would overlap is dropped.
  queue.sort((a,b) => (b[3]-a[3]) || (b[4]-a[4]));
  if (parked.length){
    const [px,py] = toScreen(parked[0]);
    ctx.font = '11px ui-sans-serif,system-ui,sans-serif'; ctx.fillStyle = '#4D534D';
    ctx.fillText('No links to or from these ('+parked.length+')', px-4, py-14);
  }
  for (const q of queue) label(q[0], q[1], q[2], q[3]);
}
function label(text, x, y, force){
  ctx.font = (force?'600 ':'')+'11px ui-sans-serif,system-ui,sans-serif';
  const w = ctx.measureText(text).width, box = [x-2, y-11, x+w+2, y+3];
  if (!force && placed.some(b => box[0]<b[2] && box[2]>b[0] && box[1]<b[3] && box[3]>b[1])) return;
  placed.push(box);
  ctx.fillStyle = 'rgba(241,242,238,.85)'; ctx.fillRect(box[0], box[1], box[2]-box[0], box[3]-box[1]);
  ctx.fillStyle = '#1B1E1C'; ctx.fillText(text, x, y);
}
function pick(mx,my){
  let best=null, bd=225;
  for (const i of vis){
    const [x,y]=toScreen(i), d=(x-mx)**2+(y-my)**2;
    if (d<bd){ bd=d; best=i; }
  }
  return best;
}

let drag=null;
cv.addEventListener('mousedown', e=>{ drag={x:e.clientX,y:e.clientY,vx:view.x,vy:view.y,moved:false}; });
addEventListener('mouseup', ()=>{ setTimeout(()=>{ drag=null; }, 0); });
cv.addEventListener('mousemove', e=>{
  const r=cv.getBoundingClientRect(), mx=e.clientX-r.left, my=e.clientY-r.top;
  if (drag){ drag.moved = true; view.x=drag.vx+(e.clientX-drag.x); view.y=drag.vy+(e.clientY-drag.y); draw(); return; }
  const h=pick(mx,my);
  if (h!==hover){ hover=h; cv.title = h!==null ? D.nodes[h].u : ''; draw(); }
});
cv.addEventListener('click', e=>{
  if (drag && drag.moved) return;
  const r=cv.getBoundingClientRect();
  sel = pick(e.clientX-r.left, e.clientY-r.top);
  tab='sel'; syncTabs(); render(); draw();
});
cv.addEventListener('wheel', e=>{
  e.preventDefault();
  const r=cv.getBoundingClientRect(), mx=e.clientX-r.left-r.width/2, my=e.clientY-r.top-r.height/2;
  const f = e.deltaY<0 ? 1.12 : 0.89, z = Math.max(0.05, Math.min(6, view.z*f)), k = z/view.z;
  view.x = mx - (mx-view.x)*k; view.y = my - (my-view.y)*k; view.z = z;
  draw();
}, {passive:false});

// --- side panel -------------------------------------------------------------
let tab='sel';
function syncTabs(){
  document.querySelectorAll('.tab').forEach(b=>b.classList.toggle('on', b.dataset.t===tab));
}
document.querySelectorAll('.tab').forEach(b=>{
  b.onclick = ()=>{ tab=b.dataset.t; syncTabs(); render(); };
});
const esc = s => String(s).replace(/[&<>"]/g, c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]));
function rows(list, fn){
  if(!list || !list.length) return '<div class="empty">Nothing here.</div>';
  return list.map(fn).join('');
}
function render(){
  const p = document.getElementById('panel');
  if (tab==='sel'){
    if (sel===null){ p.innerHTML='<div class="empty">Click a node in the graph.</div>'; return; }
    const n = D.nodes[sel];
    const out = D.edges.filter(e=>e.s===sel), inn = D.edges.filter(e=>e.t===sel);
    p.innerHTML =
      '<div class="row"><div class="k">url</div><a href="'+esc(n.u)+'" target="_blank" rel="noreferrer">'+esc(n.u)+'</a></div>'+
      '<div class="row"><div class="k">kind</div>'+esc(n.k)+(n.x?' (third party)':'')+'</div>'+
      '<div class="row"><div class="k">status</div>'+(n.c||'not fetched')+'</div>'+
      (n.t?'<div class="row"><div class="k">tech</div>'+esc(n.t)+'</div>':'')+
      '<div class="row"><div class="k">in / out</div>'+inn.length+' / '+out.length+'</div>'+
      '<div class="row"><div class="k">links to ('+out.length+')</div>'+
        rows(out.slice(0,60), e=>'<div>'+esc(D.nodes[e.t].u)+' <span class="n">['+esc(e.r)+']</span></div>')+'</div>'+
      '<div class="row"><div class="k">linked from ('+inn.length+')</div>'+
        rows(inn.slice(0,60), e=>'<div>'+esc(D.nodes[e.s].u)+' <span class="n">['+esc(e.r)+']</span></div>')+'</div>';
  } else if (tab==='orph'){
    p.innerHTML = '<div class="k" style="margin-bottom:8px">Fetched pages with no inbound link</div>'+
      rows(D.orphans, u=>'<div class="row">'+esc(u)+'</div>');
  } else if (tab==='hosts'){
    p.innerHTML = '<div class="k" style="margin-bottom:8px">Third-party hosts by reference count</div>'+
      rows(D.hosts, h=>'<div class="row">'+esc(h[0])+' <span class="n">'+h[1]+'</span></div>');
  } else if (tab==='tech'){
    p.innerHTML = '<div class="k" style="margin-bottom:8px">Detected technologies</div>'+
      rows(D.techs, t=>'<div class="row">'+esc(t[0])+' <span class="n">'+t[1]+' pages</span></div>');
  } else {
    p.innerHTML = '<input type="search" id="q" placeholder="Filter findings">'+
      '<div id="fl">'+rows(D.findings, f=>
        '<div class="row"><div class="k">'+esc(f[0])+'</div>'+esc(f[1])+
        '<div class="n">'+esc(f[2])+'</div></div>')+'</div>';
    const q = document.getElementById('q');
    if (q) q.oninput = ()=>{
      const v = q.value.toLowerCase();
      document.getElementById('fl').innerHTML = rows(
        D.findings.filter(f=>(f[0]+f[1]+f[2]).toLowerCase().includes(v)),
        f=>'<div class="row"><div class="k">'+esc(f[0])+'</div>'+esc(f[1])+
           '<div class="n">'+esc(f[2])+'</div></div>');
    };
  }
}
addEventListener('resize', ()=>{ resize(); });
resize(); relayout(); render();
</script>
</body></html>
`
