// 导航「开放平台」下拉：点击展开/收起，外部点击与 Esc 关闭。
// 委托绑定在 document 上，官网（landing.js 页面）与控制台（ui.js 页面）共用本文件，只允许加载一次。
(function () {
  function closeAll(except) {
    document.querySelectorAll('.drop.open').forEach(function (d) {
      if (d === except) return;
      d.classList.remove('open');
      var b = d.querySelector('.drop-btn');
      if (b) b.setAttribute('aria-expanded', 'false');
    });
  }
  document.addEventListener('click', function (e) {
    var btn = e.target.closest && e.target.closest('.drop-btn');
    if (btn) {
      var wrap = btn.closest('.drop');
      if (!wrap) return;
      var open = !wrap.classList.contains('open');
      closeAll(wrap);
      wrap.classList.toggle('open', open);
      btn.setAttribute('aria-expanded', open ? 'true' : 'false');
      return;
    }
    if (!e.target.closest || !e.target.closest('.drop')) closeAll(null);
  });
  document.addEventListener('keydown', function (e) {
    if (e.key === 'Escape') closeAll(null);
  });
})();
