// 项目管理：新建/编辑走弹窗，保存后刷新列表
// 注意：必须用 form.elements 取控件——form.id / form.name 会被 HTMLFormElement 内建属性遮蔽
var projectModal = document.getElementById('projectModal');
var projectForm = document.getElementById('projectForm');
UI.bindModal(projectModal);

function pf() { return projectForm.elements; }

document.getElementById('btnNewProject').onclick = function () {
  projectForm.reset();
  pf().id.value = '';
  document.getElementById('projectModalTitle').textContent = UI.t('新建项目');
  UI.openModal(projectModal);
};

function editProject(btn) {
  pf().id.value = btn.dataset.id;
  pf().name.value = btn.dataset.name;
  pf().description.value = btn.dataset.desc || '';
  document.getElementById('projectModalTitle').textContent = UI.t('编辑项目');
  UI.openModal(projectModal);
}

projectForm.addEventListener('submit', async function (e) {
  e.preventDefault();
  if (!UI.validateForm(projectForm)) return;
  var id = pf().id.value;
  var payload = {
    name: pf().name.value.trim(),
    description: pf().description.value.trim()
  };
  try {
    var res = await fetch(id ? '/console/api/projects/' + id : '/console/api/projects', {
      method: id ? 'PUT' : 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
      body: JSON.stringify(payload)
    });
    var data = await res.json();
    if (!res.ok) { UI.alert(data.error || '保存失败', { title: '保存失败' }); return; }
    UI.closeModal(projectModal);
    location.reload();
  } catch (err) {
    UI.toast('网络错误，保存失败', 'error');
  }
});

async function delProject(btn) {
  var ok = await UI.confirm(UI.t('确定删除项目「{0}」？其下文档将回到未分组。', btn.dataset.name), { danger: true });
  if (!ok) return;
  try {
    var res = await fetch('/console/api/projects/' + btn.dataset.id, {
      method: 'DELETE',
      headers: { 'X-Requested-With': 'XMLHttpRequest' }
    });
    if (res.ok) { location.reload(); }
    else {
      var data = await res.json();
      UI.toast(data.error || '删除失败', 'error');
    }
  } catch (err) {
    UI.toast('网络错误，删除失败', 'error');
  }
}

/* ---- 项目成员管理（属主）：列表/添加/改角色/移除 ---- */
var membersModal = document.getElementById('membersModal');
var memberList = document.getElementById('memberList');
var memberUserInput = document.getElementById('memberUserInput');
var memberUserId = document.getElementById('memberUserId');
var memberUserList = document.getElementById('memberUserList');
var memberRole = document.getElementById('memberRole');
var memberAddBtn = document.getElementById('memberAddBtn');
var memberAddRow = document.querySelector('.members-modal .member-add') || document.querySelector('.member-add');
var memberProjectId = 0;
var membersReadonly = false; // 非属主查看成员列表：只读模式
var membersPage = 1;
var membersSize = 15;
var memberPager = document.getElementById('memberPager');

if (membersModal) UI.bindModal(membersModal);

// onclick 属性调用，需挂在全局
window.openMembers = function (btn) {
  memberProjectId = btn.dataset.id;
  membersReadonly = btn.dataset.readonly === '1';
  membersPage = 1;
  memberAddRow.style.display = membersReadonly ? 'none' : 'flex';
  UI.openModal(membersModal);
  loadMembers();
};

// onclick 属性调用：成员退出自己参与的项目（创建者不可退出，后端校验）
window.leaveProject = function (btn) {
  var id = btn.dataset.id;
  var name = btn.dataset.name || '';
  UI.confirm(UI.t('确认退出该项目「{0}」？退出后将无法再访问项目内文档。', name), { danger: true }).then(async function (ok) {
    if (!ok) return;
    btn.disabled = true;
    try {
      var res = await fetch('/console/api/projects/' + id + '/members/me', {
        method: 'DELETE', headers: { 'X-Requested-With': 'XMLHttpRequest' }
      });
      var d = await res.json().catch(function () { return {}; });
      if (!res.ok) { UI.alert((d && d.error) || UI.t('退出失败')); btn.disabled = false; return; }
      UI.toast(UI.t('已退出项目'), 'success');
      setTimeout(function () { location.reload(); }, 600); // 成员数/行变化，整页刷新
    } catch (e) {
      UI.alert(UI.t('退出失败：网络错误'));
      btn.disabled = false;
    }
  });
};

