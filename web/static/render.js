// 浏览器端 Markdown 渲染（分享阅读页）
(function () {
  function renderMarkdown(src, el) {
    if (window.Embeds && Embeds.parseMarkdown) {
      var html = Embeds.parseMarkdown(src || '');
      if (html != null) {
        el.innerHTML = html;
        return;
      }
    }
    if (window.marked && window.DOMPurify) {
      // DOMPurify 消毒，防分享文档内嵌脚本/XSS
      el.innerHTML = DOMPurify.sanitize(marked.parse(src || ''));
    } else {
      // C1：消毒组件未就绪时绝不写未净化 HTML，降级为纯文本展示
      el.textContent = src || '';
    }
  }

  function hydrateEmbeds(el) {
    if (!el || !window.Embeds) return;
    var canEdit = !!(window.READER && READER.canEdit);
    var raw = document.getElementById('raw-markdown');
    var editArea = document.getElementById('editContent');
    Embeds.hydrate(el, {
      editable: canEdit,
      showPreview: true,
      getSource: function () {
        if (editArea && !editArea.hidden) return editArea.value;
        return raw ? raw.value : '';
      },
      setSource: function (next) {
        // 分享可编辑：先落库成功再更新本地，避免失败后 raw/服务端不一致
        if (window.READER && READER.canEdit && READER.token) {
          persistShareContent(next);
          return;
        }
        if (raw) raw.value = next;
        if (editArea && !editArea.hidden) editArea.value = next;
        renderMarkdown(next, el);
        hydrateEmbeds(el);
      }
    });
  }

  async function persistShareContent(content) {
    if (!window.READER || !READER.token) return;
    try {
      var res = await fetch('/s/' + READER.token + '/content', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
        body: JSON.stringify({ content: content })
      });
      var data = await res.json().catch(function () { return {}; });
      if (!res.ok) {
        if (window.UI) UI.alert((data && data.error) || '保存失败');
        return;
      }
      var raw = document.getElementById('raw-markdown');
      var editArea = document.getElementById('editContent');
      if (raw) raw.value = content;
      if (editArea && !editArea.hidden) editArea.value = content;
      if (window.UI) UI.toast(UI.t('common.saved') || '已保存', 'success');
      var preview = document.getElementById('preview');
      if (preview) {
        renderMarkdown(content, preview);
        hydrateEmbeds(preview);
      }
    } catch (e) {
      if (window.UI) UI.alert('保存失败：网络错误');
    }
  }

  var raw = document.getElementById('raw-markdown');
  var preview = document.getElementById('preview');
  if (raw && preview) {
    if (window.marked && marked.setOptions) {
      marked.setOptions({ gfm: true, breaks: false });
    }
    renderMarkdown(raw.value, preview);
    hydrateEmbeds(preview);
  }

  window.DocRender = {
    renderMarkdown: renderMarkdown,
    hydrateEmbeds: hydrateEmbeds
  };
})();
