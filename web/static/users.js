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
        var res = await fetch('/admin/api/users', {
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
  window.editUser = function (id, username, nickname, role, status, avatar) {
    editID = id;
    document.getElementById('editUsername').value = username || '';
    editForm.querySelector('input[name=nickname]').value = nickname || '';
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
      };
      try {
        var res = await fetch('/admin/api/users/' + editID, {
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

async function resetPwd(id) {
  var pwd = await UI.prompt('输入新密码（至少 6 位）', 'password', '新密码');
  if (!pwd) return;
  var res = await fetch('/admin/api/users/' + id, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
    body: JSON.stringify({ password: pwd }),
  });
  var data = await res.json();
  if (res.ok) { UI.toast('密码已重置', 'success'); }
  else { UI.alert((data && data.error) || '重置失败'); }
}

async function delUser(id) {
  if (!await UI.confirm('确认删除该用户？')) return;
  var res = await fetch('/admin/api/users/' + id, {
    method: 'DELETE',
    headers: { 'X-Requested-With': 'XMLHttpRequest' },
  });
  var data = await res.json();
  if (res.ok) { rowEl(id).remove(); UI.toast('已删除', 'success'); } else { UI.alert((data && data.error) || '删除失败'); }
}