// 成员搜索：按需查询启用用户（后端限 20 条），避免全量枚举
var memberSearchTimer = 0;
memberUserInput.addEventListener('input', function () {
  memberUserId.value = '';
  clearTimeout(memberSearchTimer);
  var q = memberUserInput.value.trim();
  if (!q) { memberUserList.hidden = true; return; }
  memberSearchTimer = setTimeout(function () { searchMemberUsers(q); }, 250);
});
memberUserInput.addEventListener('blur', function () { memberUserList.hidden = true; });

function searchMemberUsers(q) {
  fetch('/console/api/users/options?q=' + encodeURIComponent(q), {
    headers: { 'X-Requested-With': 'XMLHttpRequest' }
  })
    .then(function (r) { return r.json(); })
    .then(function (d) {
      if (memberUserInput.value.trim() !== q) return; // 输入已变化，丢弃过期结果
      var opts = (d && d.data) || [];
      memberUserList.innerHTML = '';
      if (!opts.length) { memberUserList.hidden = true; return; }
      opts.forEach(function (u) {
        var item = mEl('div', 'member-suggest-item', u.nickname + ' (' + u.username + ')');
        item.addEventListener('mousedown', function () { // mousedown 先于 input 的 blur
          memberUserId.value = u.id;
          memberUserInput.value = u.nickname + ' (' + u.username + ')';
          memberUserList.hidden = true;
        });
        memberUserList.appendChild(item);
      });
      memberUserList.hidden = false;
    })
    .catch(function () { memberUserList.hidden = true; });
}

async function loadMembers() {
  memberList.innerHTML = '<div class="muted">' + UI.t('加载中…') + '</div>';
  try {
    var res = await fetch('/console/api/projects/' + memberProjectId + '/members?page=' + membersPage + '&size=' + membersSize, {
      headers: { 'X-Requested-With': 'XMLHttpRequest' }
    });
    var data = await res.json();
    // 服务端可能钳制越界 page/size，回写本地状态避免下次请求继续带旧值
    if (data && data.page) membersPage = data.page;
    if (data && data.size) membersSize = data.size;
    var ms = (data && data.members) || [];
    memberList.innerHTML = '';
    if (!ms.length) {
      memberList.appendChild(mEl('div', 'muted', UI.t('暂无成员')));
    } else {
      ms.forEach(function (m) { memberList.appendChild(memberRow(m)); });
      if (window.bindOwnerHover) window.bindOwnerHover(memberList);
    }
    UI.renderPager(memberPager, {
      page: membersPage,
      totalPages: data.total_pages || 1,
      size: membersSize,
      total: data.total || 0,
      onPage: function (p) { membersPage = p; loadMembers(); },
      onSize: function (s) { membersSize = s; membersPage = 1; loadMembers(); }
    });
  } catch (e) {
    memberList.innerHTML = '<div class="muted">' + UI.t('加载失败') + '</div>';
  }
}

function mEl(tag, cls, text) {
  var n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text !== undefined) n.textContent = text;
  return n;
}

