// 分享设置弹窗（公共）：配合模板片段 shareSettingsModal 使用。
// ShareModal.bind({ lockCanEdit, onChanged }) 初始化；
// ShareModal.openFor({ docId, title, shared, canEdit, hasPwd, token, shareURL }) 打开并填充状态。
// 保存走统一接口 POST/DELETE /admin/api/docs/:id/share；带密码分享的密码会记忆到 localStorage 便于复制。
// 有密码分享时展示「申请查看」审批列表（与旧列表页实现同接口）。
window.ShareModal = (function () {
  'use strict';

  var opts = null;
  var opened = null; // 当前弹窗对应的文档状态

  function t(s) { return window.UI && UI.t ? UI.t(s) : s; }
  function el(id) { return document.getElementById(id); }

  function sharePwdKey(url) {
    var m = String(url || '').match(/\/s\/([A-Za-z0-9]+)/);
    return m ? 'ds_share_pwd_' + m[1] : '';
  }
  function storedSharePwd(url) {
    try { return localStorage.getItem(sharePwdKey(url)) || ''; } catch (e) { return ''; }
  }

  function genPassword() {
    var chars = 'abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789';
    var out = '', buf = new Uint32Array(6);
    (window.crypto || window.msCrypto).getRandomValues(buf);
    for (var i = 0; i < 6; i++) out += chars[buf[i] % chars.length];
    return out;
  }

  function refreshLink() {
    var enabled = el('shareEnabled').checked;
    el('shareConfig').style.display = enabled ? 'block' : 'none';
    var reqBox = el('accessReqBox');
    if (reqBox) reqBox.hidden = !enabled;
    var linkEl = el('shareLink');
    if (enabled && opened && opened.shareURL) {
      el('shareURL').textContent = opened.shareURL;
      linkEl.style.display = 'flex';
    } else {
      linkEl.style.display = 'none';
    }
    if (enabled && opened && opened.docId) loadAccessRequests(opened.docId);
  }

  function copyShare() {
    var url = el('shareURL').textContent;
    var text = url;
    var pwd = storedSharePwd(url);
    if (pwd) text += '\n' + t('访问密码') + ': ' + pwd;
    navigator.clipboard.writeText(text).then(function () {
      if (window.UI) UI.toast(t('已复制'), 'success');
    }).catch(function () {
      if (window.UI) UI.toast(t('复制失败'), 'error');
    });
  }

  function save() {
    if (!opened || !opened.docId) return;
    var enabledEl = el('shareEnabled');
    var payload = {
      enabled: enabledEl.checked,
      can_edit: el('shareCanEdit') ? el('shareCanEdit').checked : false,
      password: el('sharePassword').value,
      expire_days: parseInt(el('shareExpire').value, 10) || 0
    };
    var saveBtn = el('saveShareBtn');
    saveBtn.disabled = true;
    fetch('/admin/api/docs/' + opened.docId + '/share', {
      method: enabledEl.checked ? 'POST' : 'DELETE',
      headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
      body: JSON.stringify(payload)
    }).then(function (res) {
      return res.json().catch(function () { return {}; }).then(function (data) {
        if (!res.ok) throw new Error((data && data.error) || t('操作失败'));
        return data;
      });
    }).then(function (data) {
      if (enabledEl.checked && payload.password.trim() && data.url) {
        try { localStorage.setItem(sharePwdKey(data.url), payload.password.trim()); } catch (e) {}
      }
      if (data.url) opened.shareURL = data.url;
      opened.shared = enabledEl.checked;
      opened.hasPwd = !!(payload.password.trim() || opened.hasPwd);
      if (window.UI) UI.toast(t(enabledEl.checked ? '分享设置已更新' : '已关闭分享'), 'success');
      if (typeof opts.onChanged === 'function') opts.onChanged(enabledEl.checked, opened);
    }).catch(function (err) {
      if (window.UI) UI.toast(err.message || t('common.netErr'), 'error');
    }).finally(function () {
      saveBtn.disabled = false;
    });
  }

  /* ---- 申请查看审批列表（有密码分享） ---- */
  var statusMap = null;
  function loadAccessRequests(docId) {
    var list = el('accessReqList');
    if (!list || !docId) return;
    list.innerHTML = '<div class="muted">' + t('加载中…') + '</div>';
    fetch('/admin/api/docs/' + docId + '/access-requests', {
      headers: { 'X-Requested-With': 'XMLHttpRequest' }
    }).then(function (res) {
      return res.json().catch(function () { return {}; }).then(function (data) {
        return { ok: res.ok, data: data };
      });
    }).then(function (out) {
      if (!out.ok) {
        list.innerHTML = '<div class="muted">' + t((out.data && out.data.error) || '加载失败') + '</div>';
        return;
      }
      var items = (out.data && out.data.data) || [];
      if (!items.length) {
        list.innerHTML = '<div class="muted">' + t('share.applyListEmpty') + '</div>';
        return;
      }
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
      items.forEach(function (r) {
        tbody.appendChild(applyRow(docId, r));
      });
      table.appendChild(tbody);
      list.innerHTML = '';
      list.appendChild(table);
    }).catch(function () {
      list.innerHTML = '<div class="muted">' + t('加载失败') + '</div>';
    });
  }

  function applyRow(docId, r) {
    var tr = document.createElement('tr');
    var tdName = document.createElement('td');
    tdName.textContent = r.name || '—';
    tr.appendChild(tdName);
    var tdTime = document.createElement('td');
    tdTime.className = 'cell-muted';
    tdTime.textContent = r.created_at || '—';
    tr.appendChild(tdTime);
    var tdStatus = document.createElement('td');
    var tag = document.createElement('span');
    tag.className = 'tag' + (r.status === 1 ? ' tag-green' : '');
    tag.textContent = statusMap[r.status] || String(r.status);
    tdStatus.appendChild(tag);
    tr.appendChild(tdStatus);
    var tdActs = document.createElement('td');
    tdActs.className = 'col-actions';
    if (r.status === 0) {
      var ok = document.createElement('button');
      ok.type = 'button';
      ok.className = 'btn btn-sm btn-primary';
      ok.textContent = t('share.applyApprove');
      ok.addEventListener('click', function () { reviewAccess(docId, r.id, 'approve', tdActs); });
      var no = document.createElement('button');
      no.type = 'button';
      no.className = 'btn btn-sm';
      no.textContent = t('share.applyReject');
      no.addEventListener('click', function () { reviewAccess(docId, r.id, 'reject', tdActs); });
      tdActs.appendChild(ok);
      tdActs.appendChild(no);
    } else {
      tdActs.textContent = '—';
    }
    tr.appendChild(tdActs);
    return tr;
  }

  function reviewAccess(docId, rid, action, acts) {
    if (acts) acts.querySelectorAll('button').forEach(function (b) { b.disabled = true; });
    fetch('/admin/api/docs/' + docId + '/access-requests/' + rid, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
      body: JSON.stringify({ action: action })
    }).then(function (res) {
      return res.json().catch(function () { return {}; }).then(function (data) {
        return { ok: res.ok, data: data };
      });
    }).then(function (out) {
      if (!out.ok) {
        if (window.UI) UI.toast(t((out.data && out.data.error) || '操作失败'), 'error');
        if (acts) acts.querySelectorAll('button').forEach(function (b) { b.disabled = false; });
        return;
      }
      if (window.UI) UI.toast(t('操作成功'), 'success');
      loadAccessRequests(docId);
    }).catch(function () {
      if (window.UI) UI.toast(t('common.netErr'), 'error');
      if (acts) acts.querySelectorAll('button').forEach(function (b) { b.disabled = false; });
    });
  }

  function bind(o) {
    opts = o || {};
    var modal = el('shareModal');
    if (!modal || !window.UI) return;
    UI.bindModal(modal);
    el('genPwdBtn').addEventListener('click', function () { el('sharePassword').value = genPassword(); });
    el('copyShareBtn').addEventListener('click', copyShare);
    el('shareEnabled').addEventListener('change', refreshLink);
    el('saveShareBtn').addEventListener('click', save);
    var refresh = el('refreshAccessReq');
    if (refresh) refresh.addEventListener('click', function () {
      if (opened) loadAccessRequests(opened.docId);
    });
  }

  function openFor(state) {
    var modal = el('shareModal');
    if (!modal || !window.UI) return;
    if (!state || !state.docId) {
      UI.toast(t('请先保存文档后再分享'), 'info');
      return;
    }
    opened = state;
    el('shDocTitle').textContent = state.title ? ' · ' + state.title : '';
    var enabled = !!state.shared;
    el('shareEnabled').checked = enabled;
    var canEditEl = el('shareCanEdit');
    if (canEditEl) canEditEl.checked = !!state.canEdit;
    var pwdEl = el('sharePassword');
    pwdEl.value = '';
    pwdEl.placeholder = state.hasPwd ? t('已设置密码') : '';
    el('genPwdBtn').textContent = t(state.hasPwd ? 'share.resetPwd' : 'share.genPwd');
    el('shareExpire').value = '0';
    refreshLink();
    UI.openModal(modal);
  }

  return { bind: bind, openFor: openFor };
})();
