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
    var res = await fetch(id ? '/admin/api/projects/' + id : '/admin/api/projects', {
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
    var res = await fetch('/admin/api/projects/' + btn.dataset.id, {
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

if (membersModal) UI.bindModal(membersModal);

// onclick 属性调用，需挂在全局
window.openMembers = function (btn) {
  memberProjectId = btn.dataset.id;
  membersReadonly = btn.dataset.readonly === '1';
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
      var res = await fetch('/admin/api/projects/' + id + '/members/me', {
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
  fetch('/admin/api/users/options?q=' + encodeURIComponent(q), {
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
    var res = await fetch('/admin/api/projects/' + memberProjectId + '/members', {
      headers: { 'X-Requested-With': 'XMLHttpRequest' }
    });
    var data = await res.json();
    var ms = (data && data.members) || [];
    memberList.innerHTML = '';
    if (!ms.length) {
      memberList.appendChild(mEl('div', 'muted', UI.t('暂无成员')));
      return;
    }
    ms.forEach(function (m) { memberList.appendChild(memberRow(m)); });
    if (window.bindOwnerHover) window.bindOwnerHover(memberList);
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
    var res = await fetch('/admin/api/projects/' + memberProjectId + '/members/' + m.id, {
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
    var res = await fetch('/admin/api/projects/' + memberProjectId + '/members/' + m.id, {
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
      var res = await fetch('/admin/api/projects/' + memberProjectId + '/members', {
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
