package main

const indexHTML = `<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>A2A 智能体协作网关</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>
:root{--bg:#f6f7f9;--card:#fff;--line:#e5e6eb;--ink:#1f2329;--muted:#6b7280;--accent:#2563eb;--ok:#16a34a;--bad:#dc2626;--warn:#d97706}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--ink);font:14px/1.6 -apple-system,"Segoe UI","Microsoft YaHei",sans-serif}
header{background:#fff;border-bottom:1px solid var(--line);padding:12px 24px;display:flex;align-items:center;gap:14px}
header h1{font-size:15px;margin:0}
header .sub{font-size:12px;color:var(--muted)}
.wrap{display:grid;grid-template-columns:320px minmax(0,1fr);gap:16px;padding:16px 24px 40px;align-items:start}
.card{background:var(--card);border:1px solid var(--line);border-radius:10px}
.card h2{font-size:13px;margin:0;padding:12px 14px;border-bottom:1px solid var(--line);display:flex;justify-content:space-between;align-items:center}
.card h2 em{font-style:normal;font-size:12px;color:var(--muted);font-weight:400}
.body{padding:14px}
textarea{width:100%;border:1px solid var(--line);border-radius:8px;padding:10px;font:inherit;resize:vertical}
button{font:inherit;padding:7px 14px;border-radius:7px;border:1px solid var(--line);background:#fff;cursor:pointer}
button.primary{background:var(--accent);border-color:var(--accent);color:#fff;font-weight:600}
button:disabled{opacity:.6;cursor:default}
.agent{display:flex;gap:8px;align-items:center;padding:8px 0;border-bottom:1px dashed var(--line);font-size:13px}
.agent:last-child{border-bottom:none}
.dot{width:7px;height:7px;border-radius:50%;background:var(--ok)}
.dot.off{background:var(--bad)}
.task{padding:10px 0;border-bottom:1px solid var(--line);cursor:pointer}
.task:last-child{border-bottom:none}
.task.on{background:#f0f6ff;margin:0 -10px;padding:10px}
.task .t1{font-weight:600;font-size:13px}
.task .t2{font-size:11.5px;color:var(--muted);margin-top:2px}
.st{font-size:11px;padding:1px 7px;border-radius:9px;background:#eef2ff;color:#3730a3}
.st.completed{background:#dcfce7;color:#166534}
.st.failed{background:#fee2e2;color:#991b1b}
.st.working{background:#fef3c7;color:#92400e}
.hop{display:grid;grid-template-columns:22px 90px 1fr 70px;gap:8px;align-items:center;
     padding:7px 0;border-bottom:1px dashed var(--line);font-size:13px}
.hop .seq{color:var(--muted);font-size:11px}
.hop .bar{height:8px;border-radius:4px;background:linear-gradient(90deg,#93c5fd,#2563eb)}
.hop.failed .bar{background:linear-gradient(90deg,#fca5a5,#dc2626)}
.hop.rejected .bar{background:linear-gradient(90deg,#fcd34d,#d97706)}
.hop .ms{text-align:right;color:var(--muted);font-size:12px}
.art{border-left:3px solid var(--accent);background:#f8fbff;padding:10px 12px;border-radius:0 8px 8px 0;margin-bottom:10px}
.art b{font-size:13px}
.art pre{white-space:pre-wrap;font:12.5px/1.7 inherit;margin:6px 0 0;color:#374151}
.hint{font-size:11.5px;color:var(--muted);margin-top:8px}
.row{display:flex;gap:8px;align-items:center;margin-top:10px}
</style></head><body>
<header>
  <h1>A2A 智能体协作网关</h1>
  <span class="sub">一句话 → 拆给多个专职 agent 协作，全程可审计</span>
</header>
<div class="wrap">
  <div>
    <div class="card"><h2>已注册 Agent <em id="agentN">-</em></h2>
      <div class="body" id="agents"></div></div>
    <div class="card" style="margin-top:16px"><h2>任务 <em id="taskN">-</em></h2>
      <div class="body" id="tasks"></div></div>
  </div>
  <div>
    <div class="card">
      <h2>发一句话试试 <em>走 A2A 协议，不是函数调用</em></h2>
      <div class="body">
        <textarea id="txt" rows="3">打印机连不上，整个楼层的文件都打不出来</textarea>
        <div class="row">
          <button class="primary" id="send">发送</button>
          <button id="same">再发一次（同 messageId）</button>
          <button id="crash">模拟网关重启</button>
          <span class="hint" id="msg"></span>
        </div>
      </div>
    </div>
    <div class="card" style="margin-top:16px">
      <h2>任务详情 <em id="tid">未选择</em></h2>
      <div class="body" id="detail"><div class="hint">左侧选一个任务。</div></div>
    </div>
  </div>
</div>
<script>
let cur = null, curTask = null, lastMsgId = null, es = null;
const $ = id => document.getElementById(id);

async function loadAgents() {
  const r = await fetch('/api/agents'); const list = (await r.json()) || [];
  $('agentN').textContent = list.length + ' 个';
  $('agents').innerHTML = list.map(a => ` + "`" + `
    <div class="agent"><span class="dot ${a.healthy ? '' : 'off'}"></span>
      <b>${a.name}</b><span style="color:#6b7280;font-size:12px">${a.url.replace('http://127.0.0.1:',':')}</span>
    </div>` + "`" + `).join('');
}

async function loadTasks() {
  const r = await fetch('/api/tasks'); const list = (await r.json()) || [];
  $('taskN').textContent = list.length + ' 条';
  $('tasks').innerHTML = list.map(t => ` + "`" + `
    <div class="task ${cur === t.id ? 'on' : ''}" data-id="${t.id}">
      <div class="t1">${esc((t.history && t.history[0] ? t.history[0].parts[0].text : t.id)).slice(0, 24)}</div>
      <div class="t2"><span class="st ${t.status.state}">${t.status.state}</span>
        · ${(t.hops||[]).length} 跳 · ${(t.artifacts||[]).length} 个结果</div>
    </div>` + "`" + `).join('') || '<div class="hint">还没有任务</div>';
  document.querySelectorAll('.task').forEach(el => el.onclick = () => show(el.dataset.id));
}

function esc(s) { return String(s || '').replace(/[<>&]/g, c => ({'<':'&lt;','>':'&gt;','&':'&amp;'}[c])); }

async function show(id) {
  cur = id;
  const r = await fetch('/api/tasks/' + id); const t = await r.json();
  curTask = t;
  $('tid').textContent = t.id;
  const hopsArr = t.hops || [], maxMs = Math.max(1, ...hopsArr.map(h => h.ms));
  const hops = hopsArr.map(h => ` + "`" + `
    <div class="hop ${h.state}">
      <span class="seq">${h.seq}</span>
      <span>${h.agent}</span>
      <div><div class="bar" style="width:${Math.max(6, Math.round(h.ms / maxMs * 100))}%"></div>
        <div style="font-size:11px;color:#6b7280;margin-top:2px">${h.skill}${h.note ? ' · ' + esc(h.note) : ''}</div></div>
      <span class="ms">${h.ms}ms</span>
    </div>` + "`" + `).join('') || '<div class="hint">还没有跨 agent 调用</div>';

  const arts = (t.artifacts || []).map(a => ` + "`" + `
    <div class="art"><b>${esc(a.name)}</b> <span style="color:#6b7280;font-size:12px">${esc(a.description || '')}</span>
      <pre>${esc((a.parts || []).filter(p => p.kind === 'text').map(p => p.text).join('\\n'))}</pre>
    </div>` + "`" + `).join('') || '<div class="hint">暂无结果</div>';

  $('detail').innerHTML = ` + "`" + `
    <div style="margin-bottom:10px">
      <span class="st ${t.status.state}">${t.status.state}</span>
      <span style="color:#6b7280;font-size:12.5px;margin-left:8px">${esc(t.status.message ? t.status.message.parts[0].text : '')}</span>
    </div>
    <div style="font-size:12px;color:#6b7280;margin:6px 0 4px">跨 agent 调用（串行执行，条长 = 该跳耗时）</div>
    ${hops}
    <div style="font-size:12px;color:#6b7280;margin:14px 0 4px">产出</div>
    ${arts}` + "`" + `;
  loadTasks();
}

async function send(same) {
  const text = $('txt').value.trim(); if (!text) return;
  const mid = same && lastMsgId ? lastMsgId : 'msg-' + Math.random().toString(16).slice(2, 10);
  lastMsgId = mid;
  $('send').disabled = $('same').disabled = true;
  $('msg').textContent = '已提交 ' + mid;
  const r = await fetch('/api/send', {method:'POST', headers:{'content-type':'application/json'},
    body: JSON.stringify({text, messageId: mid})});
  const data = await r.json();
  $('send').disabled = $('same').disabled = false;
  if (data.duplicated) $('msg').textContent = '幂等命中：直接返回已有任务 ' + data.task.id;
  // SSE 跟着任务状态走
  if (es) es.close();
  es = new EventSource('/api/tasks/' + data.task.id + '/events');
  es.addEventListener('task', ev => { const t = JSON.parse(ev.data); show(t.id); });
  es.addEventListener('end', () => es.close());
  show(data.task.id);
}

$('send').onclick = () => send(false);
$('same').onclick = () => send(true);
$('crash').onclick = async () => {
  $('msg').textContent = '正在重启网关（任务状态已落盘）…';
  await fetch('/api/crash').catch(()=>{});
  setTimeout(() => { location.reload(); }, 900);
};
loadAgents(); loadTasks();
setInterval(loadAgents, 5000);
</script></body></html>`