function memberRow(m) {
  var row = mEl('div', 'member-row');
  // 名字为悬停链接：弹出用户信息卡（脱敏）
  var nameSpan = document.createElement('span');
  nameSpan.className = 'owner-link grow';
  nameSpan.dataset.name = m.nickname || '';
  nameSpan.dataset.username = m.username || '';
  nameSpan.dataset.avatar = m.avatar || '';
  nameSpan.dataset.email = m.email || '';
  nameSpan.dataset.phone = m.phone || '';
  nameSpan.dataset.active = m.last_active || '';
  nameSpan.textContent = m.nickname + ' (' + m.username + ')';
  row.appendChild(nameSpan);
  // 只读模式（非属主查看）与属主行：展示角色文本，无操作按钮
  if (membersReadonly || m.role === 'owner') {
    var roleText = m.role === 'owner' ? UI.t('proj.ownerRole')
      : (m.role === 'edit' ? UI.t('proj.roleEdit') : UI.t('proj.roleView'));
    row.appendChild(mEl('span', 'tag', roleText));
    if (window.bindOwnerHover) window.bindOwnerHover(row);
    return row;
  }
  var sel = document.createElement('select');
  [['view', UI.t('proj.roleView')], ['edit', UI.t('proj.roleEdit')]].forEach(function (p) {
    var o = document.createElement('option');
    o.value = p[0];
    o.textContent = p[1];
    sel.appendChild(o);
  });
  sel.value = m.role;
  sel.addEventListener('change', async function () {
    var res = await fetch('/console/api/projects/' + memberProjectId + '/members/' + m.id, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
      body: JSON.stringify({ role: sel.value })
    });
    if (res.ok) { UI.toast(UI.t('已保存'), 'success'); return; }
    var d = await res.json().catch(function () { return {}; });
    UI.toast((d && d.error) || UI.t('更新失败'), 'error');
    loadMembers();
  });
  row.appendChild(sel);
  var del = mEl('button', 'btn btn-sm btn-danger', UI.t('proj.remove'));
  del.type = 'button';
  del.addEventListener('click', async function () {
    if (!await UI.confirm(UI.t('移除成员「{0}」？其将无法再访问项目内文档。', m.nickname + ' (' + m.username + ')'), { danger: true })) return;
    var res = await fetch('/console/api/projects/' + memberProjectId + '/members/' + m.id, {
      method: 'DELETE', headers: { 'X-Requested-With': 'XMLHttpRequest' }
    });
    if (res.ok) { loadMembers(); } else { UI.toast(UI.t('删除失败'), 'error'); }
  });
  row.appendChild(del);
  return row;
}

if (memberAddBtn) {
  memberAddBtn.addEventListener('click', async function () {
    var uid = memberUserId.value;
    if (!uid) { memberUserInput.focus(); return; }
    memberAddBtn.disabled = true;
    try {
      var res = await fetch('/console/api/projects/' + memberProjectId + '/members', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
        body: JSON.stringify({ user_id: parseInt(uid, 10), role: memberRole.value })
      });
      var d = await res.json();
      if (!res.ok) { UI.alert((d && d.error) || UI.t('创建失败')); return; }
      memberUserId.value = '';
      memberUserInput.value = '';
      await loadMembers();
    } catch (e) {
      UI.alert(UI.t('网络错误，创建失败'));
    } finally {
      memberAddBtn.disabled = false;
    }
  });
}

