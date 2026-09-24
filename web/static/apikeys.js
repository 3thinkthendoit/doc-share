// API 密钥管理页交互
(function () {
  var keyModal = document.getElementById('keyModal');
  var secretModal = document.getElementById('secretModal');
  var keyForm = document.getElementById('keyForm');
  var btnNew = document.getElementById('btnNewKey');
  UI.bindModal(keyModal);
  UI.bindModal(secretModal);

  if (btnNew) {
    btnNew.addEventListener('click', function () {
      keyForm.reset();
      UI.openModal(keyModal);
      var first = keyForm.querySelector('input[name=name]');
      if (first) first.focus();
    });
  }

  // 展示 AppKey + Secret（创建/重置后各一次）；记录密钥名称供下载文件用
  var currentKeyName = '';
  function showSecret(appKey, secret, title, name) {
    document.getElementById('outAppKey').value = appKey;
    document.getElementById('outSecret').value = secret;
    document.getElementById('secretTitle').textContent = UI.t(title);
    currentKeyName = name || '';
    UI.openModal(secretModal);
  }

  // 下载密钥凭证为 txt 文件（Secret 仅此一次，防止关闭弹窗后丢失）
  document.getElementById('btnDownloadKey').addEventListener('click', function () {
    var appKey = document.getElementById('outAppKey').value;
    var secret = document.getElementById('outSecret').value;
    if (!appKey || !secret) return;
    var lines = [
      'DocShare API Key',
      '----------------------------------------',
      'Name: ' + (currentKeyName || '-'),
      'AppKey: ' + appKey,
      'Secret: ' + secret,
      'Created: ' + new Date().toISOString(),
      '',
      UI.t('Secret 仅显示这一次，离开后无法再次查看；遗失请使用「重置密钥」重新生成。'),
    ];
    var blob = new Blob([lines.join('\r\n') + '\r\n'], { type: 'text/plain;charset=utf-8' });
    var a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    var safeName = (currentKeyName || 'key').replace(/[\\/:*?"<>|\s]+/g, '-');
    a.download = 'docshare-api-key-' + safeName + '.txt';
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(a.href);
  });

  if (keyForm) {
    keyForm.addEventListener('submit', async function (e) {
      e.preventDefault();
      if (!UI.validateForm(keyForm)) return;
      var name = new FormData(keyForm).get('name');
      try {
        var res = await fetch('/admin/api/apikeys', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
          body: JSON.stringify({ name: name }),
        });
        var data = await res.json();
        if (res.ok) {
          UI.closeModal(keyModal);
          showSecret(data.data.app_key, data.secret, '密钥已生成', name);
        } else {
          UI.alert((data && data.error) || '创建失败');
        }
      } catch (err) {
        UI.alert('创建失败：网络错误或服务不可用');
      }
    });
  }

  // 复制按钮（secretModal 内 data-copy 指向目标 input id）
  document.addEventListener('click', async function (e) {
    var btn = e.target.closest('[data-copy]');
    if (!btn) return;
    var input = document.getElementById(btn.dataset.copy);
    if (!input || !input.value) return;
    try {
      await navigator.clipboard.writeText(input.value);
      UI.toast('已复制到剪贴板', 'success');
    } catch (err) {
      input.select();
      document.execCommand('copy');
      UI.toast('已复制到剪贴板', 'success');
    }
  });

  window.resetKey = async function (btn) {
    var id = btn.dataset.id;
    if (!await UI.confirm(UI.t('重置后旧 Secret 立即失效，确认重置「{0}」？', btn.dataset.name))) return;
    try {
      var res = await fetch('/admin/api/apikeys/' + id + '/reset', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
      });
      var data = await res.json();
      if (res.ok) {
        showSecret(btn.closest('tr').children[1].textContent.trim(), data.secret, '密钥已重置', btn.dataset.name);
      } else {
        UI.alert((data && data.error) || '重置失败');
      }
    } catch (err) {
      UI.alert('重置失败：网络错误或服务不可用');
    }
  };

  window.toggleKey = async function (btn) {
    var id = btn.dataset.id;
    var status = parseInt(btn.dataset.status, 10);
    try {
      var res = await fetch('/admin/api/apikeys/' + id + '/status', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
        body: JSON.stringify({ status: status }),
      });
      var data = await res.json();
      if (res.ok) {
        location.reload();
      } else {
        UI.alert((data && data.error) || '操作失败');
      }
    } catch (err) {
      UI.alert('操作失败：网络错误或服务不可用');
    }
  };

  window.delKey = async function (btn) {
    var id = btn.dataset.id;
    if (!await UI.confirm(UI.t('删除后该密钥立即失效且不可恢复，确认删除「{0}」？', btn.dataset.name))) return;
    try {
      var res = await fetch('/admin/api/apikeys/' + id, {
        method: 'DELETE',
        headers: { 'X-Requested-With': 'XMLHttpRequest' },
      });
      var data = await res.json();
      if (res.ok) {
        btn.closest('tr').remove();
        UI.toast('已删除', 'success');
      } else {
        UI.alert((data && data.error) || '删除失败');
      }
    } catch (err) {
      UI.alert('删除失败：网络错误或服务不可用');
    }
  };
})();
