// 模板管理：新建走「类型选择 → 对应编辑器」（与新建文档同构），删除走确认框
// 页面文案统一经 UI.t 查词典
(function () {
  var typeModal = document.getElementById('tplTypeModal');
  var newBtn = document.getElementById('btnNewTemplate');
  var grid = document.getElementById('tplTypeGrid');
  if (typeModal && newBtn && grid) {
    UI.bindModal(typeModal);
    function icon(paths) {
      return '<span class="doc-type-icon"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' + paths + '</svg></span>';
    }
    // 与新建文档同一组类型卡片；HTML 整站不支持模板，不提供
    var TPL_TYPES = [
      {
        title: 'Markdown',
        desc: UI.t('tpl.typeMarkdownDesc'),
        icon: icon('<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5"/><path d="M9 13h6"/><path d="M9 17h4"/>'),
        action: function () { location.href = '/console/templates/new?type=markdown'; }
      },
      {
        title: UI.t('docs.tagMindmap'),
        desc: UI.t('tpl.typeMindmapDesc'),
        icon: icon('<circle cx="12" cy="5" r="2.2"/><circle cx="5" cy="19" r="2.2"/><circle cx="19" cy="19" r="2.2"/><path d="M12 7.2v4.3M12 11.5 6.2 17.2M12 11.5l5.8 5.7"/>'),
        action: function () { location.href = '/console/templates/new?type=mindmap'; }
      },
      {
        title: UI.t('docs.tagBoard'),
        desc: UI.t('tpl.typeBoardDesc'),
        icon: icon('<path d="M4 20l4.5-1.2L20 7.3a2 2 0 0 0 0-2.8l-.5-.5a2 2 0 0 0-2.8 0L5.2 15.5 4 20z"/><path d="M13.5 6.5l4 4"/>'),
        action: function () { location.href = '/console/templates/new?type=board'; }
      }
    ];
    // drawio 需要独立编辑器地址，未配置时不提供该类型（与新建文档一致）
    if (window.TPL_DRAWIO) {
      TPL_TYPES.push({
        title: UI.t('docs.tagDrawio'),
        desc: UI.t('tpl.typeDrawioDesc'),
        icon: icon('<rect x="3" y="4" width="18" height="16" rx="2"/><path d="m10 10-2 2 2 2"/><path d="m14 10 2 2-2 2"/>'),
        action: function () { location.href = '/console/templates/new?type=drawio'; }
      });
    }
    TPL_TYPES.forEach(function (t) {
      var card = document.createElement('button');
      card.type = 'button';
      card.className = 'doc-type-card';
      card.innerHTML = t.icon + '<span class="doc-type-text"><strong>' + t.title + '</strong><span class="muted">' + t.desc + '</span></span>';
      card.addEventListener('click', t.action);
      grid.appendChild(card);
    });
    newBtn.addEventListener('click', function () { UI.openModal(typeModal); });
  }
})();

async function delTemplate(btn) {
  var ok = await UI.confirm(UI.t('tpl.confirmDel', btn.dataset.name), { danger: true });
  if (!ok) return;
  try {
    var res = await fetch('/console/api/templates/' + btn.dataset.id, {
      method: 'DELETE',
      headers: { 'X-Requested-With': 'XMLHttpRequest' }
    });
    if (res.ok) { location.reload(); }
    else {
      var data = await res.json();
      UI.toast(data.error || UI.t('err.deleteFail'), 'error');
    }
  } catch (err) {
    UI.toast(UI.t('err.netDel'), 'error');
  }
}