/* ---- 项目分享（属主/管理员）：整组文档对外分享，语义同文档分享 ---- */
// 与 share_modal.js 同构：开启/关闭、公开或密码访问、有效期、可编辑开关、访问申请审批。
// 差异：接口走 /console/api/projects/:id/share*，分享链接为 /ps/:token；密码记忆用独立前缀。
(function () {
  var modal = document.getElementById('projectShareModal');
  if (!modal || !window.UI) return;
  UI.bindModal(modal);

  var curId = 0, curName = '', curURL = '';
  function el(id) { return document.getElementById(id); }
  function t(s) { return UI.t(s); }

  function genPassword() {
    var chars = 'abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789';
    var out = '', buf = new Uint32Array(4);
    (window.crypto || window.msCrypto).getRandomValues(buf);
    for (var i = 0; i < 4; i++) out += chars[buf[i] % chars.length];
    return out;
  }
  function pwdKey(url) {
    var m = String(url || '').match(/\/ps\/([A-Za-z0-9]+)/);
    return m ? 'ds_pshare_pwd_' + m[1] : '';
  }
  function storedPwd(url) {
    try { return localStorage.getItem(pwdKey(url)) || ''; } catch (e) { return ''; }
  }

  function refreshType() {
    var isPwd = el('psTypePwd').checked;
    el('psPwdWrap').style.display = isPwd ? 'block' : 'none';
    // 访问申请仅对密码分享有意义：公开分享时隐藏
    el('psAccessReqBox').hidden = !(el('psEnabled').checked && isPwd);
  }
  function refreshLink() {
    var enabled = el('psEnabled').checked;
    el('psConfig').style.display = enabled ? 'block' : 'none';
    var box = el('psLinkBox');
    if (enabled && curURL) { el('psURL').textContent = curURL; box.style.display = 'flex'; }
    else { box.style.display = 'none'; }
    refreshType();
    if (enabled) loadAccessRequests();
  }

  function copyLink() {
    var url = el('psURL').textContent;
    var pwd = storedPwd(url);
    var lines = [];
    if (curName) lines.push(t('标题：') + curName);
    lines.push(t('链接：') + url);
    lines.push(t('访问密码：') + (pwd || t('无')));
    var text = lines.join('\n');
    var ok = function () { UI.toast(t('复制成功'), 'success'); };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(ok, function () { fallbackCopy(text); ok(); });
    } else { fallbackCopy(text); ok(); }
  }
  function fallbackCopy(text) {
    var ta = document.createElement('textarea');
    ta.value = text; ta.style.position = 'fixed'; ta.style.opacity = '0';
    document.body.appendChild(ta); ta.select();
    try { document.execCommand('copy'); } catch (e) { }
    ta.remove();
  }

  function save() {
    if (!curId) return;
    var enabled = el('psEnabled').checked;
    var isPwd = el('psTypePwd').checked;
    var payload = {
      enabled: enabled,
      can_edit: el('psCanEdit').checked,
      password: isPwd ? el('psPassword').value : '',
      remove_password: !isPwd, // 公开分享：清除已设密码
      expire_days: parseInt(el('psExpire').value, 10) || 0
    };
    var btn = el('psSaveBtn');
    btn.disabled = true;
    fetch('/console/api/projects/' + curId + '/share', {
      method: enabled ? 'POST' : 'DELETE',
      headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
      body: JSON.stringify(payload)
    }).then(function (res) {
      return res.json().catch(function () { return {}; }).then(function (d) {
        if (!res.ok) throw new Error((d && d.error) || t('操作失败'));
        return d;
      });
    }).then(function (d) {
      if (enabled && isPwd && payload.password.trim() && d.url) {
        try { localStorage.setItem(pwdKey(d.url), payload.password.trim()); } catch (e) { }
      }
      if (enabled && !isPwd && d.url) {
        try { localStorage.removeItem(pwdKey(d.url)); } catch (e) { }
      }
      if (d.url) curURL = d.url;
      if (enabled && typeof d.hasPassword === 'boolean') {
        el('psPassword').placeholder = d.hasPassword ? t('已设置密码') : '';
      }
      UI.toast(t(enabled ? '分享设置已更新' : '已关闭分享'), 'success');
      refreshLink();
    }).catch(function (err) {
      UI.toast(err.message || t('common.netErr'), 'error');
    }).finally(function () { btn.disabled = false; });
  }

  /* ---- 访问申请审批列表（有密码分享） ---- */
  var statusMap = null;
  function loadAccessRequests() {
    var list = el('psAccessReqList');
    if (!curId || !el('psEnabled').checked || !el('psTypePwd').checked) return;
    list.innerHTML = '<div class="muted">' + t('加载中…') + '</div>';
    fetch('/console/api/projects/' + curId + '/access-requests', {
      headers: { 'X-Requested-With': 'XMLHttpRequest' }
    }).then(function (res) {
      return res.json().catch(function () { return {}; }).then(function (d) { return { ok: res.ok, d: d }; });
    }).then(function (out) {
      if (!out.ok) { list.innerHTML = '<div class="muted">' + t((out.d && out.d.error) || '加载失败') + '</div>'; return; }
      var items = (out.d && out.d.data) || [];
      if (!items.length) { list.innerHTML = '<div class="muted">' + t('share.applyListEmpty') + '</div>'; return; }
      statusMap = statusMap || {
        0: t('share.applyStatusPending'), 1: t('share.applyStatusApproved'), 2: t('share.applyStatusRejected')
      };
      var table = document.createElement('table');
      table.className = 'table';
      table.innerHTML =
        '<thead><tr>' +
        '<th>' + t('dash.pendingName') + '</th>' +
        '<th>' + t('dash.pendingTime') + '</th>' +
        '<th>' + t('common.status') + '</th>' +
        '<th class="col-actions">' + t('common.actions') + '</th>' +
        '</tr></thead>';
      var tbody = document.createElement('tbody');
      items.forEach(function (r) { tbody.appendChild(applyRow(r)); });
      table.appendChild(tbody);
      list.innerHTML = '';
      list.appendChild(table);
    }).catch(function () {
      list.innerHTML = '<div class="muted">' + t('加载失败') + '</div>';
    });
  }

  function applyRow(r) {
    var tr = document.createElement('tr');
    var tdName = document.createElement('td'); tdName.textContent = r.name || '—'; tr.appendChild(tdName);
    var tdTime = document.createElement('td'); tdTime.className = 'cell-muted'; tdTime.textContent = r.created_at || '—'; tr.appendChild(tdTime);
    var tdStatus = document.createElement('td');
    var tag = document.createElement('span');
    tag.className = 'tag' + (r.status === 1 ? ' tag-green' : '');
    tag.textContent = statusMap[r.status] || String(r.status);
    tdStatus.appendChild(tag); tr.appendChild(tdStatus);
    var tdActs = document.createElement('td'); tdActs.className = 'col-actions';
    if (r.status === 0) {
      var okB = document.createElement('button');
      okB.type = 'button'; okB.className = 'btn btn-sm btn-primary'; okB.textContent = t('share.applyApprove');
      okB.addEventListener('click', function () { review(r.id, 'approve', tdActs); });
      var noB = document.createElement('button');
      noB.type = 'button'; noB.className = 'btn btn-sm'; noB.textContent = t('share.applyReject');
      noB.addEventListener('click', function () { review(r.id, 'reject', tdActs); });
      tdActs.appendChild(okB); tdActs.appendChild(noB);
    } else { tdActs.textContent = '—'; }
    tr.appendChild(tdActs);
    return tr;
  }

  function review(rid, action, acts) {
    if (acts) acts.querySelectorAll('button').forEach(function (b) { b.disabled = true; });
    fetch('/console/api/projects/' + curId + '/access-requests/' + rid, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
      body: JSON.stringify({ action: action })
    }).then(function (res) {
      return res.json().catch(function () { return {}; }).then(function (d) { return { ok: res.ok, d: d }; });
    }).then(function (out) {
      if (!out.ok) {
        UI.toast(t((out.d && out.d.error) || '操作失败'), 'error');
        if (acts) acts.querySelectorAll('button').forEach(function (b) { b.disabled = false; });
        return;
      }
      UI.toast(t('已保存'), 'success');
      loadAccessRequests();
    }).catch(function () {
      UI.toast(t('common.netErr'), 'error');
      if (acts) acts.querySelectorAll('button').forEach(function (b) { b.disabled = false; });
    });
  }

  function populate(cfg) {
    var enabled = !!cfg.enabled;
    el('psEnabled').checked = enabled;
    el('psCanEdit').checked = !!cfg.can_edit;
    el('psPassword').value = '';
    el('psPassword').placeholder = cfg.has_password ? t('已设置密码') : '';
    el('psGenPwd').textContent = t(cfg.has_password ? 'share.resetPwd' : 'share.genPwd');
    el('psExpire').value = '0';
    el('psTypePwd').checked = !!cfg.has_password; // 有密码 → 密码访问；无密码 → 公开访问
    el('psTypePublic').checked = !cfg.has_password;
    curURL = cfg.url || '';
    refreshLink();
  }

  // onclick 属性调用，需挂在全局
  window.openProjectShare = function (btn) {
    curId = btn.dataset.id;
    curName = btn.dataset.name || '';
    el('psProjName').textContent = curName ? ' · ' + curName : '';
    el('psConfig').style.display = 'none';
    UI.openModal(modal);
    // 分享配置按需拉取：列表页不预载每行分享状态
    fetch('/console/api/projects/' + curId + '/share', { headers: { 'X-Requested-With': 'XMLHttpRequest' } })
      .then(function (r) { return r.json(); })
      .then(function (resp) { populate((resp && resp.data) || { enabled: false }); })
      .catch(function () { populate({ enabled: false }); });
  };

  el('psGenPwd').addEventListener('click', function () { el('psPassword').value = genPassword(); });
  el('psCopyBtn').addEventListener('click', copyLink);
  el('psEnabled').addEventListener('change', refreshLink);
  el('psTypePublic').addEventListener('change', refreshType);
  el('psTypePwd').addEventListener('change', refreshType);
  el('psSaveBtn').addEventListener('click', save);
  el('psRefreshReq').addEventListener('click', loadAccessRequests);
})();
