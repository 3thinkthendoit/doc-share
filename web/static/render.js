// 浏览器端 Markdown 渲染（分享阅读页）
(function () {
  function pageTitle() {
    var el = document.querySelector('.doc-title');
    if (!el) return '';
    var text = '';
    el.childNodes.forEach(function (n) {
      if (n.nodeType === 3) text += n.textContent;
    });
    return text.replace(/\s+/g, ' ').trim();
  }

  // 页头已经展示文档标题。正文开头若是同一条一级标题，预览里去掉，不改原文。
  function stripDuplicateTitle(el) {
    var title = pageTitle();
    if (!title) return;
    var h = el.firstElementChild;
    if (!h || h.tagName !== 'H1') return;
    if ((h.textContent || '').replace(/\s+/g, ' ').trim() !== title) return;
    h.remove();
  }

  function renderMarkdown(src, el) {
    if (window.Embeds && Embeds.parseMarkdown) {
      var html = Embeds.parseMarkdown(src || '');
      if (html != null) {
        el.innerHTML = html;
        stripDuplicateTitle(el);
        return;
      }
    }
    if (window.marked && window.DOMPurify) {
      // DOMPurify 消毒，防分享文档内嵌脚本/XSS
      el.innerHTML = DOMPurify.sanitize(marked.parse(src || ''));
      stripDuplicateTitle(el);
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
