// 浏览器端 Markdown 渲染（分享阅读页）
(function () {
  function renderMarkdown(src, el) {
    if (window.marked && window.DOMPurify) {
      // DOMPurify 消毒，防分享文档内嵌脚本/XSS
      el.innerHTML = DOMPurify.sanitize(marked.parse(src || ''));
    } else {
      // C1：消毒组件未就绪时绝不写未净化 HTML，降级为纯文本展示
      el.textContent = src || '';
    }
  }
  var raw = document.getElementById('raw-markdown');
  var preview = document.getElementById('preview');
  if (raw && preview) {
    if (window.marked && marked.setOptions) {
      marked.setOptions({ gfm: true, breaks: false });
    }
    renderMarkdown(raw.value, preview);
  }
})();
