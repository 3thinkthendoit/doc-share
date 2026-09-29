/* revisions.js: 历史版本面板（修订列表 + 打版本标签 + 一键回滚）。
   Markdown 编辑页（doc_edit.html）与画布编辑页（json_edit.html：思维导图/白板/drawio）共用。
   后端在每次内容覆盖前都会写修订快照（与文档类型无关），故画布文档同样有历史；
   本组件只负责展示与回滚，不依赖任何具体编辑器。
   用法：Revisions.init(function () { return 当前文档ID; }); */
window.Revisions = (function () {
  'use strict';

  function init(getDocId) {
    var revisionsModal = document.getElementById('revisionsModal');
    var revisionsBtn = document.getElementById('revisionsBtn');
    if (!revisionsModal || !revisionsBtn) return;
    UI.bindModal(revisionsModal);
    var revList = document.getElementById('revisionsList');

    // 版本标签弹窗：给某个快照打 v1.0.1 这类版本号并写修订说明
    var labelModal = document.getElementById('labelRevModal');
    var labelInput = document.getElementById('labelRevInput');
    var labelNote = document.getElementById('labelRevNote');
    var labelTarget = document.getElementById('labelRevTarget');
    var labelSave = document.getElementById('labelRevSave');
    var labelRev = null; // 当前正在打标签的修订
    if (labelModal) UI.bindModal(labelModal);

    function docID() { return getDocId(); }

    function revTime(r) {
      return (r.created_at || '').replace('T', ' ').substring(0, 16);
    }

    // 自动版本号：未打手工标签的快照按保存顺序编为 v1.0.0、v1.0.1…，
    // 与手工标签（如 v2.0.0）同形，便于在发版说明里互相引用
    function autoVersion(r) {
      return 'v1.0.' + Math.max(0, (r.seq || 1) - 1);
    }

    function revMeta(r) {
      return (r.label || autoVersion(r)) + ' · ' + revTime(r) + ' · ' + (r.editor_name || '-');
    }

    // 一个逻辑行 = 主行（版本号 / 更新人 / 更新时间 / 操作）+ 可选说明行（跨四列），返回行数组
    function buildRevRow(r) {
      var tr = document.createElement('tr');
      tr.className = 'rev-row';

      // 版本号列：手工标签优先（主色胶囊），否则自动 v1.0.N
      var tdV = document.createElement('td');
      var ver = document.createElement('span');
      if (r.label) {
        ver.className = 'revision-label';
        ver.textContent = r.label;
      } else {
        ver.className = 'rev-auto';
        ver.textContent = autoVersion(r);
      }
      tdV.appendChild(ver);
      tr.appendChild(tdV);

      var tdE = document.createElement('td');
      tdE.textContent = r.editor_name || '-';
      tr.appendChild(tdE);

      var tdT = document.createElement('td');
      tdT.className = 'rev-time';
      tdT.textContent = revTime(r);
      tr.appendChild(tdT);

      var tdO = document.createElement('td');
      var ops = document.createElement('div');
      ops.className = 'rev-ops';
      var labelBtn = document.createElement('button');
      labelBtn.type = 'button';
      labelBtn.className = 'btn btn-sm';
      labelBtn.textContent = UI.t('revisions.labelBtn');
      labelBtn.addEventListener('click', function () {
        labelRev = r;
        labelInput.value = r.label || '';
        labelNote.value = r.note || '';
        labelTarget.textContent = revMeta(r);
        UI.openModal(labelModal);
        labelInput.focus();
      });
      ops.appendChild(labelBtn);

      var btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'btn btn-sm';
      btn.textContent = UI.t('revisions.restore');
      btn.addEventListener('click', async function () {
        if (!await UI.confirm(UI.t('revisions.rollbackConfirm'))) return;
        btn.disabled = true;
        var res2 = await fetch('/console/api/docs/' + docID() + '/revisions/' + r.id + '/rollback', {
          method: 'POST', headers: { 'X-Requested-With': 'XMLHttpRequest' }
        });
        if (res2.ok) { location.reload(); return; }
        var d2 = await res2.json().catch(function () { return {}; });
        UI.alert((d2 && d2.error) || UI.t('revisions.rollbackFail'));
        btn.disabled = false;
      });
      ops.appendChild(btn);
      tdO.appendChild(ops);
      tr.appendChild(tdO);

      var rows = [tr];
      if (r.note) {
        var trN = document.createElement('tr');
        trN.className = 'rev-note-row';
        var tdN = document.createElement('td');
        tdN.colSpan = 4;
        tdN.textContent = r.note;
        trN.appendChild(tdN);
        rows.push(trN);
      }
      return rows;
    }

    async function loadRevisions() {
      revList.innerHTML = '<div class="muted rev-empty">' + UI.t('revisions.loading') + '</div>';
      var revs;
      try {
        var res = await fetch('/console/api/docs/' + docID() + '/revisions', {
          headers: { 'X-Requested-With': 'XMLHttpRequest' }
        });
        var data = await res.json().catch(function () { return {}; });
        if (!res.ok) throw new Error((data && data.error) || 'load failed');
        revs = (data && data.revisions) || [];
      } catch (e) {
        revList.innerHTML = '<div class="muted rev-empty">' + UI.t('revisions.loadFail') + '</div>';
        return;
      }
      if (!revs.length) {
        revList.innerHTML = '<div class="muted rev-empty">' + UI.t('revisions.empty') + '</div>';
        return;
      }
      var table = document.createElement('table');
      table.className = 'revision-table';
      var thead = document.createElement('thead');
      var htr = document.createElement('tr');
      // 表头复用既有词典键：版本号 / 更新人 / 更新时间 / 操作
      ['revisions.labelField', 'common.updater', 'common.updatedAt', 'common.actions'].forEach(function (k, i) {
        var th = document.createElement('th');
        th.textContent = UI.t(k);
        if (i === 3) th.className = 'rev-ops-th';
        htr.appendChild(th);
      });
      thead.appendChild(htr);
      table.appendChild(thead);
      var tbody = document.createElement('tbody');
      revs.forEach(function (r) {
        buildRevRow(r).forEach(function (row) { tbody.appendChild(row); });
      });
      table.appendChild(tbody);
      revList.innerHTML = '';
      revList.appendChild(table);
    }

    if (labelSave) {
      labelSave.addEventListener('click', async function () {
        if (!labelRev) return;
        labelSave.disabled = true;
        try {
          var res = await fetch('/console/api/docs/' + docID() + '/revisions/' + labelRev.id + '/label', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
            body: JSON.stringify({ label: labelInput.value, note: labelNote.value })
          });
          var d = await res.json().catch(function () { return {}; });
          // 409 = 版本号重复，后端文案已经 i18n 中间件翻译
          if (!res.ok) { UI.toast((d && d.error) || UI.t('err.saveFail'), 'error'); return; }
          UI.closeModal(labelModal);
          UI.toast(UI.t('edit.saveOk'), 'success');
          loadRevisions();
        } catch (e) {
          UI.toast(UI.t('common.netErr'), 'error');
        } finally {
          labelSave.disabled = false;
        }
      });
    }

    revisionsBtn.addEventListener('click', function () {
      UI.openModal(revisionsModal);
      loadRevisions();
    });
  }

  return { init: init };
})();
