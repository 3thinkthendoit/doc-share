// 授权登录（OAuth 应用）管理页交互
(function () {
  var appModal = document.getElementById('appModal');
  var secretModal = document.getElementById('appSecretModal');
  var appForm = document.getElementById('appForm');
  UI.bindModal(appModal);
  UI.bindModal(secretModal);
  // 新建应用后，关闭密钥弹窗再刷新列表（先让用户看到并保存 Secret）
  var reloadOnClose = false;
  secretModal.addEventListener('click', function (e) {
    if (reloadOnClose && e.target.closest('[data-close-modal]')) location.reload();
  });

  function api(method, url, body) {
    var opts = { method: method, headers: { 'X-Requested-With': 'XMLHttpRequest' } };
    if (body !== undefined) {
      opts.headers['Content-Type'] = 'application/json';
      opts.body = JSON.stringify(body);
    }
    return fetch(url, opts).then(function (res) {
      return res.json().catch(function () { return {}; }).then(function (data) {
        if (!res.ok) throw new Error((data && data.error) || UI.t('oauthsrv.opFail'));
        return data;
      });
    });
  }

  function openForm(title, v) {
    appForm.reset();
    appForm.id.value = v.id || '';
    appForm.name.value = v.name || '';
    appForm.homepage_url.value = v.homepage || '';
    appForm.redirect_uris.value = v.uris || '';
    // 应用类型创建后不可改：编辑时隐藏
    document.getElementById('appTypeField').hidden = !!v.id;
    document.getElementById('appModalTitle').textContent = UI.t(title);
    UI.openModal(appModal);
    appForm.name.focus();
  }

  var currentName = '';
  function showSecret(clientId, secret, title, name) {
    document.getElementById('outClientId').value = clientId;
    document.getElementById('outClientSecret').value = secret || '';
    // 公开客户端没有 Secret：只展示 Client ID
    document.getElementById('outClientSecretRow').hidden = !secret;
    document.getElementById('appSecretWarn').textContent = UI.t(secret ? 'oauthsrv.secretWarn' : 'oauthsrv.publicCreated');
    document.getElementById('appSecretTitle').textContent = UI.t(title);
    currentName = name || '';
    UI.openModal(secretModal);
  }

  document.getElementById('btnNewApp').addEventListener('click', function () {
    openForm('oauthsrv.newApp', {});
  });

  appForm.addEventListener('submit', async function (e) {
    e.preventDefault();
    if (!UI.validateForm(appForm)) return;
    var id = appForm.id.value;
    var body = {
      name: appForm.name.value.trim(),
      homepage_url: appForm.homepage_url.value.trim(),
      redirect_uris: appForm.redirect_uris.value,
    };
    try {
      if (id) {
        var r = await api('PUT', '/console/api/oauth-apps/' + id, body);
        UI.closeModal(appModal);
        if (r.resubmitted) await UI.alert(UI.t('oauthsrv.resubmitted'));
        location.reload();
      } else {
        body.client_type = appForm.client_type.value;
        var data = await api('POST', '/console/api/oauth-apps', body);
        UI.closeModal(appModal);
        showSecret(data.data.client_id, data.client_secret, 'oauthsrv.secretTitle', body.name);
        reloadOnClose = true;
      }
    } catch (err) {
      UI.alert(err.message);
    }
  });

  // 复制（弹窗内 data-copy 指向 input id）
  secretModal.addEventListener('click', async function (e) {
    var btn = e.target.closest('[data-copy]');
    if (!btn) return;
    var input = document.getElementById(btn.dataset.copy);
    if (!input || !input.value) return;
    try {
      await navigator.clipboard.writeText(input.value);
    } catch (err) {
      input.select();
      document.execCommand('copy');
    }
    UI.toast(UI.t('已复制到剪贴板'), 'success');
  });

  // 下载凭证（Secret 只显示这一次）
  document.getElementById('btnDownloadApp').addEventListener('click', function () {
    var id = document.getElementById('outClientId').value;
    var secret = document.getElementById('outClientSecret').value;
    if (!id) return; // 公开客户端没有 Secret，也允许下载
    var ep = window.__OAUTH_EP || {};
    var lines = [
      'DocShare OAuth / OIDC App',
      '----------------------------------------',
      'Name: ' + (currentName || '-'),
      'Client ID: ' + id,
      secret ? 'Client Secret: ' + secret : 'Client type: public (no secret; PKCE S256 required)',
      'Issuer: ' + (ep.issuer || ''),
      'Discovery: ' + (ep.discovery || ''),
      'Authorize URL: ' + (ep.authorize || ''),
      'Token URL: ' + (ep.token || ''),
      'Userinfo URL: ' + (ep.userinfo || ''),
      'Scopes: openid profile email offline_access',
      'Created: ' + new Date().toISOString(),
      '',
      UI.t(secret ? 'oauthsrv.secretWarn' : 'oauthsrv.publicCreated'),
    ];
    var blob = new Blob([lines.join('\r\n') + '\r\n'], { type: 'text/plain;charset=utf-8' });
    var a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = 'docshare-oauth-' + (currentName || id).replace(/[\\/:*?"<>|\s]+/g, '-') + '.txt';
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(a.href);
  });

  window.editApp = function (btn) {
    openForm('oauthsrv.editApp', {
      id: btn.dataset.id, name: btn.dataset.name, homepage: btn.dataset.homepage, uris: btn.dataset.uris,
    });
  };

  window.rotateApp = async function (btn) {
    if (!await UI.confirm(UI.t('oauthsrv.confirmRotate', btn.dataset.name), { danger: true })) return;
    try {
      var data = await api('POST', '/console/api/oauth-apps/' + btn.dataset.id + '/rotate-secret');
      showSecret(data.client_id, data.client_secret, 'oauthsrv.secretRotated', btn.dataset.name);
    } catch (err) {
      UI.alert(err.message);
    }
  };

  window.delApp = async function (btn) {
    if (!await UI.confirm(UI.t('oauthsrv.confirmDelete', btn.dataset.name), { danger: true })) return;
    try {
      await api('DELETE', '/console/api/oauth-apps/' + btn.dataset.id);
      btn.closest('tr').remove();
      UI.toast(UI.t('已删除'), 'success');
    } catch (err) {
      UI.alert(err.message);
    }
  };

  window.reviewApp = async function (btn) {
    var action = btn.dataset.action;
    var reason = '';
    if (action === 'reject' || action === 'disable') {
      var msg = action === 'reject' ? 'oauthsrv.promptReject' : 'oauthsrv.promptDisable';
      var v = await UI.prompt(UI.t(msg, btn.dataset.name), 'text', UI.t('oauthsrv.reasonPh'));
      if (v === null) return;
      reason = String(v);
    } else if (!await UI.confirm(UI.t('oauthsrv.confirmApprove', btn.dataset.name))) {
      return;
    }
    try {
      await api('POST', '/console/api/oauth-apps/' + btn.dataset.id + '/review', { action: action, reason: reason });
      location.reload();
    } catch (err) {
      UI.alert(err.message);
    }
  };

  window.revokeConsent = async function (btn) {
    if (!await UI.confirm(UI.t('oauthsrv.confirmRevoke', btn.dataset.name), { danger: true })) return;
    try {
      await api('DELETE', '/console/api/oauth-consents/' + encodeURIComponent(btn.dataset.client));
      btn.closest('tr').remove();
      UI.toast(UI.t('oauthsrv.revoked'), 'success');
    } catch (err) {
      UI.alert(err.message);
    }
  };
})();
