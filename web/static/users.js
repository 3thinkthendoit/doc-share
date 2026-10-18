// 用户管理页交互
(function () {
  var modal = document.getElementById('createUserModal');
  var createBtn = document.getElementById('createUserBtn');
  UI.bindModal(modal);
  if (createBtn) {
    createBtn.addEventListener('click', function () {
      UI.openModal(modal);
      var first = modal.querySelector('input[name=username]');
      if (first) first.focus();
    });
  }

  var createForm = document.getElementById('createForm');
  var createAvatar = UI.avatarPicker(createForm && createForm.querySelector('[data-avatar-pick]'));
  if (createForm) {
    createForm.addEventListener('submit', async function (e) {
      e.preventDefault();
      if (!UI.validateForm(createForm)) return;
      var fd = new FormData(createForm);
      var payload = {
        username: fd.get('username'),
        nickname: fd.get('nickname'),
        password: fd.get('password'),
        role: fd.get('role'),
        avatar: fd.get('avatar'),
      };
      try {
        var res = await fetch('/console/api/users', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
          body: JSON.stringify(payload),
        });
        var data = await res.json();
        if (res.ok) {
          UI.closeModal(modal);
          UI.toast('用户创建成功', 'success');
          setTimeout(function () { location.reload(); }, 500);
        } else {
          UI.alert((data && data.error) || '创建失败');
        }
      } catch (err) {
        UI.alert('创建失败：网络错误或服务不可用');
      }
    });
  }

  // 编辑用户弹窗
  var editModal = document.getElementById('editUserModal');
  var editForm = document.getElementById('editForm');
  var editAvatar = UI.avatarPicker(editForm && editForm.querySelector('[data-avatar-pick]'));
  var editID = 0;
  UI.bindModal(editModal);
  window.editUser = function (id, username, nickname, role, status, avatar, email, phone) {
    editID = id;
    document.getElementById('editUsername').value = username || '';
    editForm.querySelector('input[name=nickname]').value = nickname || '';
    editForm.querySelector('input[name=email]').value = email || '';
    editForm.querySelector('input[name=phone]').value = phone || '';
    if (editAvatar) editAvatar.set(avatar || '');
    var roleSel = editForm.querySelector('select[name=role]');
    var statusSel = editForm.querySelector('select[name=status]');
    roleSel.value = role;
    statusSel.value = String(status);
    // 程序化改 value 后同步自定义下拉显示
    UI.syncSelect(roleSel);
    UI.syncSelect(statusSel);
    UI.openModal(editModal);
  };
  if (editForm) {
    editForm.addEventListener('submit', async function (e) {
      e.preventDefault();
      var fd = new FormData(editForm);
      var payload = {
        nickname: fd.get('nickname'),
        role: fd.get('role'),
        status: parseInt(fd.get('status'), 10),
        avatar: fd.get('avatar'),
        email: (fd.get('email') || '').trim(),
        phone: (fd.get('phone') || '').trim(),
      };
      try {
        var res = await fetch('/console/api/users/' + editID, {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
          body: JSON.stringify(payload),
        });
        var data = await res.json();
        if (res.ok) {
          UI.closeModal(editModal);
          UI.toast('已保存', 'success');
          setTimeout(function () { location.reload(); }, 400);
        } else {
          UI.alert((data && data.error) || '保存失败');
        }
      } catch (err) {
        UI.alert('保存失败：网络错误或服务不可用');
      }
    });
  }
})();

function rowEl(id) { return document.querySelector('tr[data-id="' + id + '"]'); }

