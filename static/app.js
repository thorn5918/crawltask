/* PyScheduler Go · 前端单页应用（原生 JS，无构建依赖） */
'use strict';

/* ================= 工具 ================= */

const $ = (sel, el) => (el || document).querySelector(sel);
const $$ = (sel, el) => Array.from((el || document).querySelectorAll(sel));

function esc(s) {
  return String(s == null ? '' : s)
    .replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;').replaceAll("'", '&#39;');
}

async function api(method, url, body) {
  const opt = { method, headers: {} };
  if (body !== undefined) {
    opt.headers['Content-Type'] = 'application/json';
    opt.body = JSON.stringify(body);
  }
  const resp = await fetch(url, opt);
  let data = null;
  try { data = await resp.json(); } catch (e) { /* ignore */ }
  if (!resp.ok) {
    throw new Error((data && data.error) ? data.error : 'HTTP ' + resp.status);
  }
  return data;
}

function toast(msg, type) {
  const el = document.createElement('div');
  el.className = 'toast ' + (type || '');
  el.textContent = msg;
  $('#toasts').appendChild(el);
  setTimeout(() => { el.style.opacity = '0'; el.style.transition = 'opacity .3s'; setTimeout(() => el.remove(), 320); }, 3200);
}

function openModal(opts) {
  const root = $('#modal-root');
  const mask = document.createElement('div');
  mask.className = 'modal-mask';
  mask.innerHTML = `
    <div class="modal${opts.wide ? ' wide' : ''}">
      <div class="modal-header"><h3>${esc(opts.title || '')}</h3><button class="modal-close" title="关闭">✕</button></div>
      <div class="modal-body"></div>
      ${opts.footer === null ? '' : '<div class="modal-footer"></div>'}
    </div>`;
  const bodyEl = $('.modal-body', mask);
  if (typeof opts.body === 'string') bodyEl.innerHTML = opts.body;
  else if (opts.body) bodyEl.appendChild(opts.body);
  const footerEl = $('.modal-footer', mask);
  const close = () => {
    mask.remove();
    if (opts.onClose) opts.onClose();
  };
  $('.modal-close', mask).onclick = close;
  mask.addEventListener('mousedown', e => { if (e.target === mask) close(); });
  root.appendChild(mask);
  return { el: mask, body: bodyEl, footer: footerEl, close };
}

function confirmDialog(text, title) {
  return new Promise(resolve => {
    const m = openModal({ title: title || '确认操作', body: `<div style="padding:2px 0">${esc(text)}</div>` });
    const no = document.createElement('button');
    no.className = 'btn'; no.textContent = '取消';
    const yes = document.createElement('button');
    yes.className = 'btn primary'; yes.textContent = '确定';
    no.onclick = () => { m.close(); resolve(false); };
    yes.onclick = () => { m.close(); resolve(true); };
    m.footer.append(no, yes);
  });
}

function fmtBytes(n) {
  if (n == null) return '-';
  if (n >= 1 << 30) return (n / (1 << 30)).toFixed(2) + ' GB';
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + ' MB';
  if (n >= 1 << 10) return (n / (1 << 10)).toFixed(1) + ' KB';
  return n + ' B';
}

function fmtDur(ms) {
  if (ms == null) return '-';
  if (ms < 1000) return ms + ' ms';
  if (ms < 60000) return (ms / 1000).toFixed(1) + ' 秒';
  const s = Math.floor(ms / 1000);
  const m = Math.floor(s / 60), h = Math.floor(m / 60);
  if (h > 0) return `${h} 时 ${m % 60} 分 ${s % 60} 秒`;
  return `${m} 分 ${s % 60} 秒`;
}

const STATUS_TEXT = { running: '运行中', success: '成功', failed: '失败', killed: '已终止' };
function statusBadge(s) {
  const cls = STATUS_TEXT[s] ? s : 'gray';
  return `<span class="badge ${cls}">${esc(STATUS_TEXT[s] || s || '-')}</span>`;
}
const ENV_STATUS = { creating: ['running', '创建中'], ready: ['success', '就绪'], error: ['failed', '失败'] };
function envStatusBadge(s) {
  const m = ENV_STATUS[s] || ['gray', s || '-'];
  return `<span class="badge ${m[0]}">${esc(m[1])}</span>`;
}
function triggerText(t) { return t === 'schedule' ? '调度' : '手动'; }

function debounce(fn, ms) {
  let h;
  return function (...args) { clearTimeout(h); h = setTimeout(() => fn.apply(this, args), ms); };
}

/* ================= 路由 ================= */

const PAGES = {
  dashboard: { title: '仪表盘', render: renderDashboard },
  tasks: { title: '任务管理', render: renderTasks },
  projects: { title: '项目管理', render: renderProjects },
  envs: { title: '环境管理', render: renderEnvs },
  logs: { title: '运行日志', render: renderLogs },
  settings: { title: '通知设置', render: renderSettings },
};

let pageCleanup = null;

