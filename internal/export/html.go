package export

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"sort"

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
		label := n.Path
		if n.External {
			label = n.Host
		}
		if label == "" || label == "/" {
			label = n.Host + "/"
		}
		if len(label) > 40 {
			label = label[:18] + "..." + label[len(label)-18:]
		}
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
<title>ReconGraph — %s</title>
<style>
  :root{
    --bg:#0f1115; --panel:#161922; --line:#242938; --fg:#e6e9ef; --dim:#8b93a7;
    --page:#4f8ef7; --script:#f0a020; --style:#a86ef0; --img:#2fb886;
    --api:#f0509a; --ext:#6b7385; --err:#ef4444;
  }
  *{box-sizing:border-box}
  body{margin:0;background:var(--bg);color:var(--fg);
       font:13px/1.5 ui-sans-serif,system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
  header{padding:14px 18px;border-bottom:1px solid var(--line);display:flex;
         gap:18px;align-items:baseline;flex-wrap:wrap}
  h1{font-size:15px;margin:0;font-weight:600;letter-spacing:.01em}
  .meta{color:var(--dim);font-size:12px}
  .wrap{display:flex;height:calc(100vh - 53px);flex-wrap:wrap}
  #cv{flex:1 1 560px;min-width:320px;display:block;cursor:grab}
  #cv:active{cursor:grabbing}
  aside{width:340px;max-width:100%%;border-left:1px solid var(--line);
        overflow-y:auto;padding:14px;background:var(--panel)}
  .tabs{display:flex;gap:4px;margin-bottom:12px;flex-wrap:wrap}
  .tab{padding:4px 10px;border:1px solid var(--line);border-radius:5px;
       cursor:pointer;color:var(--dim);font-size:12px;background:none}
  .tab.on{background:var(--page);border-color:var(--page);color:#fff}
  .row{padding:6px 0;border-bottom:1px solid var(--line);word-break:break-all;font-size:12px}
  .row:last-child{border:0}
  .k{color:var(--dim);font-size:11px;text-transform:uppercase;letter-spacing:.04em}
  .n{color:var(--dim)}
  .legend{display:flex;gap:10px;flex-wrap:wrap;font-size:11px;color:var(--dim)}
  .sw{display:inline-block;width:9px;height:9px;border-radius:50%%;margin-right:4px}
  input[type=search]{width:100%%;padding:6px 8px;margin-bottom:10px;background:var(--bg);
       border:1px solid var(--line);border-radius:5px;color:var(--fg);font-size:12px}
  .empty{color:var(--dim);font-style:italic;padding:10px 0}
  a{color:var(--page)}
  @media (max-width:820px){ .wrap{height:auto} #cv{height:60vh} aside{width:100%%;border-left:0;border-top:1px solid var(--line)} }
</style>
</head><body>
<header>
  <h1>ReconGraph</h1>
  <span class="meta" id="hdr"></span>
  <span class="legend">
    <span><i class="sw" style="background:var(--page)"></i>page</span>
    <span><i class="sw" style="background:var(--script)"></i>script</span>
    <span><i class="sw" style="background:var(--style)"></i>stylesheet</span>
    <span><i class="sw" style="background:var(--img)"></i>image</span>
    <span><i class="sw" style="background:var(--api)"></i>api</span>
    <span><i class="sw" style="background:var(--ext)"></i>external</span>
  </span>
</header>
<div class="wrap">
  <canvas id="cv"></canvas>
  <aside>
    <div class="tabs">
      <button class="tab on" data-t="sel">Selected</button>
      <button class="tab" data-t="orph">Orphans</button>
      <button class="tab" data-t="hosts">Third party</button>
      <button class="tab" data-t="tech">Tech</button>
      <button class="tab" data-t="find">Findings</button>
    </div>
    <div id="panel"></div>
  </aside>
</div>
<script>
const D = %s;
const COLORS = {page:'#4f8ef7',script:'#f0a020',stylesheet:'#a86ef0',image:'#2fb886',
  media:'#22a5c4',api:'#f0509a',form:'#ef4444',document:'#8b93a7',bucket:'#eab308'};
const colorOf = n => n.x ? '#6b7385' : (COLORS[n.k] || '#8b93a7');

document.getElementById('hdr').textContent =
  D.target + ' · ' + D.nodes.length + ' nodes · ' + D.edges.length +
  ' edges · ' + D.status + ' · ' + D.started;

// --- force-directed layout (Fruchterman-Reingold, cooled) -------------------
const N = D.nodes.length;
const labelMin = Math.max(3, (D.nodes.filter(n => n.k==='page').map(n => n.d).sort((a,b) => b-a)[19]) || 0);
const pos = new Float64Array(N*2), vel = new Float64Array(N*2);
for (let i=0;i<N;i++){
  const a = (i/N)*Math.PI*2, r = 120 + (i%%17)*14;
  pos[i*2] = Math.cos(a)*r; pos[i*2+1] = Math.sin(a)*r;
}
const adj = D.edges.map(e => [e.s, e.t]);
let temp = 1.0;

function step(){
  const k = 46;
  for (let i=0;i<N;i++){ vel[i*2]=0; vel[i*2+1]=0; }
  // O(n^2) repulsion. Fine to a few hundred nodes; needs Barnes-Hut above that.
  for (let i=0;i<N;i++){
    for (let j=i+1;j<N;j++){
      let dx = pos[i*2]-pos[j*2], dy = pos[i*2+1]-pos[j*2+1];
      let d2 = dx*dx+dy*dy; if (d2 < 0.01) { dx = Math.random()-0.5; dy = Math.random()-0.5; d2 = 0.01; }
      const f = (k*k)/d2;
      vel[i*2]+=dx*f; vel[i*2+1]+=dy*f; vel[j*2]-=dx*f; vel[j*2+1]-=dy*f;
    }
  }
  for (const [s,t] of adj){
    const dx = pos[s*2]-pos[t*2], dy = pos[s*2+1]-pos[t*2+1];
    const d = Math.sqrt(dx*dx+dy*dy)||0.01, f = (d*d)/k/d;
    vel[s*2]-=dx*f; vel[s*2+1]-=dy*f; vel[t*2]+=dx*f; vel[t*2+1]+=dy*f;
  }
  for (let i=0;i<N;i++){
    let vx=vel[i*2], vy=vel[i*2+1];
    const m = Math.sqrt(vx*vx+vy*vy)||1, lim = Math.min(m, 14*temp);
    pos[i*2] += vx/m*lim; pos[i*2+1] += vy/m*lim;
    pos[i*2] -= pos[i*2]*0.002; pos[i*2+1] -= pos[i*2+1]*0.002;
  }
  temp *= 0.985;
}
for (let i=0;i<(N>400?90:240);i++) step();

// --- rendering --------------------------------------------------------------
const cv = document.getElementById('cv'), ctx = cv.getContext('2d');
let view = {x:0,y:0,z:1}, sel = null, hover = null;

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
function draw(){
  const r = cv.getBoundingClientRect();
  ctx.clearRect(0,0,r.width,r.height);
  ctx.lineWidth = 1;
  placed = [];
  const queue = [];
  for (const e of D.edges){
    const [x1,y1]=toScreen(e.s), [x2,y2]=toScreen(e.t);
    const on = sel!==null && (e.s===sel||e.t===sel);
    ctx.strokeStyle = on ? 'rgba(79,142,247,.85)' : 'rgba(255,255,255,.07)';
    ctx.beginPath(); ctx.moveTo(x1,y1); ctx.lineTo(x2,y2); ctx.stroke();
  }
  for (let i=0;i<N;i++){
    const n = D.nodes[i], [x,y] = toScreen(i);
    const rad = Math.max(3, Math.min(11, 3 + Math.sqrt(n.d)*1.7)) * (sel===i?1.5:1);
    ctx.beginPath(); ctx.arc(x,y,rad,0,Math.PI*2);
    ctx.fillStyle = n.c>=400 ? '#ef4444' : colorOf(n);
    ctx.fill();
    if (sel===i||hover===i){ ctx.strokeStyle='#fff'; ctx.lineWidth=1.5; ctx.stroke(); ctx.lineWidth=1; }
    if (sel===i || hover===i || (n.k==='page' && n.d>=labelMin/Math.max(view.z,0.2))){
      queue.push([n.l, x+rad+3, y+3, sel===i||hover===i, n.d]);
    }
  }
  // Drawn after every node so no circle paints over a label; forced labels
  // (selected, hovered) and busier pages claim space first.
  queue.sort((a,b) => (b[3]-a[3]) || (b[4]-a[4]));
  for (const q of queue) label(q[0], q[1], q[2], q[3]);
}
// Labels are the first thing to turn a large graph into noise, so only the
// best-connected pages get one (more as you zoom in), and a label that would
// overlap one already drawn is skipped.
let placed = [];
function label(text, x, y, force){
  ctx.font='10px ui-sans-serif,system-ui';
  const w = ctx.measureText(text).width, h = 12, box = [x, y-10, x+w, y-10+h];
  if (!force && placed.some(b => box[0]<b[2] && box[2]>b[0] && box[1]<b[3] && box[3]>b[1])) return;
  placed.push(box);
  ctx.fillStyle = force ? '#fff' : 'rgba(230,233,239,.75)';
  ctx.fillText(text, x, y);
}
function pick(mx,my){
  let best=null, bd=225;
  for (let i=0;i<N;i++){
    const [x,y]=toScreen(i), d=(x-mx)**2+(y-my)**2;
    if (d<bd){ bd=d; best=i; }
  }
  return best;
}

let drag=null;
cv.addEventListener('mousedown', e=>{ drag={x:e.clientX,y:e.clientY,vx:view.x,vy:view.y}; });
addEventListener('mouseup', ()=>{ drag=null; });
cv.addEventListener('mousemove', e=>{
  const r=cv.getBoundingClientRect(), mx=e.clientX-r.left, my=e.clientY-r.top;
  if (drag){ view.x=drag.vx+(e.clientX-drag.x); view.y=drag.vy+(e.clientY-drag.y); draw(); return; }
  const h=pick(mx,my);
  if (h!==hover){ hover=h; cv.title = h!==null ? D.nodes[h].u : ''; draw(); }
});
cv.addEventListener('click', e=>{
  const r=cv.getBoundingClientRect();
  sel = pick(e.clientX-r.left, e.clientY-r.top);
  tab='sel'; syncTabs(); render(); draw();
});
cv.addEventListener('wheel', e=>{
  e.preventDefault();
  view.z = Math.max(0.15, Math.min(4, view.z * (e.deltaY<0?1.12:0.89)));
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
      '<div class="row"><div class="k">kind</div>'+esc(n.k)+(n.x?' (external)':'')+'</div>'+
      '<div class="row"><div class="k">status</div>'+(n.c||'—')+'</div>'+
      (n.t?'<div class="row"><div class="k">tech</div>'+esc(n.t)+'</div>':'')+
      '<div class="row"><div class="k">in / out degree</div>'+inn.length+' / '+out.length+'</div>'+
      '<div class="row"><div class="k">links to ('+out.length+')</div>'+
        rows(out.slice(0,60), e=>'<div>'+esc(D.nodes[e.t].u)+' <span class="n">['+esc(e.r)+']</span></div>')+'</div>'+
      '<div class="row"><div class="k">linked from ('+inn.length+')</div>'+
        rows(inn.slice(0,60), e=>'<div>'+esc(D.nodes[e.s].u)+' <span class="n">['+esc(e.r)+']</span></div>')+'</div>';
  } else if (tab==='orph'){
    p.innerHTML = '<div class="k" style="margin-bottom:8px">Pages and endpoints with no inbound link</div>'+
      rows(D.orphans, u=>'<div class="row">'+esc(u)+'</div>');
  } else if (tab==='hosts'){
    p.innerHTML = '<div class="k" style="margin-bottom:8px">Third-party hosts by reference count</div>'+
      rows(D.hosts, h=>'<div class="row">'+esc(h[0])+' <span class="n">· '+h[1]+'</span></div>');
  } else if (tab==='tech'){
    p.innerHTML = '<div class="k" style="margin-bottom:8px">Detected technologies</div>'+
      rows(D.techs, t=>'<div class="row">'+esc(t[0])+' <span class="n">· '+t[1]+' pages</span></div>');
  } else {
    p.innerHTML = '<input type="search" id="q" placeholder="Filter findings...">'+
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
addEventListener('resize', resize);
resize(); render();
</script>
</body></html>
`
