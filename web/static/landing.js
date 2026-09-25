// 官网交互：主题切换、滚动入场、数字滚动、锚点平滑跳转。不依赖任何外部库。
(function () {
  'use strict';

  var reduced = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  // 滚动入场
  var items = document.querySelectorAll('[data-reveal]');
  if (reduced || !('IntersectionObserver' in window)) {
    items.forEach(function (el) { el.classList.add('l-in'); });
  } else {
    var io = new IntersectionObserver(function (entries) {
      entries.forEach(function (e) {
        if (!e.isIntersecting) return;
        e.target.classList.add('l-in');
        io.unobserve(e.target);
      });
    }, { rootMargin: '0px 0px -12% 0px', threshold: 0.08 });
    items.forEach(function (el) { io.observe(el); });
  }

  // 数字滚动：进入视口后 900ms 内递增到目标值
  function rollUp(el) {
    var target = parseInt(el.dataset.count, 10) || 0;
    if (reduced) { el.textContent = String(target); return; }
    var start = performance.now(), dur = 900;
    function step(now) {
      var p = Math.min(1, (now - start) / dur);
      el.textContent = String(Math.round(target * (1 - Math.pow(1 - p, 3))));
      if (p < 1) requestAnimationFrame(step);
    }
    requestAnimationFrame(step);
  }
  var counters = document.querySelectorAll('[data-count]');
  if (!('IntersectionObserver' in window)) {
    counters.forEach(rollUp);
  } else {
    var co = new IntersectionObserver(function (entries) {
      entries.forEach(function (e) {
        if (!e.isIntersecting) return;
        rollUp(e.target);
        co.unobserve(e.target);
      });
    }, { threshold: 0.4 });
    counters.forEach(function (el) { co.observe(el); });
  }

  // 锚点平滑跳转（fixed 导航需要留出高度）
  var nav = document.querySelector('.l-nav');
  document.querySelectorAll('a[href^="#"]').forEach(function (a) {
    a.addEventListener('click', function (ev) {
      var id = a.getAttribute('href').slice(1);
      var el = document.getElementById(id);
      if (!el) return;
      ev.preventDefault();
      // fixed 导航在窄屏会撑高（换行），高度实时取，不能写死 78
      var navH = nav ? nav.getBoundingClientRect().height : 66;
      var top = el.getBoundingClientRect().top + window.pageYOffset - navH - 12;
      window.scrollTo({ top: top, behavior: reduced ? 'auto' : 'smooth' });
    });
  });

  // 导航在滚动后加深底色：挂类而非写内联 style，否则切主题时颜色会被旧的写死值盖住
  if (nav) {
    var onScroll = function () {
      nav.classList.toggle('l-nav--scrolled', window.pageYOffset > 24);
    };
    onScroll();
    window.addEventListener('scroll', onScroll, { passive: true });
  }

  // 明暗配色：偏好存 localStorage，head 里的内联脚本在下次首屏前应用
  var themeBtn = document.querySelector('.l-theme');
  if (themeBtn) {
    themeBtn.setAttribute('aria-pressed', document.documentElement.getAttribute('data-theme') === 'light' ? 'true' : 'false');
    themeBtn.addEventListener('click', function () {
      var root = document.documentElement;
      var next = root.getAttribute('data-theme') === 'light' ? 'dark' : 'light';
      root.setAttribute('data-theme', next);
      root.setAttribute('data-theme-anim', ''); // 首次绘制不开过渡，避免闪色
      themeBtn.setAttribute('aria-pressed', next === 'light' ? 'true' : 'false');
      try { localStorage.setItem('ds_theme', next); } catch (e) { /* 隐私模式下忽略 */ }
    });
  }

  // 窄屏导航抽屉：从桌面链接克隆，点击锚点后关闭
  var toggle = document.getElementById('lNavToggle');
  var drawer = document.getElementById('lNavDrawer');
  var drawerLinks = document.getElementById('lNavDrawerLinks');
  var srcLinks = document.getElementById('lNavLinks');
  function closeLNav() {
    if (!drawer || !toggle) return;
    drawer.hidden = true;
    toggle.setAttribute('aria-expanded', 'false');
    document.body.classList.remove('l-nav-open');
  }
  function openLNav() {
    if (!drawer || !toggle || !drawerLinks || !srcLinks) return;
    drawerLinks.innerHTML = '';
    srcLinks.querySelectorAll('a').forEach(function (a) {
      drawerLinks.appendChild(a.cloneNode(true));
    });
    drawer.hidden = false;
    toggle.setAttribute('aria-expanded', 'true');
    document.body.classList.add('l-nav-open');
  }
  if (toggle && drawer) {
    toggle.addEventListener('click', function () {
      if (drawer.hidden) openLNav(); else closeLNav();
    });
    drawer.addEventListener('click', function (e) {
      if (e.target === drawer || e.target.closest('[data-close-lnav]')) closeLNav();
    });
    drawerLinks && drawerLinks.addEventListener('click', function (e) {
      if (e.target.closest('a')) closeLNav();
    });
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape') closeLNav();
    });
  }
})();