function route() {
  const h = (location.hash || '#/dashboard').replace(/^#\/?/, '');
  const page = PAGES[h] ? h : 'dashboard';
  $$('#nav a').forEach(a => a.classList.toggle('active', a.dataset.page === page));
  $('#page-title').textContent = PAGES[page].title;
  if (pageCleanup) { pageCleanup(); pageCleanup = null; }
  PAGES[page].render($('#page'));
}

window.addEventListener('hashchange', route);

/* 全局：运行中实例指示 */
setInterval(async () => {
  try {
    const d = await api('GET', '/api/runs?status=running&page_size=1');
    const el = $('#running-indicator');
    if (el) {
      el.textContent = d.total ? `● ${d.total} 个实例运行中` : '';
      el.classList.toggle('has', !!d.total);
    }
  } catch (e) { /* ignore */ }
}, 6000);

/* ================= 仪表盘 ================= */

async function renderDashboard(root) {
  root.innerHTML = `
    <div class="stat-grid" id="dash-stats"></div>
    <div class="card">
      <div class="card-title">近 7 天执行统计<span class="sub">绿色成功 · 红色失败（含被终止）</span></div>
      <div class="chart-wrap"><canvas id="dash-chart"></canvas></div>
      <div class="chart-legend">
        <span><span class="dot" style="background:#2ea44f"></span>成功</span>
        <span><span class="dot" style="background:#e5484d"></span>失败</span>
      </div>
    </div>`;

  async function refreshStats() {
    try {
      const d = await api('GET', '/api/dashboard/overview');
      const t = d.tasks || {};
      const rate = t.rate_24h < 0 ? '—' : t.rate_24h.toFixed(1) + '%';
      $('#dash-stats').innerHTML = `
        <div class="stat-card">
          <div class="stat-label">CPU 使用率</div>
          <div class="stat-value">${(d.cpu_percent || 0).toFixed(1)}<small>%</small></div>
          <div class="progress ${pctCls(d.cpu_percent)}"><div style="width:${Math.min(100, d.cpu_percent || 0)}%"></div></div>
        </div>
        <div class="stat-card">
          <div class="stat-label">内存</div>
          <div class="stat-value">${(d.mem.percent || 0).toFixed(1)}<small>%</small></div>
          <div class="stat-extra">${fmtBytes(d.mem.used)} / ${fmtBytes(d.mem.total)}</div>
          <div class="progress ${pctCls(d.mem.percent)}"><div style="width:${Math.min(100, d.mem.percent || 0)}%"></div></div>
        </div>
        <div class="stat-card">
          <div class="stat-label">磁盘（数据盘）</div>
          <div class="stat-value">${(d.disk.percent || 0).toFixed(1)}<small>%</small></div>
          <div class="stat-extra">${fmtBytes(d.disk.used)} / ${fmtBytes(d.disk.total)}</div>
          <div class="progress ${pctCls(d.disk.percent)}"><div style="width:${Math.min(100, d.disk.percent || 0)}%"></div></div>
        </div>
        <div class="stat-card">
          <div class="stat-label">任务概况</div>
          <div class="stat-value">${t.active || 0}<small> / ${t.total || 0} 活跃</small></div>
          <div class="stat-extra">运行中实例 ${t.running || 0} · 异常任务 ${d.failed_tasks || 0}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">今日执行</div>
          <div class="stat-value" style="color:var(--success)">${t.today_ok || 0}<small> 成功</small></div>
          <div class="stat-extra" style="color:var(--danger)">失败 ${t.today_fail || 0} 次</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">24 小时成功率</div>
          <div class="stat-value">${rate}</div>
          <div class="stat-extra">仅统计已结束的运行</div>
        </div>`;
    } catch (e) { /* ignore */ }
  }

  async function refreshChart() {
    try {
      const d = await api('GET', '/api/dashboard/chart?days=7');
      drawChart($('#dash-chart'), d.days || []);
    } catch (e) { /* ignore */ }
  }

  await refreshStats();
  await refreshChart();
  const t1 = setInterval(refreshStats, 5000);
  const t2 = setInterval(refreshChart, 60000);
  pageCleanup = () => { clearInterval(t1); clearInterval(t2); };
}

function pctCls(p) {
  if (p >= 90) return 'danger';
  if (p >= 75) return 'warn';
  return '';
}

function drawChart(canvas, days) {
  if (!canvas || !days.length) return;
  const dpr = window.devicePixelRatio || 1;
  const rect = canvas.getBoundingClientRect();
  if (rect.width === 0) return;
  canvas.width = rect.width * dpr;
  canvas.height = rect.height * dpr;
  const ctx = canvas.getContext('2d');
  ctx.scale(dpr, dpr);
  const W = rect.width, H = rect.height;
  ctx.clearRect(0, 0, W, H);
  const pad = { l: 38, r: 12, t: 14, b: 26 };
  const max = Math.max(1, ...days.map(d => d.success + d.failed));
  const iw = (W - pad.l - pad.r) / days.length;
  ctx.strokeStyle = '#eef1f5';
  ctx.fillStyle = '#909399';
  ctx.font = '10px sans-serif';
  ctx.textAlign = 'left';
  for (let i = 0; i <= 4; i++) {
    const y = pad.t + (H - pad.t - pad.b) * i / 4;
    ctx.beginPath(); ctx.moveTo(pad.l, y); ctx.lineTo(W - pad.r, y); ctx.stroke();
    ctx.fillText(String(Math.round(max * (1 - i / 4))), 6, y + 3);
  }
  const bw = Math.min(24, iw / 3.2);
  days.forEach((d, i) => {
    const cx = pad.l + iw * i + iw / 2;
    const hAll = H - pad.t - pad.b;
    const hS = hAll * d.success / max;
    const hF = hAll * d.failed / max;
    if (d.success > 0) { ctx.fillStyle = '#2ea44f'; ctx.fillRect(cx - bw - 2, H - pad.b - hS, bw, hS); }
    if (d.failed > 0) { ctx.fillStyle = '#e5484d'; ctx.fillRect(cx + 2, H - pad.b - hF, bw, hF); }
    ctx.fillStyle = '#606266';
    ctx.font = '11px sans-serif';
    ctx.textAlign = 'center';
    ctx.fillText(d.date, cx, H - 8);
    ctx.textAlign = 'left';
  });
}

/* ================= 任务管理 ================= */

async function renderTasks(root) {
  root.innerHTML = `
    <div class="card">
      <div class="toolbar">
        <input type="text" id="task-q" placeholder="搜索名称 / 命令 / 标签…" style="width:240px">
        <span class="spacer"></span>
        <button class="btn primary" id="btn-new-task">+ 新建任务</button>
      </div>
      <div style="overflow-x:auto">
        <table class="tbl">
          <thead><tr>
            <th style="min-width:150px">任务</th><th style="min-width:160px">调度 / 项目</th><th>环境</th>
            <th>状态</th><th>最近运行</th><th style="width:255px">操作</th>
          </tr></thead>
          <tbody id="task-tbody"><tr><td colspan="6" class="tbl-empty">加载中…</td></tr></tbody>
        </table>
      </div>
    </div>`;

  let allTasks = [];

  async function load() {
    try {
      const d = await api('GET', '/api/tasks');
      allTasks = d.tasks || [];
      renderRows();
    } catch (e) {
      $('#task-tbody').innerHTML = `<tr><td colspan="6" class="tbl-empty">加载失败：${esc(e.message)}</td></tr>`;
    }
  }

  function renderRows() {
    const q = $('#task-q').value.trim().toLowerCase();
    const list = allTasks.filter(t =>
      !q || (t.name + ' ' + t.command + ' ' + t.tags + ' ' + (t.project || '')).toLowerCase().includes(q));
    if (!list.length) {
      $('#task-tbody').innerHTML = '<tr><td colspan="6" class="tbl-empty">暂无任务，点击右上角新建</td></tr>';
      return;
    }
    $('#task-tbody').innerHTML = list.map(t => {
      const tags = (t.tags || '').split(/[,,]/).map(s => s.trim()).filter(Boolean)
        .map(s => `<span class="tag">${esc(s)}</span>`).join('');
      const last = t.last_status
        ? `${statusBadge(t.last_status)}<div class="muted">${esc(t.last_end_time || '')} · ${fmtDur(t.last_duration_ms)}</div>`
        : '<span class="muted">从未运行</span>';
      const running = t.running > 0;
      return `<tr>
        <td><b>${esc(t.name)}</b>${tags ? '<div>' + tags + '</div>' : ''}<div class="muted mono">${esc(t.command)}</div></td>
        <td><div>${esc(t.schedule_desc)}</div><div class="muted">${t.project ? '项目：' + esc(t.project) + (t.workdir ? ' / ' + esc(t.workdir) : '') : '无项目'}</div></td>
        <td>${t.env_name ? esc(t.env_name) : '<span class="muted">系统环境</span>'}</td>
        <td>
          <label class="switch"><input type="checkbox" data-toggle="${t.id}" ${t.enabled ? 'checked' : ''}></label>
          <div class="mt8">${running ? `<span class="badge running">运行中 ×${t.running}</span>` : statusBadge(t.last_status)}</div>
        </td>
        <td>${last}</td>
        <td><div class="actions">
          <button class="btn sm" data-run="${t.id}" ${t.running >= (t.max_concurrent || 1) ? 'disabled' : ''}>执行</button>
          <button class="btn sm" data-kill="${t.id}" ${running ? '' : 'disabled'}>终止</button>
          <button class="btn sm" data-hist="${t.id}">历史</button>
          <button class="btn sm" data-edit="${t.id}">编辑</button>
          <button class="btn sm danger" data-del="${t.id}">删除</button>
        </div></td>
      </tr>`;
    }).join('');

    $$('#task-tbody [data-run]').forEach(b => b.onclick = async () => {
      try {
        const d = await api('POST', `/api/tasks/${b.dataset.run}/run`);
        toast('已触发执行', 'success');
        showRunLog(d.run_id);
      } catch (e) { toast(e.message, 'error'); }
      load();
    });
    $$('#task-tbody [data-kill]').forEach(b => b.onclick = async () => {
      if (!await confirmDialog('确定终止该任务全部运行中的进程？', '强制终止')) return;
      try {
        const d = await api('POST', `/api/tasks/${b.dataset.kill}/kill`);
        toast(`已终止 ${d.killed} 个进程`, 'success');
      } catch (e) { toast(e.message, 'error'); }
      load();
    });
    $$('#task-tbody [data-hist]').forEach(b => b.onclick = () => {
      const t = allTasks.find(x => x.id === +b.dataset.hist);
      openTaskHistory(t);
    });
    $$('#task-tbody [data-edit]').forEach(b => b.onclick = () => {
      const t = allTasks.find(x => x.id === +b.dataset.edit);
      openTaskDialog(t, load);
    });
    $$('#task-tbody [data-del]').forEach(b => b.onclick = async () => {
      if (!await confirmDialog('删除任务将停止调度并清除其日志（保留执行历史），确定？', '删除任务')) return;
      try {
        await api('DELETE', `/api/tasks/${b.dataset.del}`);
        toast('已删除', 'success');
      } catch (e) { toast(e.message, 'error'); }
      load();
    });
    $$('#task-tbody [data-toggle]').forEach(cb => cb.onchange = async () => {
      const id = cb.dataset.toggle;
      const act = cb.checked ? 'resume' : 'pause';
      try {
        await api('POST', `/api/tasks/${id}/${act}`);
        toast(act === 'pause' ? '已暂停' : '已启用', 'success');
      } catch (e) { toast(e.message, 'error'); cb.checked = !cb.checked; }
      load();
    });
  }

  $('#task-q').oninput = debounce(renderRows, 250);
  $('#btn-new-task').onclick = () => openTaskDialog(null, load);

  await load();
  const t = setInterval(load, 5000);
  pageCleanup = () => clearInterval(t);
}

/* ---- 任务编辑对话框 ---- */

const CRON_PRESETS = [
  ['每分钟', '* * * * *'],
  ['每 5 分钟', '*/5 * * * *'],
  ['每 15 分钟', '*/15 * * * *'],
  ['每 30 分钟', '*/30 * * * *'],
  ['每小时（第 0 分）', '0 * * * *'],
  ['每天 0 点', '0 0 * * *'],
  ['每天 8 点', '0 8 * * *'],
  ['工作日 9 点', '0 9 * * 1-5'],
  ['每周一 0 点', '0 0 * * 1'],
  ['每月 1 日 0 点', '0 0 1 * *'],
];

async function openTaskDialog(task, onSaved) {
  const [envsRes, projsRes] = await Promise.all([
    api('GET', '/api/envs').catch(() => ({ envs: [] })),
    api('GET', '/api/projects').catch(() => ({ projects: [] })),
  ]);
  const envs = (envsRes.envs || []).filter(e => e.status === 'ready');
  const projects = projsRes.projects || [];
  const t = task || {
    name: '', command: '', project: '', workdir: '', env_id: 0,
    schedule_type: 'interval', interval_seconds: 300, run_date: '', cron_expr: '*/5 * * * *',
    max_concurrent: 1, tags: '',
  };

  const m = openModal({
    title: task ? '编辑任务' : '新建任务',
    wide: true,
    body: `
      <div class="form-row">
        <div class="form-item" style="flex:2"><label class="req">任务名称</label>
          <input type="text" id="tf-name" value="${esc(t.name)}" placeholder="如：每日销量爬虫"></div>
        <div class="form-item"><label>标签（逗号分隔）</label>
          <input type="text" id="tf-tags" value="${esc(t.tags)}" placeholder="爬虫, 日报"></div>
      </div>
      <div class="form-item"><label class="req">命令（任意命令行，如 python spider.py --date today）</label>
        <textarea id="tf-command" rows="2" class="mono" style="font-family:var(--mono)" placeholder="python spider.py --date today&#10;或：cd sub && python 1.py">${esc(t.command)}</textarea></div>
      <div class="form-row">
        <div class="form-item"><label>所属项目</label>
          <select id="tf-project"><option value="">（无项目）</option>
            ${projects.map(p => `<option value="${esc(p.name)}" ${t.project === p.name ? 'selected' : ''}>${esc(p.name)}</option>`).join('')}
          </select></div>
        <div class="form-item"><label>工作路径（相对项目根）</label>
          <input type="text" id="tf-workdir" value="${esc(t.workdir)}" placeholder="如 sub 或留空">
          <div class="form-hint">决定脚本执行的工作目录；选了项目后生效</div></div>
      </div>
      <div class="form-row">
        <div class="form-item"><label>虚拟环境</label>
          <select id="tf-env"><option value="0">（不指定 · 用系统 PATH）</option>
            ${envs.map(e => `<option value="${e.id}" ${t.env_id === e.id ? 'selected' : ''}>${esc(e.name)}（${esc(e.python_version)}）</option>`).join('')}
          </select>
          <div class="form-hint">命令中的 python 自动指向所选环境</div></div>
        <div class="form-item"><label>最大并发数</label>
          <input type="number" id="tf-maxc" min="1" max="100" value="${t.max_concurrent || 1}">
          <div class="form-hint">上轮未跑完则跳过本次调度</div></div>
      </div>
      <div class="form-item"><label class="req">调度方式</label>
        <div class="radio-group" id="tf-sched-type">
          <label><input type="radio" name="st" value="interval" ${t.schedule_type === 'interval' ? 'checked' : ''}>间隔执行</label>
          <label><input type="radio" name="st" value="cron" ${t.schedule_type === 'cron' ? 'checked' : ''}>Cron 表达式</label>
          <label><input type="radio" name="st" value="date" ${t.schedule_type === 'date' ? 'checked' : ''}>一次性</label>
          <label><input type="radio" name="st" value="manual" ${t.schedule_type === 'manual' ? 'checked' : ''}>仅手动</label>
        </div>
      </div>
      <div class="form-item st-row" data-st="interval"><label>执行间隔（秒）</label>
        <input type="number" id="tf-interval" min="1" value="${t.interval_seconds || 300}">
        <div class="form-hint">如 300 = 每 5 分钟；3600 = 每小时</div></div>
      <div class="form-item st-row" data-st="cron">
        <div class="form-row">
          <div class="form-item" style="flex:2"><label>Cron 表达式（分 时 日 月 周）</label>
            <input type="text" id="tf-cron" class="mono" style="font-family:var(--mono)" value="${esc(t.cron_expr)}" placeholder="*/5 * * * *"></div>
          <div class="form-item"><label>常用预设</label>
            <select id="tf-cron-preset"><option value="">选择…</option>
              ${CRON_PRESETS.map(([n, v]) => `<option value="${v}">${n}（${v}）</option>`).join('')}
            </select></div>
        </div>
      </div>
      <div class="form-item st-row" data-st="date"><label>执行时间</label>
        <input type="datetime-local" id="tf-date" value="${esc(t.run_date)}">
        <div class="form-hint">到点触发一次后自动转为暂停；服务重启后 60 秒宽限期内仍会补跑</div></div>
      <div class="form-item st-row" data-st="manual"><div class="form-hint">该任务不会被自动调度，仅在点击「执行」时运行。</div></div>
    `,
  });

  function syncSt() {
    const st = m.el.querySelector('input[name=st]:checked').value;
    $$('.st-row', m.el).forEach(r => r.style.display = r.dataset.st === st ? '' : 'none');
  }
  $$('input[name=st]', m.el).forEach(r => r.onchange = syncSt);
  syncSt();
  $('#tf-cron-preset', m.el).onchange = () => {
    if ($('#tf-cron-preset', m.el).value) $('#tf-cron', m.el).value = $('#tf-cron-preset', m.el).value;
  };

  const cancel = document.createElement('button');
  cancel.className = 'btn'; cancel.textContent = '取消';
  const ok = document.createElement('button');
  ok.className = 'btn primary'; ok.textContent = task ? '保存' : '创建';
  cancel.onclick = m.close;
  ok.onclick = async () => {
    const body = {
      name: $('#tf-name', m.el).value,
      command: $('#tf-command', m.el).value,
      project: $('#tf-project', m.el).value,
      workdir: $('#tf-workdir', m.el).value,
      env_id: +$('#tf-env', m.el).value,
      schedule_type: m.el.querySelector('input[name=st]:checked').value,
      interval_seconds: +$('#tf-interval', m.el).value || 0,
      run_date: $('#tf-date', m.el).value,
      cron_expr: $('#tf-cron', m.el).value.trim(),
      max_concurrent: +$('#tf-maxc', m.el).value || 1,
      tags: $('#tf-tags', m.el).value,
    };
    try {
      if (task) await api('PUT', `/api/tasks/${task.id}`, body);
      else await api('POST', '/api/tasks', body);
      toast(task ? '已保存' : '任务已创建', 'success');
      m.close();
      if (onSaved) onSaved();
    } catch (e) { toast(e.message, 'error'); }
  };
  m.footer.append(cancel, ok);
}

/* ---- 任务执行历史 ---- */

async function openTaskHistory(task) {
  const m = openModal({ title: `执行历史 · ${task.name}`, wide: true, body: `
    <table class="tbl">
      <thead><tr><th>编号</th><th>状态</th><th>触发</th><th>开始时间</th><th>耗时</th><th>退出码</th><th></th></tr></thead>
      <tbody id="hist-body"><tr><td colspan="7" class="tbl-empty">加载中…</td></tr></tbody>
    </table>
    <div class="pager"><span id="hist-total"></span><button class="btn sm" id="hist-more">加载更多</button></div>
  `});
  let page = 1, total = 0;
  async function loadPage() {
    try {
      const d = await api('GET', `/api/tasks/${task.id}/runs?page=${page}&page_size=15`);
      total = d.total;
      $('#hist-total', m.el).textContent = `共 ${total} 条`;
      if (page === 1) $('#hist-body', m.el).innerHTML = '';
      (d.items || []).forEach(r => {
        const tr = document.createElement('tr');
        tr.style.cursor = 'pointer';
        tr.innerHTML = `
          <td>#${r.id}</td><td>${statusBadge(r.status)}</td><td>${triggerText(r.trigger)}</td>
          <td class="nowrap">${esc(r.start_time)}</td><td>${fmtDur(r.duration_ms)}</td>
          <td>${r.exit_code == null ? '-' : r.exit_code}</td>
          <td><button class="btn sm">日志</button></td>`;
        tr.onclick = () => showRunLog(r.id);
        $('#hist-body', m.el).appendChild(tr);
      });
      if (page === 1 && !((d.items || []).length)) $('#hist-body', m.el).innerHTML = '<tr><td colspan="7" class="tbl-empty">暂无记录</td></tr>';
      page++;
      if ((page - 1) * 15 >= total) $('#hist-more', m.el).style.display = 'none';
    } catch (e) { toast(e.message, 'error'); }
  }
  $('#hist-more', m.el).onclick = loadPage;
  loadPage();
}

/* ---- 运行日志查看器 ---- */

function showRunLog(runId) {
  const m = openModal({
    title: `运行日志 · #${runId}`,
    wide: true,
    footer: null,
    onClose() { clearTimeout(timer); },
    body: `
      <div class="log-meta" id="rl-meta">加载中…</div>
      <div class="log-toolbar">
        <input type="text" id="rl-kw" placeholder="关键词过滤（回车应用）…">
        <label class="switch"><input type="checkbox" id="rl-follow" checked>自动刷新</label>
        <label class="switch"><input type="checkbox" id="rl-scroll" checked>自动滚动</label>
        <span class="muted" id="rl-size"></span>
      </div>
      <div class="log-box" id="rl-box">（暂无输出）</div>`,
  });

  let offset = 0, text = '', timer = null, run = null;

  function render() {
    const box = $('#rl-box', m.el);
    const k = $('#rl-kw', m.el).value.trim().toLowerCase();
    let html;
    if (k) {
      const lines = text.split('\n').filter(l => l.toLowerCase().includes(k));
      html = lines.map(l => esc(l).split(k).join(`<span class="kw-hit">${esc(k)}</span>`)).join('\n');
    } else {
      html = esc(text);
    }
    box.innerHTML = html || '（暂无输出）';
    if ($('#rl-scroll', m.el).checked) box.scrollTop = box.scrollHeight;
  }

  function renderMeta() {
    if (!run) return;
    const ec = run.exit_code == null ? '-' : run.exit_code;
    $('#rl-meta', m.el).innerHTML = `
      <span>任务：<b>${esc(run.task_name)}</b></span><span>状态：<b>${statusBadge(run.status)}</b></span>
      <span>触发：<b>${triggerText(run.trigger)}</b></span><span>开始：<b>${esc(run.start_time)}</b></span>
      <span>耗时：<b>${fmtDur(run.duration_ms)}</b></span><span>退出码：<b>${ec}</b></span>`;
  }

  async function poll() {
    try {
      const d = await api('GET', `/api/runs/${runId}/log?offset=${offset}`);
      if (d.content) { text += d.content; offset = d.size; render(); }
      $('#rl-size', m.el).textContent = fmtBytes(d.size);
      const rd = await api('GET', `/api/runs/${runId}`);
      run = rd.run;
      renderMeta();
    } catch (e) { /* ignore */ }
    if (run && run.status === 'running') {
      if ($('#rl-follow', m.el).checked) timer = setTimeout(poll, 1500);
      else timer = setTimeout(poll, 3000);
    }
  }

  $('#rl-kw', m.el).onkeydown = e => { if (e.key === 'Enter') render(); };
  $('#rl-scroll', m.el).onchange = render;
  poll();
}

/* ================= 项目管理 ================= */

async function renderProjects(root) {
  root.innerHTML = `
    <div class="proj-layout">
      <div class="card">
        <div class="card-title">项目列表<button class="btn sm primary" id="btn-new-proj">+ 新建</button></div>
        <div id="proj-list"><div class="tbl-empty">加载中…</div></div>
      </div>
      <div class="card" style="margin-bottom:0">
        <div id="file-area"><div class="tbl-empty">选择左侧项目以浏览文件</div></div>
      </div>
    </div>
    <input type="file" id="proj-file-input" multiple hidden>
    <input type="file" id="proj-dir-input" webkitdirectory hidden>`;

  let currentProject = null;
  let currentPath = '';

  async function loadProjects() {
    try {
      const d = await api('GET', '/api/projects');
      const list = d.projects || [];
      $('#proj-list', root).innerHTML = list.length ? list.map(p => {
        const tags = (p.tags || '').split(/[,,]/).map(s => s.trim()).filter(Boolean)
          .map(s => `<span class="tag">${esc(s)}</span>`).join('');
        return `<div class="proj-list-item ${p.name === currentProject ? 'active' : ''}" data-proj="${esc(p.name)}">
          <div style="min-width:0">
            <div class="name">${esc(p.name)}</div>
            ${tags ? '<div>' + tags + '</div>' : ''}
          </div>
          <span class="ops">
            <button class="btn sm" data-tags="${esc(p.name)}" title="编辑标签">标签</button>
            <button class="btn sm danger" data-del="${esc(p.name)}" title="删除项目">删</button>
          </span>
        </div>`;
      }).join('') : '<div class="tbl-empty">暂无项目</div>';
      bindProjList();
    } catch (e) { $('#proj-list', root).innerHTML = `<div class="tbl-empty">${esc(e.message)}</div>`; }
  }

  function bindProjList() {
    $$('#proj-list .proj-list-item', root).forEach(item => {
      item.onclick = e => {
        if (e.target.closest('button')) return;
        currentProject = item.dataset.proj;
        currentPath = '';
        loadProjects();
        renderFiles();
      };
    });
    $$('#proj-list [data-tags]', root).forEach(b => b.onclick = async () => {
      const name = b.dataset.tags;
      try {
        const d = await api('GET', '/api/projects');
        const p = (d.projects || []).find(x => x.name === name) || { tags: '' };
        const mm = openModal({
          title: `编辑标签 · ${name}`,
          body: `<div class="form-item"><label>标签（逗号分隔）</label>
            <input type="text" id="pt-tags" value="${esc(p.tags)}" placeholder="如：爬虫, v2"></div>`,
        });
        const cancel = document.createElement('button'); cancel.className = 'btn'; cancel.textContent = '取消';
        const ok = document.createElement('button'); ok.className = 'btn primary'; ok.textContent = '保存';
        cancel.onclick = mm.close;
        ok.onclick = async () => {
          try {
            await api('PUT', `/api/projects/${encodeURIComponent(name)}`, { tags: $('#pt-tags', mm.el).value });
            toast('已保存', 'success'); mm.close(); loadProjects();
          } catch (e2) { toast(e2.message, 'error'); }
        };
        mm.footer.append(cancel, ok);
      } catch (e) { toast(e.message, 'error'); }
    });
    $$('#proj-list [data-del]', root).forEach(b => b.onclick = async () => {
      if (!await confirmDialog(`删除项目「${b.dataset.del}」将递归删除其全部文件，确定？`, '删除项目')) return;
      try {
        await api('DELETE', `/api/projects/${encodeURIComponent(b.dataset.del)}`);
        toast('已删除', 'success');
        if (currentProject === b.dataset.del) { currentProject = null; $('#file-area', root).innerHTML = '<div class="tbl-empty">选择左侧项目以浏览文件</div>'; }
        loadProjects();
      } catch (e) { toast(e.message, 'error'); }
    });
  }

  async function renderFiles() {
    const area = $('#file-area', root);
    if (!currentProject) return;
    const encName = encodeURIComponent(currentProject);
    try {
      const d = await api('GET', `/api/projects/${encName}/files?path=${encodeURIComponent(currentPath)}`);
      const entries = d.entries || [];
      const crumbs = breadcrumbHTML(currentPath);
      area.innerHTML = `
        <div class="toolbar">
          <div class="breadcrumb" style="margin:0;flex:1">${crumbs}</div>
          <button class="btn sm" id="f-upfiles">上传文件</button>
          <button class="btn sm" id="f-updir">上传文件夹</button>
          <button class="btn sm" id="f-mkdir">新建文件夹</button>
          <button class="btn sm" id="f-refresh">刷新</button>
        </div>
        <div class="dropzone" id="f-drop">拖拽 文件 / 整个文件夹 到此处上传</div>
        <div style="overflow-x:auto">
        <table class="tbl">
          <thead><tr><th style="width:46%">名称</th><th>大小</th><th>修改时间</th><th style="width:150px">操作</th></tr></thead>
          <tbody id="f-tbody"></tbody>
        </table></div>`;
      const tb = $('#f-tbody', root);
      if (!entries.length) {
        tb.innerHTML = '<tr><td colspan="4" class="tbl-empty">空文件夹</td></tr>';
      } else {
        tb.innerHTML = entries.map(f => {
          const p = currentPath ? currentPath + '/' + f.name : f.name;
          if (f.is_dir) {
            return `<tr class="f-row" data-path="${esc(p)}" data-dir="1" style="cursor:pointer">
              <td>📁 <b>${esc(f.name)}</b></td><td>-</td><td class="nowrap">${esc(f.mtime)}</td>
              <td><div class="actions"><button class="btn sm" data-open="${esc(p)}">打开</button>
                <button class="btn sm danger" data-rm="${esc(p)}">删除</button></div></td></tr>`;
          }
          return `<tr class="f-row" data-path="${esc(p)}" style="cursor:pointer">
            <td>📄 ${esc(f.name)}</td><td>${fmtBytes(f.size)}</td><td class="nowrap">${esc(f.mtime)}</td>
            <td><div class="actions"><button class="btn sm" data-edit="${esc(p)}">编辑</button>
              <a class="btn sm" href="/api/projects/${encName}/file?path=${encodeURIComponent(p)}&download=1">下载</a>
              <button class="btn sm danger" data-rm="${esc(p)}">删除</button></div></td></tr>`;
        }).join('');
      }
      bindFileRows();
    } catch (e) {
      area.innerHTML = `<div class="tbl-empty">加载失败：${esc(e.message)}</div>`;
    }
  }

  function breadcrumbHTML(p) {
    let html = `<a data-crumb="">${esc(currentProject)}</a>`;
    if (p) {
      const parts = p.split('/');
      let acc = '';
      parts.forEach((seg, i) => {
        acc = acc ? acc + '/' + seg : seg;
        html += `<span class="sep">/</span>` + (i === parts.length - 1
          ? `<span class="cur">${esc(seg)}</span>`
          : `<a data-crumb="${esc(acc)}">${esc(seg)}</a>`);
      });
    }
    return html;
  }

  function bindFileRows() {
    $$('#file-area [data-crumb]', root).forEach(a => a.onclick = () => {
      currentPath = a.dataset.crumb || '';
      renderFiles();
    });
    $$('#file-area .f-row', root).forEach(tr => tr.onclick = e => {
      if (e.target.closest('button, a')) return;
      if (tr.dataset.dir) { currentPath = tr.dataset.path; renderFiles(); }
      else openEditor(tr.dataset.path);
    });
    $$('#file-area [data-open]', root).forEach(b => b.onclick = () => { currentPath = b.dataset.open; renderFiles(); });
    $$('#file-area [data-edit]', root).forEach(b => b.onclick = () => openEditor(b.dataset.edit));
    $$('#file-area [data-rm]', root).forEach(b => b.onclick = async () => {
      if (!await confirmDialog(`确定删除「${b.dataset.rm}」？（文件夹将递归删除）`, '删除')) return;
      try {
        await api('DELETE', `/api/projects/${encodeURIComponent(currentProject)}/file?path=${encodeURIComponent(b.dataset.rm)}`);
        toast('已删除', 'success');
        renderFiles();
      } catch (e) { toast(e.message, 'error'); }
    });
    $('#f-refresh', root).onclick = renderFiles;
    $('#f-upfiles', root).onclick = () => $('#proj-file-input', root).click();
    $('#f-updir', root).onclick = () => $('#proj-dir-input', root).click();
    $('#f-mkdir', root).onclick = async () => {
      const mm = openModal({
        title: '新建文件夹',
        body: `<div class="form-item"><label>文件夹名称（可在名称中用 / 建多级）</label>
          <input type="text" id="mk-path" placeholder="如 data 或 data/raw"></div>`,
      });
      const cancel = document.createElement('button'); cancel.className = 'btn'; cancel.textContent = '取消';
      const ok = document.createElement('button'); ok.className = 'btn primary'; ok.textContent = '创建';
      cancel.onclick = mm.close;
      ok.onclick = async () => {
        const v = $('#mk-path', mm.el).value.trim();
        if (!v) { toast('请输入名称', 'warning'); return; }
        const full = currentPath ? currentPath + '/' + v : v;
        try {
          await api('POST', `/api/projects/${encodeURIComponent(currentProject)}/mkdir`, { path: full });
          toast('已创建', 'success'); mm.close(); renderFiles();
        } catch (e) { toast(e.message, 'error'); }
      };
      mm.footer.append(cancel, ok);
    };

    /* 上传 */
    $('#proj-file-input', root).onchange = function () {
      const items = Array.from(this.files || []).map(f => ({ file: f, path: f.name }));
      this.value = '';
      doUpload(items);
    };
    $('#proj-dir-input', root).onchange = function () {
      const items = Array.from(this.files || []).map(f => {
        const rel = (f.webkitRelativePath || f.name).split('/').slice(1).join('/') || f.name;
        return { file: f, path: rel };
      }).filter(it => it.path);
      this.value = '';
      doUpload(items);
    };
    const dz = $('#f-drop', root);
    ['dragenter', 'dragover'].forEach(ev => dz.addEventListener(ev, e => { e.preventDefault(); dz.classList.add('drag'); }));
    ['dragleave', 'drop'].forEach(ev => dz.addEventListener(ev, e => { e.preventDefault(); dz.classList.remove('drag'); }));
    dz.addEventListener('drop', async e => {
      const items = await collectDroppedFiles(e.dataTransfer.items);
      if (items.length) doUpload(items);
    });
  }

  async function collectDroppedFiles(dtItems) {
    const out = [];
    const entries = Array.from(dtItems || [])
      .map(i => (i.webkitGetAsEntry && i.webkitGetAsEntry())).filter(Boolean);
    async function walk(entry, prefix) {
      if (entry.isFile) {
        const f = await new Promise(res => entry.file(res, () => res(null)));
        if (f) out.push({ file: f, path: prefix + entry.name });
      } else if (entry.isDirectory) {
        const reader = entry.createReader();
        let batch;
        do {
          batch = await new Promise(res => reader.readEntries(res, () => res([])));
          for (const e of batch) await walk(e, prefix + entry.name + '/');
        } while (batch.length);
      }
    }
    for (const e of entries) await walk(e, '');
    return out;
  }

  async function doUpload(items) {
    if (!items.length) return;
    const dz = $('#f-drop', root);
    if (dz) dz.textContent = `正在上传 ${items.length} 个文件…`;
    const fd = new FormData();
    items.forEach(it => fd.append('files', it.file, it.path || it.file.name));
    try {
      const resp = await fetch(`/api/projects/${encodeURIComponent(currentProject)}/upload`, { method: 'POST', body: fd });
      const d = await resp.json().catch(() => null);
      if (!resp.ok) throw new Error((d && d.error) || 'HTTP ' + resp.status);
      toast(`上传完成：${d.count} 个文件` + ((d.errors || []).length ? `，失败 ${d.errors.length} 个` : ''), (d.errors || []).length ? 'warning' : 'success');
      (d.errors || []).slice(0, 3).forEach(e2 => toast(e2, 'error'));
    } catch (e) { toast('上传失败：' + e.message, 'error'); }
    renderFiles();
  }

  async function openEditor(path) {
    const encName = encodeURIComponent(currentProject);
    let d;
    try {
      d = await api('GET', `/api/projects/${encName}/file?path=${encodeURIComponent(path)}`);
    } catch (e) { toast(e.message, 'error'); return; }
    const mm = openModal({
      title: `编辑 · ${path}`,
      wide: true,
      onClose() { document.removeEventListener('keydown', keyHandler); },
      body: `
        <textarea id="ed-text" class="file-editor-textarea" spellcheck="false"></textarea>
        <div class="muted mt8">Ctrl+S 保存 · 仅支持 UTF-8 文本（≤ 1MB）· 当前 ${fmtBytes(d.size)}</div>`,
    });
    const ta = $('#ed-text', mm.el);
    ta.value = d.content;
    async function save() {
      try {
        await api('PUT', `/api/projects/${encName}/file`, { path, content: ta.value });
        toast('已保存', 'success');
      } catch (e) { toast(e.message, 'error'); }
    }
    const keyHandler = e => {
      if ((e.ctrlKey || e.metaKey) && e.key === 's') { e.preventDefault(); save(); }
    };
    document.addEventListener('keydown', keyHandler);
    const cancel = document.createElement('button'); cancel.className = 'btn'; cancel.textContent = '关闭';
    const ok = document.createElement('button'); ok.className = 'btn primary'; ok.textContent = '保存';
    cancel.onclick = mm.close;
    ok.onclick = save;
    mm.footer.append(cancel, ok);
  }

  $('#btn-new-proj', root).onclick = async () => {
    const mm = openModal({
      title: '新建项目',
      body: `
        <div class="form-item"><label class="req">项目名称</label>
          <input type="text" id="np-name" placeholder="如 sales-spider"></div>
        <div class="form-item"><label>标签（逗号分隔）</label>
          <input type="text" id="np-tags" placeholder="如：爬虫, v1"></div>
        <div class="form-hint">一个项目对应 projects/ 下一个文件夹</div>`,
    });
    const cancel = document.createElement('button'); cancel.className = 'btn'; cancel.textContent = '取消';
    const ok = document.createElement('button'); ok.className = 'btn primary'; ok.textContent = '创建';
    cancel.onclick = mm.close;
    ok.onclick = async () => {
      const name = $('#np-name', mm.el).value.trim();
      if (!name) { toast('请输入项目名称', 'warning'); return; }
      try {
        await api('POST', '/api/projects', { name, tags: $('#np-tags', mm.el).value });
        toast('项目已创建', 'success');
        mm.close();
        currentProject = name;
        currentPath = '';
        loadProjects();
        renderFiles();
      } catch (e) { toast(e.message, 'error'); }
    };
    mm.footer.append(cancel, ok);
  };

  await loadProjects();
  if (currentProject) renderFiles();
}

/* ================= 环境管理 ================= */

async function renderEnvs(root) {
  root.innerHTML = `
    <div class="card">
      <div class="card-title">Python 虚拟环境
        <span class="sub">一个环境可服务多个任务 · 删除环境不会删除 pythons/ 下可复用的运行时</span>
      </div>
      <div class="env-grid" id="env-grid"><div class="tbl-empty">加载中…</div></div>
      <div class="mt16"><button class="btn primary" id="btn-new-env">+ 新建环境</button></div>
    </div>`;

  async function load() {
    try {
      const d = await api('GET', '/api/envs');
      const envs = d.envs || [];
      if (!envs.length) {
        $('#env-grid').innerHTML = '<div class="tbl-empty" style="grid-column:1/-1">暂无环境，点击下方按钮创建</div>';
        return;
      }
      $('#env-grid').innerHTML = envs.map(e => {
        const src = e.source === 'download'
          ? '<span class="badge info">在线下载</span>' : '<span class="badge gray">本机解释器</span>';
        let st;
        if (e.status === 'creating') st = `<span class="badge running">创建中${e.busy ? '…' : ''}</span>`;
        else if (e.status === 'ready') st = '<span class="badge success">就绪</span>';
        else st = '<span class="badge failed">失败</span>';
        return `<div class="env-card">
          <div class="head"><div class="name">${esc(e.name)}</div><div>${st}</div></div>
          <div class="meta">
            <div>Python 版本：${esc(e.python_version || '-')} ${src}</div>
            <div class="mono" style="word-break:break-all">${esc(e.interpreter || '（创建中…）')}</div>
            <div>创建于 ${esc(e.created_at || '-')}${e.busy ? ' · <b style="color:var(--warning)">操作进行中</b>' : ''}</div>
          </div>
          <div class="ops">
            <button class="btn sm" data-pkg="${e.id}" ${e.status === 'ready' ? '' : 'disabled'}>包管理</button>
            <button class="btn sm" data-log="${e.id}">日志</button>
            <button class="btn sm danger" data-del="${e.id}">删除</button>
          </div>
        </div>`;
      }).join('');

      $$('#env-grid [data-pkg]').forEach(b => b.onclick = () => {
        const e = envs.find(x => x.id === +b.dataset.pkg);
        openPackageManager(e);
      });
      $$('#env-grid [data-log]').forEach(b => b.onclick = () => {
        const e = envs.find(x => x.id === +b.dataset.log);
        showEnvLog(e);
      });
      $$('#env-grid [data-del]').forEach(b => b.onclick = async () => {
        const e = envs.find(x => x.id === +b.dataset.del);
        if (!await confirmDialog(`删除环境「${e.name}」？（仅删除该虚拟环境目录，不影响 pythons/ 下的运行时）`, '删除环境')) return;
        try {
          await api('DELETE', `/api/envs/${e.id}`);
          toast('已删除', 'success');
        } catch (e2) { toast(e2.message, 'error'); }
        load();
      });
    } catch (e) {
      $('#env-grid').innerHTML = `<div class="tbl-empty">${esc(e.message)}</div>`;
    }
  }

  $('#btn-new-env').onclick = () => openEnvDialog(load);

  await load();
  const t = setInterval(load, 3000);
  pageCleanup = () => clearInterval(t);
}

async function openEnvDialog(onCreated) {
  const m = openModal({
    title: '新建虚拟环境',
    body: `
      <div class="form-item"><label class="req">环境名称</label>
        <input type="text" id="ev-name" placeholder="如 py312 或 spider-env"></div>
      <div class="form-item"><label class="req">创建方式</label>
        <div class="radio-group">
          <label><input type="radio" name="ev-src" value="local" checked>本机解释器</label>
          <label><input type="radio" name="ev-src" value="download">在线下载 Python</label>
        </div></div>
      <div id="ev-local-box">
        <div class="form-item"><label>本机解释器（自动发现，可手动输入路径）</label>
          <select id="ev-interp"><option value="">发现中…</option></select>
          <div class="form-hint" id="ev-interp-hint">自动发现 PATH / py 启动器 / 常见安装目录中的 Python</div>
          <input type="text" id="ev-interp-custom" placeholder="手动输入解释器完整路径（可选，优先于上方选择）"></div>
        <button class="btn sm" id="ev-refresh-interp">重新发现</button>
      </div>
      <div id="ev-download-box" style="display:none">
        <div class="form-item"><label>Python 版本（python-build-standalone）</label>
          <select id="ev-version"><option value="">获取中…</option></select>
          <div class="form-hint" id="ev-ver-hint">在线下载依赖 GitHub 连通性；下载的运行时保留在 pythons/ 可复用</div></div>
        <div class="form-item"><label>下载加速前缀（GitHub 不可达时使用，可自由输入任意加速服务，保存后全局生效）</label>
          <input type="text" id="ev-mirror-custom" placeholder="可输入任意加速前缀，如 https://your-proxy.example.com；留空 = 直连 GitHub">
          <div style="display:flex;gap:8px;margin-top:8px;flex-wrap:wrap" id="ev-mirror-quick">
            <button class="btn sm" data-mirror="">直连 GitHub</button>
            <button class="btn sm" data-mirror="https://ghfast.top">ghfast.top</button>
            <button class="btn sm" data-mirror="https://gh.llkk.cc">gh.llkk.cc</button>
            <button class="btn sm" data-mirror="https://ghproxy.net">ghproxy.net</button>
          </div>
          <div class="form-hint">阿里源/清华源仅镜像 pip 包，不能用于运行时下载；前缀需为 ghproxy 形式（前缀 + GitHub 原始地址），公共加速可能失效，可自行填入可用的加速服务</div></div>
      </div>`,
  });

  async function loadInterps() {
    const sel = $('#ev-interp', m.el);
    sel.innerHTML = '<option value="">发现中…</option>';
    try {
      const d = await api('GET', '/api/interpreters');
      const list = d.interpreters || [];
      if (!list.length) {
        sel.innerHTML = '<option value="">（未发现，请在下方手动输入路径）</option>';
      } else {
        sel.innerHTML = list.map(i => `<option value="${esc(i.path)}">${esc(i.path)}（Python ${esc(i.version)}）</option>`).join('');
      }
    } catch (e) {
      sel.innerHTML = '<option value="">发现失败，请手动输入路径</option>';
    }
  }
  loadInterps();

  async function loadVersions() {
    const sel = $('#ev-version', m.el);
    try {
      const d = await api('GET', '/api/python-versions');
      const list = d.versions || [];
      sel.innerHTML = list.map(v => `<option value="${v}">Python ${v}</option>`).join('');
      $('#ev-ver-hint', m.el).textContent = (d.source === 'github' ? '（列表来自 GitHub 最新发布）' : '（离线兜底列表）') +
        ' · 在线下载依赖 GitHub 连通性；运行时保留在 pythons/ 可复用';
    } catch (e) {
      sel.innerHTML = '<option value="">获取失败</option>';
    }
  }

  async function loadMirror() {
    try {
      const d = await api('GET', '/api/download-mirror');
      $('#ev-mirror-custom', m.el).value = d.mirror || '';
    } catch (e) { /* ignore */ }
  }

  async function saveMirror() {
    let val = $('#ev-mirror-custom', m.el).value.trim().replace(/\/+$/, '');
    try {
      await api('POST', '/api/download-mirror', { mirror: val });
      toast('下载加速设置已保存', 'success');
    } catch (e) { toast(e.message, 'error'); }
  }

  $$('input[name=ev-src]', m.el).forEach(r => r.onchange = () => {
    const v = m.el.querySelector('input[name=ev-src]:checked').value;
    $('#ev-local-box', m.el).style.display = v === 'local' ? '' : 'none';
    $('#ev-download-box', m.el).style.display = v === 'download' ? '' : 'none';
    if (v === 'download') { loadVersions(); loadMirror(); }
  });
  $$('#ev-mirror-quick [data-mirror]', m.el).forEach(b => b.onclick = () => {
    $('#ev-mirror-custom', m.el).value = b.dataset.mirror;
    saveMirror();
  });
  $('#ev-mirror-custom', m.el).onblur = saveMirror;
  $('#ev-refresh-interp', m.el).onclick = loadInterps;

  const cancel = document.createElement('button'); cancel.className = 'btn'; cancel.textContent = '取消';
  const ok = document.createElement('button'); ok.className = 'btn primary'; ok.textContent = '创建';
  cancel.onclick = m.close;
  ok.onclick = async () => {
    const name = $('#ev-name', m.el).value.trim();
    if (!name) { toast('请输入环境名称', 'warning'); return; }
    const source = m.el.querySelector('input[name=ev-src]:checked').value;
    try {
      let body;
      if (source === 'local') {
        const custom = ($('#ev-interp-custom', m.el) || {}).value || '';
        const interpreter = custom.trim() || $('#ev-interp', m.el).value;
        if (!interpreter) { toast('请选择或输入解释器路径', 'warning'); return; }
        body = { name, source, interpreter };
      } else {
        const version = $('#ev-version', m.el).value;
        if (!version) { toast('请选择 Python 版本', 'warning'); return; }
        body = { name, source, version };
      }
      const d = await api('POST', '/api/envs', body);
      toast('开始创建，可点击「日志」查看进度', 'success');
      m.close();
      if (onCreated) onCreated();
      showEnvLog(d.env);
    } catch (e) { toast(e.message, 'error'); }
  };
  m.footer.append(cancel, ok);
}

/* ---- 环境日志查看器 ---- */

function showEnvLog(env) {
  const m = openModal({
    title: `环境日志 · ${env.name}`,
    wide: true,
    footer: null,
    onClose() { clearInterval(timer); },
    body: `
      <div class="log-meta" id="el-meta"></div>
      <div class="log-box" id="el-box" style="height:340px">（暂无输出）</div>`,
  });
  let offset = 0;
  async function poll() {
    try {
      const d = await api('GET', `/api/envs/${env.id}/log?offset=${offset}`);
      if (d.content) {
        const box = $('#el-box', m.el);
        box.innerHTML += esc(d.content);
        box.scrollTop = box.scrollHeight;
        offset = d.size;
      }
      $('#el-meta', m.el).innerHTML =
        `状态：<b>${envStatusBadge(d.status)}</b> · ${d.busy ? '<b style="color:var(--warning)">操作进行中…</b>' : '空闲'} · ${fmtBytes(d.size)}`;
    } catch (e) { /* ignore */ }
  }
  const timer = setInterval(poll, 1200);
  poll();
}

/* ---- 包管理 ---- */

async function openPackageManager(env) {
  const mirrors = await api('GET', '/api/mirrors').then(d => d.mirrors || []).catch(() => []);
  const m = openModal({
    title: `包管理 · ${env.name}`,
    wide: true,
    onClose() { clearInterval(timer); },
    body: `
      <div class="pkg-install-bar">
        <input type="text" id="pk-install" placeholder="要安装的包，空格分隔，如：requests pandas==2.2.0">
        <select id="pk-mirror">${mirrors.map(x => `<option value="${esc(x.url)}">${esc(x.name)}</option>`).join('')}</select>
        <button class="btn primary" id="pk-install-btn">安装</button>
      </div>
      <div class="toolbar">
        <input type="text" id="pk-filter" class="pkg-filter" placeholder="过滤已安装的包…">
        <button class="btn sm" id="pk-refresh">刷新列表</button>
      </div>
      <div style="max-height:260px;overflow-y:auto">
        <table class="tbl">
          <thead><tr><th>包名</th><th>版本</th><th style="width:80px">操作</th></tr></thead>
          <tbody id="pk-body"><tr><td colspan="3" class="tbl-empty">加载中…</td></tr></tbody>
        </table>
      </div>
      <div class="card-title mt16" style="margin-bottom:8px">pip 日志</div>
      <div class="log-box" id="pk-log" style="height:220px">（暂无输出）</div>`,
  });

  let packages = [], logOffset = 0;

  async function loadPkgs() {
    try {
      const d = await api('GET', `/api/envs/${env.id}/packages`);
      packages = d.packages || [];
      renderPkgs();
    } catch (e) {
      $('#pk-body', m.el).innerHTML = `<tr><td colspan="3" class="tbl-empty">${esc(e.message)}</td></tr>`;
    }
  }

  function renderPkgs() {
    const q = ($('#pk-filter', m.el).value || '').toLowerCase();
    const list = packages.filter(p => !q || p.name.toLowerCase().includes(q));
    if (!list.length) {
      $('#pk-body', m.el).innerHTML = '<tr><td colspan="3" class="tbl-empty">无匹配的包</td></tr>';
      return;
    }
    $('#pk-body', m.el).innerHTML = list.map(p => `<tr>
      <td class="mono">${esc(p.name)}</td><td class="mono">${esc(p.version)}</td>
      <td><button class="btn sm danger" data-un="${esc(p.name)}">卸载</button></td></tr>`).join('');
    $$('#pk-body [data-un]', m.el).forEach(b => b.onclick = async () => {
      if (!await confirmDialog(`确定卸载 ${b.dataset.un}？`, '卸载包')) return;
      try {
        await api('DELETE', `/api/envs/${env.id}/packages?name=${encodeURIComponent(b.dataset.un)}`);
        toast('卸载已开始，见下方日志', 'success');
      } catch (e) { toast(e.message, 'error'); }
    });
  }

  $('#pk-filter', m.el).oninput = renderPkgs;
  $('#pk-refresh', m.el).onclick = loadPkgs;
  $('#pk-install-btn', m.el).onclick = async () => {
    const pkgs = $('#pk-install', m.el).value.trim();
    if (!pkgs) { toast('请输入要安装的包', 'warning'); return; }
    try {
      await api('POST', `/api/envs/${env.id}/packages`, {
        packages: pkgs,
        index: $('#pk-mirror', m.el).value,
      });
      toast('安装已开始，见下方日志', 'success');
    } catch (e) { toast(e.message, 'error'); }
  };

  let wasBusy = null;
  const timer = setInterval(async () => {
    try {
      const d = await api('GET', `/api/envs/${env.id}/log?offset=${logOffset}`);
      if (d.content) {
        const box = $('#pk-log', m.el);
        box.innerHTML += esc(d.content);
        box.scrollTop = box.scrollHeight;
        logOffset = d.size;
      }
      if (wasBusy === true && d.busy === false) loadPkgs();
      wasBusy = d.busy;
    } catch (e) { /* ignore */ }
  }, 1200);

  loadPkgs();
}

/* ================= 运行日志 ================= */

async function renderLogs(root) {
  root.innerHTML = `
    <div class="card">
      <div class="toolbar">
        <select id="lg-task" style="width:200px"><option value="">全部任务</option></select>
        <select id="lg-status" style="width:110px">
          <option value="">全部状态</option>
          <option value="running">运行中</option><option value="success">成功</option>
          <option value="failed">失败</option><option value="killed">已终止</option>
        </select>
        <input type="date" id="lg-from" title="开始日期">
        <span class="muted">至</span>
        <input type="date" id="lg-to" title="结束日期">
        <button class="btn primary" id="lg-query">查询</button>
        <button class="btn" id="lg-reset">重置</button>
        <span class="spacer"></span>
        <span class="muted" id="lg-total"></span>
      </div>
      <div style="overflow-x:auto">
        <table class="tbl">
          <thead><tr><th>编号</th><th>任务</th><th>状态</th><th>触发</th><th>开始时间</th><th>耗时</th><th>退出码</th><th></th></tr></thead>
          <tbody id="lg-body"><tr><td colspan="8" class="tbl-empty">加载中…</td></tr></tbody>
        </table>
      </div>
      <div class="pager">
        <button class="btn sm" id="lg-prev">上一页</button>
        <span id="lg-page"></span>
        <button class="btn sm" id="lg-next">下一页</button>
      </div>
    </div>`;

  api('GET', '/api/tasks').then(d => {
    $('#lg-task').innerHTML = '<option value="">全部任务</option>' +
      (d.tasks || []).map(t => `<option value="${t.id}">${esc(t.name)}</option>`).join('');
  }).catch(() => {});

  let page = 1;

  async function load() {
    const q = new URLSearchParams();
    const taskID = $('#lg-task').value;
    if (taskID) q.set('task_id', taskID);
    if ($('#lg-status').value) q.set('status', $('#lg-status').value);
    if ($('#lg-from').value) q.set('date_from', $('#lg-from').value);
    if ($('#lg-to').value) q.set('date_to', $('#lg-to').value);
    q.set('page', page);
    q.set('page_size', 20);
    try {
      const d = await api('GET', '/api/runs?' + q.toString());
      $('#lg-total').textContent = `共 ${d.total} 条`;
      $('#lg-page').textContent = `第 ${page} 页`;
      const items = d.items || [];
      if (!items.length) {
        $('#lg-body').innerHTML = '<tr><td colspan="8" class="tbl-empty">暂无记录</td></tr>';
      } else {
        $('#lg-body').innerHTML = items.map(r => `<tr data-rid="${r.id}" style="cursor:pointer">
          <td>#${r.id}</td><td>${esc(r.task_name)}</td><td>${statusBadge(r.status)}</td>
          <td>${triggerText(r.trigger)}</td><td class="nowrap">${esc(r.start_time)}</td>
          <td>${fmtDur(r.duration_ms)}</td><td>${r.exit_code == null ? '-' : r.exit_code}</td>
          <td><button class="btn sm">日志</button></td></tr>`).join('');
        $$('#lg-body tr[data-rid]').forEach(tr => tr.onclick = () => showRunLog(+tr.dataset.rid));
      }
      $('#lg-prev').disabled = page <= 1;
      $('#lg-next').disabled = page * 20 >= d.total;
    } catch (e) {
      $('#lg-body').innerHTML = `<tr><td colspan="8" class="tbl-empty">${esc(e.message)}</td></tr>`;
    }
  }

  $('#lg-query').onclick = () => { page = 1; load(); };
  $('#lg-reset').onclick = () => {
    $('#lg-task').value = ''; $('#lg-status').value = '';
    $('#lg-from').value = ''; $('#lg-to').value = '';
    page = 1; load();
  };
  $('#lg-prev').onclick = () => { if (page > 1) { page--; load(); } };
  $('#lg-next').onclick = () => { page++; load(); };

  await load();
  const t = setInterval(load, 8000);
  pageCleanup = () => clearInterval(t);
}

/* ================= 通知设置 ================= */

const NOTIFY_TYPES = [
  ['dingtalk', '钉钉机器人'],
  ['feishu', '飞书机器人'],
  ['wecom', '企业微信群机器人'],
  ['custom', '自定义 webhook'],
];

const NOTIFY_HINTS = {
  dingtalk: '钉钉：群设置 → 机器人 → 添加「自定义」机器人，安全设置建议勾选「自定义关键词」并填「任务」。',
  feishu: '飞书：群设置 → 群机器人 → 添加「Custom Bot」。',
  wecom: '企业微信：群右键 → 添加群机器人。',
  custom: '自定义：POST 该地址，JSON 体为 {"title":"...","text":"..."}。',
};

async function renderSettings(root) {
  let s;
  try { s = await api('GET', '/api/settings/notify'); }
  catch (e) { toast(e.message, 'error'); return; }
  s = Object.assign({ enabled: false, type: 'dingtalk', webhook: '', notify_on: 'failed' }, s);

  root.innerHTML = `
    <div class="card" style="max-width:720px">
      <div class="card-title">任务失败通知<span class="sub">通过 webhook 推送到 IM 群</span></div>
      <div class="form-item">
        <label class="switch"><input type="checkbox" id="ns-enabled" ${s.enabled ? 'checked' : ''}> 启用通知</label>
      </div>
      <div class="form-item"><label>通知渠道</label>
        <div class="radio-group" id="ns-type">
          ${NOTIFY_TYPES.map(([v, n]) => `<label><input type="radio" name="nt" value="${v}" ${s.type === v ? 'checked' : ''}>${n}</label>`).join('')}
        </div>
        <div class="form-hint" id="ns-hint">${esc(NOTIFY_HINTS[s.type] || '')}</div>
      </div>
      <div class="form-item"><label class="req">Webhook 地址</label>
        <input type="text" id="ns-webhook" value="${esc(s.webhook)}" placeholder="https://…">
      </div>
      <div class="form-item"><label>通知时机</label>
        <div class="radio-group">
          <label><input type="radio" name="non" value="failed" ${s.notify_on !== 'all' ? 'checked' : ''}>仅任务失败时</label>
          <label><input type="radio" name="non" value="all" ${s.notify_on === 'all' ? 'checked' : ''}>每次执行完成（含成功）</label>
        </div>
      </div>
      <div style="display:flex;gap:10px">
        <button class="btn primary" id="ns-save">保存设置</button>
        <button class="btn" id="ns-test">发送测试</button>
      </div>
      <div class="form-hint mt8">消息内容包含任务名、状态、耗时、退出码与日志摘要。</div>
    </div>`;

  $$('input[name=nt]', root).forEach(r => r.onchange = () => {
    $('#ns-hint').textContent = NOTIFY_HINTS[r.value] || '';
  });

  function payload() {
    return {
      enabled: $('#ns-enabled', root).checked,
      type: (root.querySelector('input[name=nt]:checked') || {}).value || 'dingtalk',
      webhook: $('#ns-webhook', root).value.trim(),
      notify_on: (root.querySelector('input[name=non]:checked') || {}).value || 'failed',
    };
  }

  $('#ns-save', root).onclick = async () => {
    try {
      await api('POST', '/api/settings/notify', payload());
      toast('已保存', 'success');
    } catch (e) { toast(e.message, 'error'); }
  };
  $('#ns-test', root).onclick = async () => {
    const p = payload();
    if (!p.webhook) { toast('请先填写 webhook 地址', 'warning'); return; }
    try {
      const d = await api('POST', '/api/settings/notify/test', p);
      if (d.ok) toast('测试消息已发送，请到群里查看', 'success');
      else toast('发送失败：' + (d.error || '') + (d.response ? '（' + d.response + '）' : ''), 'error');
    } catch (e) { toast(e.message, 'error'); }
  };
}

/* ================= 启动 ================= */
route();