(function () {
  var table = document.querySelector('main .table');
  if (!table) return;
  function closeMenus() {
    table.querySelectorAll('.row-menu-pop').forEach(function (pop) {
      pop.hidden = true;
      var b = pop.parentNode.querySelector('.row-menu-btn');
      if (b) b.setAttribute('aria-expanded', 'false');
    });
  }
  function openMenu(menu) {
    closeMenus();
    var btn = menu.querySelector('.row-menu-btn');
    var pop = menu.querySelector('.row-menu-pop');
    pop.hidden = false;
    btn.setAttribute('aria-expanded', 'true');
    var r = btn.getBoundingClientRect();
    var w = pop.offsetWidth || 148;
    var left = Math.max(8, r.right - w);
    var top = r.bottom + 4;
    if (top + pop.offsetHeight > window.innerHeight - 8) top = Math.max(8, r.top - pop.offsetHeight - 4);
    pop.style.top = top + 'px';
    pop.style.left = left + 'px';
  }
  document.addEventListener('click', function (e) {
    if (!e.target.closest('.row-menu')) closeMenus();
  });
  window.addEventListener('scroll', closeMenus, true);
  window.addEventListener('resize', closeMenus);
  table.addEventListener('click', function (e) {
    var toggle = e.target.closest('.row-menu-btn');
    if (toggle) {
      var menu = toggle.closest('.row-menu');
      var pop = menu.querySelector('.row-menu-pop');
      if (pop.hidden) openMenu(menu); else closeMenus();
      return;
    }
    var btn = e.target.closest('[data-act]');
    if (!btn || !btn.closest('.row-menu')) return;
    closeMenus();
    var tr = btn.closest('tr[data-id]');
    if (!tr) return;
    var id = tr.getAttribute('data-id');
    var act = btn.getAttribute('data-act');
    if (act === 'edit') {
      editUser(id, tr.getAttribute('data-username'), tr.getAttribute('data-nickname'), tr.getAttribute('data-role'), tr.getAttribute('data-status'), tr.getAttribute('data-avatar'), tr.getAttribute('data-email'), tr.getAttribute('data-phone'));
    } else if (act === 'pwd') resetPwd(id);
    else if (act === 'unlock') unlockUser(id);
    else if (act === 'totp') resetTOTP(id);
    else if (act === 'del') delUser(id);
  });
})();

async function resetPwd(id) {
  var pwd = await UI.prompt('输入新密码（至少 6 位）', 'password', '新密码');
  if (!pwd) return;
  var res = await fetch('/console/api/users/' + id, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
    body: JSON.stringify({ password: pwd }),
  });
  var data = await res.json();
  if (res.ok) { UI.toast('密码已重置', 'success'); }
  else { UI.alert((data && data.error) || '重置失败'); }
}

async function unlockUser(id) {
  if (!await UI.confirm(UI.t('user.unlockConfirm'))) return;
  var res = await fetch('/console/api/users/' + id + '/unlock', {
    method: 'POST',
    headers: { 'X-Requested-With': 'XMLHttpRequest' },
  });
  var data = await res.json().catch(function () { return {}; });
  if (res.ok) {
    UI.toast(UI.t('user.unlocked'), 'success');
    setTimeout(function () { location.reload(); }, 400);
  } else {
    UI.alert((data && data.error) || UI.t('user.unlockFail'));
  }
}

async function resetTOTP(id) {
  if (!await UI.confirm(UI.t('user.resetTotpConfirm'))) return;
  var res = await fetch('/console/api/users/' + id + '/reset-totp', {
    method: 'POST',
    headers: { 'X-Requested-With': 'XMLHttpRequest' },
  });
  var data = await res.json().catch(function () { return {}; });
  if (res.ok) {
    UI.toast(UI.t('user.resetTotpOk'), 'success');
    setTimeout(function () { location.reload(); }, 400);
  } else {
    UI.alert((data && data.error) || UI.t('user.resetTotpFail'));
  }
}

async function delUser(id) {
  if (!await UI.confirm('确认删除该用户？', { danger: true })) return;
  var res = await fetch('/console/api/users/' + id, {
    method: 'DELETE',
    headers: { 'X-Requested-With': 'XMLHttpRequest' },
  });
  var data = await res.json();
  if (res.ok) { rowEl(id).remove(); UI.toast('已删除', 'success'); } else { UI.alert((data && data.error) || '删除失败'); }
}
