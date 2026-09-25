// 轻量 UI 组件：toast 轻提示 + 自定义 alert/confirm/prompt 弹窗（替代浏览器原生弹窗）
window.UI = (function () {
  'use strict';

  /* ---- 多语言 ----
     后端按当前页面语言注入“中文原文 → 译文”映射（默认语言下为空对象）。
     所有可见文案都经过 toast / dialog 这两个出口，因此在它们内部统一查表，
     页面脚本无需逐个改写；直写 DOM 的文案用 UI.t('中文原文') 包裹。 */
  var L10N = window.__L10N || {};
  function t(s) {
    var out = (s && L10N[s]) || s;
    for (var i = 1; i < arguments.length; i++) {
      out = String(out).split('{' + (i - 1) + '}').join(arguments[i]);
    }
    return out;
  }

  /* ---- toast ---- */
  function toast(msg, type) {
    var box = document.getElementById('ui-toasts');
    if (!box) {
      box = document.createElement('div');
      box.id = 'ui-toasts';
      document.body.appendChild(box);
    }
    var el = document.createElement('div');
    el.className = 'toast toast-' + (type || 'info');
    el.textContent = t(msg);
    box.appendChild(el);
    requestAnimationFrame(function () { el.classList.add('show'); });
    setTimeout(function () {
      el.classList.remove('show');
      setTimeout(function () { el.remove(); }, 260);
    }, 2200);
  }

  /* ---- dialog ---- */
  // opts: {message, input:'text'|'password'|null, placeholder, buttons:[{label, primary, value}]}
  // 返回 Promise，resolve 被点击按钮的 value；prompt 取消时 resolve(null)
  function dialog(opts) {
    return new Promise(function (resolve) {
      var overlay = document.createElement('div');
      overlay.className = 'modal-overlay ui-dialog';

      var modal = document.createElement('div');
      modal.className = 'dialog';

      var msg = document.createElement('div');
      msg.className = 'dialog-msg';
      msg.textContent = t(opts.message || '');
      modal.appendChild(msg);

      var input = null;
      if (opts.input) {
        input = document.createElement('input');
        input.type = opts.input === 'password' ? 'password' : 'text';
        input.className = 'dialog-input';
        input.placeholder = t(opts.placeholder || '');
        if (opts.value != null && opts.value !== '') input.value = String(opts.value);
        modal.appendChild(input);
      }

      var foot = document.createElement('div');
      foot.className = 'dialog-foot';
      modal.appendChild(foot);
      overlay.appendChild(modal);

      var done = false;
      function close(val) {
        if (done) return;
        done = true;
        document.removeEventListener('keydown', onKey);
        overlay.classList.add('closing');
        setTimeout(function () { overlay.remove(); }, 180);
        resolve(val);
      }
      function cancel() {
        close(opts.input ? null : false);
      }
      var primaryValue = true;
      function confirmWith(btn, value) {
        if (input) {
          if (btn.dataset.primary === '1' && !input.value.trim()) {
            input.classList.add('error');
            input.focus();
            return;
          }
          if (btn.dataset.primary === '1') {
            close(input.value); // prompt：返回输入的文本
            return;
          }
        }
        close(value);
      }

      (opts.buttons || [{ label: '确定', primary: true, value: true }]).forEach(function (b) {
        var btn = document.createElement('button');
        btn.type = 'button';
        btn.className = 'btn' + (b.primary ? ' btn-primary' : '') + (b.danger ? ' btn-danger-solid' : '');
        btn.dataset.primary = b.primary ? '1' : '0';
        btn.textContent = t(b.label);
        btn.addEventListener('click', function () { confirmWith(btn, b.value); });
        if (b.primary) primaryValue = b.value;
        foot.appendChild(btn);
      });

      function onKey(e) {
        if (e.key === 'Escape') { cancel(); return; }
        if (e.key === 'Enter') {
          var primary = foot.querySelector('[data-primary="1"]');
          if (primary) confirmWith(primary, primaryValue);
        }
      }
      document.addEventListener('keydown', onKey);
      overlay.addEventListener('click', function (e) {
        if (e.target === overlay) cancel();
      });

      document.body.appendChild(overlay);
      requestAnimationFrame(function () {
        overlay.classList.add('show');
        if (input) input.focus(); else {
          var p = foot.querySelector('[data-primary="1"]');
          if (p) p.focus();
        }
      });
    });
  }

  /* ---- 静态弹窗（页面内预置的 .modal-overlay）---- */
  // 按页面上可见 overlay 同步 body 滚动锁，避免多层弹窗互相拆掉 modal-open
  function syncBodyLock() {
    var locked = false;
    document.querySelectorAll('.modal-overlay').forEach(function (el) {
      if (el.hidden || !el.isConnected) return;
      locked = true;
    });
    document.body.classList.toggle('modal-open', locked);
  }
  function openModal(el) {
    if (!el) return;
    el.hidden = false;
    syncBodyLock();
  }
  function closeModal(el) {
    if (!el) return;
    el.hidden = true;
    syncBodyLock();
  }
  // 绑定 [data-close-modal] 按钮关闭（✕ / 取消）。
  // 注意：表单弹窗故意不响应遮罩点击和 ESC，防误触丢失填写内容；
  // 消息类弹窗（UI.alert/confirm/prompt）是动态 dialog，仍支持遮罩/ESC 关闭
  function bindModal(el) {
    if (!el) return;
    el.addEventListener('click', function (e) {
      if (e.target.closest('[data-close-modal]')) closeModal(el);
    });
  }

  /* ---- 表单校验：替代浏览器原生气泡提示 ---- */
  // 输入时自动清除错误态（事件委托，页面级生效）
  document.addEventListener('input', function (e) {
    if (e.target && e.target.classList) e.target.classList.remove('field-error');
  });
  // 检查 required 字段：空值标红 + toast 提示，返回是否通过
  function validateForm(form) {
    var ok = true, first = null;
    form.querySelectorAll('input[required], textarea[required], select[required]').forEach(function (inp) {
      var bad = !String(inp.value || '').trim();
      inp.classList.toggle('field-error', bad);
      if (bad) { ok = false; if (!first) first = inp; }
    });
    if (!ok) {
      toast('请填写完整后再提交', 'error');
      if (first) first.focus();
    }
    return ok;
  }
  // 给表单绑定提交拦截（需配合 form 上的 novalidate 使用）
  function bindFormValidation(form) {
    if (!form) return;
    form.addEventListener('submit', function (e) {
      if (!validateForm(form)) e.preventDefault();
    });
  }

  /* ---- 自定义下拉（原生 select 弹出面板由系统绘制，无法主题化）----
     保留原生 select 在 DOM 内：FormData / GET 表单提交 / .value 读取全部不变，
     只在其上叠加按钮触发器 + 自绘面板 */
  var xsOpen = null;
  function closeXs() {
    if (!xsOpen) return;
    xsOpen._xs.panel.hidden = true;
    xsOpen._xs.btn.classList.remove('open');
    xsOpen = null;
  }
  function buildXsOptions(sel) {
    var s = sel._xs;
    s.panel.innerHTML = '';
    Array.prototype.forEach.call(sel.options, function (opt, i) {
      var item = document.createElement('div');
      item.className = 'xsel-opt' + (i === sel.selectedIndex ? ' sel' : '');
      item.textContent = opt.text;
      item.addEventListener('click', function (e) {
        e.stopPropagation();
        sel.value = opt.value;
        sel.selectedIndex = i;
        closeXs();
        syncXs(sel);
        sel.dispatchEvent(new Event('change', { bubbles: true }));
        sel.dispatchEvent(new Event('input', { bubbles: true }));
      });
      s.panel.appendChild(item);
    });
  }
  function syncXs(sel) {
    if (!sel._xs) return;
    var opt = sel.options[sel.selectedIndex];
    sel._xs.txt.textContent = opt ? opt.text : '';
    sel._xs.btn.classList.toggle('disabled', !!sel.disabled);
    buildXsOptions(sel);
  }
  function skinSelect(sel) {
    if (sel._xs || sel.multiple || sel.size > 1) return;
    var wrap = document.createElement('span');
    wrap.className = 'xsel';
    sel.parentNode.insertBefore(wrap, sel);
    wrap.appendChild(sel);
    var btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'xsel-btn';
    var txt = document.createElement('span');
    txt.className = 'xsel-txt';
    var caret = document.createElement('span');
    caret.className = 'xsel-caret';
    caret.textContent = '▾';
    btn.appendChild(txt);
    btn.appendChild(caret);
    wrap.appendChild(btn);
    var panel = document.createElement('div');
    panel.className = 'xsel-panel';
    panel.hidden = true;
    wrap.appendChild(panel);
    sel._xs = { wrap: wrap, btn: btn, txt: txt, panel: panel };
    btn.addEventListener('click', function (e) {
      e.stopPropagation();
      if (sel.disabled) return;
      var wasOpen = xsOpen === sel;
      closeXs();
      if (!wasOpen) {
        xsOpen = sel;
        panel.hidden = false;
        btn.classList.add('open');
      }
    });
    syncXs(sel);
  }
  document.addEventListener('click', function () { closeXs(); });
  // 捕获阶段拦截 ESC：只收下拉，不连带关闭所在弹窗
  document.addEventListener('keydown', function (e) {
    if (e.key === 'Escape' && xsOpen) {
      e.stopPropagation();
      closeXs();
    }
  }, true);

  // 头像选择器：上传到 /admin/api/upload 后回填隐藏域 + 预览；返回 set/get 句柄
  function avatarPicker(root, fallbackText) {
    if (!root) return null;
    var input = root.querySelector('[data-avatar-input]');
    var file = root.querySelector('[data-avatar-file]');
    var prev = root.querySelector('[data-avatar-preview]');
    var upBtn = root.querySelector('[data-avatar-upload]');
    var clearBtn = root.querySelector('[data-avatar-clear]');
    if (!input || !file || !prev) return null;
    // 初始占位（模板里已按语言填好的昵称首字），无头像时回显它
    var initial = (prev.textContent || '').trim();

    function render() {
      prev.innerHTML = '';
      if (input.value) {
        var img = document.createElement('img');
        img.src = input.value;
        img.alt = t('头像');
        prev.appendChild(img);
      } else {
        prev.textContent = fallbackText || initial || t('头');
      }
    }
    if (upBtn) upBtn.addEventListener('click', function () { file.click(); });
    file.addEventListener('change', async function () {
      if (!file.files.length) return;
      var fd = new FormData();
      fd.append('file', file.files[0]);
      file.value = '';
      UI.toast('头像上传中…', 'info');
      try {
        var res = await fetch('/admin/api/upload', { method: 'POST', headers: { 'X-Requested-With': 'XMLHttpRequest' }, body: fd });
        var data = await res.json().catch(function () { return {}; });
        if (res.ok && data.url) {
          input.value = data.url;
          render();
          UI.toast('头像已上传，保存后生效', 'success');
        } else {
          UI.alert((data && data.error) || '头像上传失败');
        }
      } catch (err) {
        UI.alert('头像上传失败：网络错误或服务不可用');
      }
    });
    if (clearBtn) clearBtn.addEventListener('click', function () { input.value = ''; render(); });
    render();
    return {
      set: function (url) { input.value = url || ''; render(); },
      get: function () { return input.value; },
    };
  }

  // 弹窗内数字分页：el 为容器；opts={page,totalPages,size,total,sizes,onPage,onSize}
  function renderPager(el, opts) {
    if (!el) return;
    el.innerHTML = '';
    opts = opts || {};
    var page = opts.page || 1;
    var totalPages = opts.totalPages || 1;
    var size = opts.size || 15;
    var total = opts.total || 0;
    var sizes = opts.sizes || [15, 30, 50];
    if (total <= 0) return;

    var wrap = document.createElement('div');
    wrap.className = 'pager';
    if (totalPages > 1) {
      var pages = document.createElement('div');
      pages.className = 'pager-pages';
      function addBtn(label, p, active) {
        if (active) {
          var span = document.createElement('span');
          span.className = 'btn btn-sm btn-primary';
          span.textContent = label;
          pages.appendChild(span);
          return;
        }
        var a = document.createElement('button');
        a.type = 'button';
        a.className = 'btn btn-sm';
        a.textContent = label;
        a.addEventListener('click', function () {
          if (typeof opts.onPage === 'function') opts.onPage(p);
        });
        pages.appendChild(a);
      }
      if (page > 1) addBtn('‹', page - 1, false);
      var start = Math.max(1, page - 3);
      var end = Math.min(totalPages, start + 6);
      start = Math.max(1, end - 6);
      for (var i = start; i <= end; i++) addBtn(String(i), i, i === page);
      if (page < totalPages) addBtn('›', page + 1, false);
      wrap.appendChild(pages);
    }
    var sel = document.createElement('select');
    sel.className = 'pager-size';
    sel.setAttribute('aria-label', t('pager.size'));
    sizes.forEach(function (s) {
      var o = document.createElement('option');
      o.value = s;
      o.textContent = s + ' ' + t('pager.perPage');
      if (s === size) o.selected = true;
      sel.appendChild(o);
    });
    sel.addEventListener('change', function () {
      if (typeof opts.onSize === 'function') opts.onSize(parseInt(sel.value, 10) || 15);
    });
    wrap.appendChild(sel);
    var info = document.createElement('span');
    info.className = 'muted';
    info.textContent = t('pager.info', page, totalPages, total);
    wrap.appendChild(info);
    el.appendChild(wrap);
  }

  // 用户信息悬停卡：.owner-link[data-name|username|avatar|email|phone|active]
  var ownerPop = null;
  function ensureOwnerPop() {
    if (ownerPop) return ownerPop;
    ownerPop = document.createElement('div');
    ownerPop.className = 'owner-pop';
    ownerPop.hidden = true;
    document.body.appendChild(ownerPop);
    return ownerPop;
  }
  function showOwnerPop(el) {
    var pop = ensureOwnerPop();
    var name = el.dataset.name || '';
    var av = el.dataset.avatar || '';
    var avNode = document.createElement('span');
    avNode.className = 'owner-initial';
    if (av) {
      var img = document.createElement('img');
      img.src = av;
      img.alt = '';
      avNode.appendChild(img);
    } else {
      avNode.textContent = name.charAt(0) || '?';
    }
    var info = document.createElement('div');
    info.className = 'info';
    var nameRow = document.createElement('div');
    nameRow.className = 'name';
    nameRow.textContent = name || '—';
    var userRow = document.createElement('div');
    userRow.className = 'username';
    userRow.textContent = '@' + (el.dataset.username || '—');
    info.appendChild(nameRow);
    info.appendChild(userRow);

    var head = document.createElement('div');
    head.className = 'owner-pop-head';
    head.appendChild(avNode);
    head.appendChild(info);

    var body = document.createElement('div');
    body.className = 'owner-pop-body';
    [
      [t('owner.phone'), el.dataset.phone],
      [t('owner.email'), el.dataset.email],
      [t('owner.active'), el.dataset.active]
    ].forEach(function (p) {
      var row = document.createElement('div');
      row.className = 'field';
      var lbl = document.createElement('span');
      lbl.className = 'lbl';
      lbl.textContent = p[0];
      var val = document.createElement('span');
      val.className = 'val';
      val.textContent = p[1] || '—';
      row.appendChild(lbl);
      row.appendChild(val);
      body.appendChild(row);
    });

    pop.innerHTML = '';
    pop.appendChild(head);
    pop.appendChild(body);
    pop.hidden = false;
    var rect = el.getBoundingClientRect();
    var top = rect.bottom + 8;
    var left = Math.min(rect.left, window.innerWidth - pop.offsetWidth - 8);
    if (left < 8) left = 8;
    // 贴近视口底部时改到触发元素上方
    if (top + pop.offsetHeight > window.innerHeight - 8) {
      top = Math.max(8, rect.top - pop.offsetHeight - 8);
    }
    pop.style.top = top + 'px';
    pop.style.left = left + 'px';
    var arrowX = rect.left + rect.width / 2 - left;
    arrowX = Math.max(16, Math.min(arrowX, pop.offsetWidth - 16));
    pop.style.setProperty('--arrow-x', arrowX + 'px');
  }
  function bindOwnerHover(root) {
    (root || document).querySelectorAll('.owner-link').forEach(function (el) {
      if (el.dataset.popBound) return;
      el.dataset.popBound = '1';
      el.addEventListener('mouseenter', function () { showOwnerPop(el); });
      el.addEventListener('mouseleave', function () {
        if (ownerPop) ownerPop.hidden = true;
      });
    });
  }

  return {
    t: t,
    lang: window.__LANG || 'zh-CN',
    toast: toast,
    validateForm: validateForm,
    bindFormValidation: bindFormValidation,
    openModal: openModal,
    closeModal: closeModal,
    syncBodyLock: syncBodyLock,
    bindModal: bindModal,
    syncSelect: syncXs,
    skinSelect: skinSelect,
    avatarPicker: avatarPicker,
    renderPager: renderPager,
    bindOwnerHover: bindOwnerHover,
    alert: function (message) {
      return dialog({ message: message, buttons: [{ label: '知道了', primary: true, value: true }] });
    },
    // confirm：opts.danger=true 时确认按钮为红色实心（删除/重置等破坏性操作）
    confirm: function (message, opts) {
      opts = opts || {};
      return dialog({
        message: message,
        buttons: [
          { label: '取消', primary: false, value: false },
          { label: '确定', primary: true, value: true, danger: !!opts.danger },
        ],
      });
    },
    prompt: function (message, inputType, placeholder, defaultValue) {
      return dialog({
        message: message,
        input: inputType || 'text',
        placeholder: placeholder || '',
        value: defaultValue || '',
        buttons: [
          { label: '取消', primary: false, value: null },
          { label: '确定', primary: true, value: true },
        ],
      }); // resolve 输入文本；取消/关闭返回 null
    },
  };
})();

// 导航栏用户菜单与修改密码弹窗（ui.js 在 head 加载，需等 DOM 就绪）
document.addEventListener('DOMContentLoaded', function () {
  if (!window.UI) return;

  // 明暗主题：与官网共用 localStorage ds_theme；.theme-toggle（控制台）与 .l-theme（官网/分享顶栏）
  (function initThemeToggles() {
    var root = document.documentElement;
    var btns = document.querySelectorAll('.theme-toggle, .l-theme');
    if (!btns.length) return;
    function syncPressed() {
      var isLight = root.getAttribute('data-theme') === 'light';
      btns.forEach(function (btn) {
        btn.setAttribute('aria-pressed', isLight ? 'true' : 'false');
      });
    }
    function syncHljs(theme) {
      var link = document.getElementById('hljs-theme') ||
        document.querySelector('link[href*="highlight.js"][href*="/styles/"]');
      if (!link) return;
      var base = 'https://cdn.jsdelivr.net/npm/highlight.js@11/styles/';
      link.href = base + (theme === 'dark' ? 'github-dark.min.css' : 'github.min.css');
    }
    var cur = root.getAttribute('data-theme') || 'light';
    syncPressed();
    syncHljs(cur);
    btns.forEach(function (btn) {
      btn.addEventListener('click', function () {
        var next = root.getAttribute('data-theme') === 'light' ? 'dark' : 'light';
        root.setAttribute('data-theme', next);
        root.setAttribute('data-theme-anim', '');
        try { localStorage.setItem('ds_theme', next); } catch (e) { /* 隐私模式忽略 */ }
        syncPressed();
        syncHljs(next);
      });
    });
  })();

  // 全站升级自定义下拉（官网/分享顶栏语言选择保持原生，与 landing 视觉一致）
  document.querySelectorAll('select').forEach(function (sel) {
    if (sel.closest('.l-lang')) return;
    UI.skinSelect ? UI.skinSelect(sel) : null;
  });
  // 所有者 / 成员悬停信息卡
  UI.bindOwnerHover(document);
  window.bindOwnerHover = UI.bindOwnerHover;
  var chip = document.getElementById('userMenuBtn');
  var dropdown = document.getElementById('userDropdown');
  if (chip && dropdown) {
    chip.addEventListener('click', function (e) {
      e.stopPropagation();
      dropdown.hidden = !dropdown.hidden;
    });
    document.addEventListener('click', function (e) {
      if (!dropdown.hidden && !dropdown.contains(e.target)) dropdown.hidden = true;
    });
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape') dropdown.hidden = true;
    });
  }

  var pwdModal = document.getElementById('pwdModal');
  var pwdBtn = document.getElementById('changePwdBtn');
  var pwdForm = document.getElementById('pwdForm');
  if (pwdModal && window.UI) UI.bindModal(pwdModal);

  // 个人信息弹窗：头像/昵称自助修改，保存后刷新导航栏
  var profileModal = document.getElementById('profileModal');
  var profileBtn = document.getElementById('profileBtn');
  var profileForm = document.getElementById('profileForm');
  if (profileModal) {
    UI.bindModal(profileModal);
    UI.avatarPicker(profileForm.querySelector('[data-avatar-pick]'));
  }
  if (profileBtn && profileModal && dropdown) {
    profileBtn.addEventListener('click', function () {
      dropdown.hidden = true;
      UI.openModal(profileModal);
    });
  }
  if (profileForm && profileModal) {
    profileForm.addEventListener('submit', async function (e) {
      e.preventDefault();
      if (!UI.validateForm(profileForm)) return;
      var fd = new FormData(profileForm);
      try {
        var res = await fetch('/admin/api/profile', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
          body: JSON.stringify({
            nickname: fd.get('nickname'),
            avatar: fd.get('avatar'),
            email: (fd.get('email') || '').trim(),
            phone: (fd.get('phone') || '').trim(),
          }),
        });
        var data = await res.json().catch(function () { return {}; });
        if (res.ok) {
          UI.closeModal(profileModal);
          UI.toast('个人信息已保存', 'success');
          location.reload(); // 刷新导航栏头像/昵称
        } else {
          UI.alert((data && data.error) || '保存失败');
        }
      } catch (err) {
        UI.alert('保存失败：网络错误或服务不可用');
      }
    });
  }

  if (pwdBtn && pwdModal && dropdown) {
    pwdBtn.addEventListener('click', function () {
      dropdown.hidden = true;
      UI.openModal(pwdModal);
    });
  }
  if (pwdForm && pwdModal) {
    pwdForm.addEventListener('submit', async function (e) {
      e.preventDefault();
      if (!UI.validateForm(pwdForm)) return;
      var fd = new FormData(pwdForm);
      var oldPwd = fd.get('old'), newPwd = fd.get('new'), confirmPwd = fd.get('confirm');
      if (newPwd.length < 6) {
        UI.toast('新密码至少 6 位', 'error');
        return;
      }
      if (newPwd !== confirmPwd) {
        UI.toast('两次输入的新密码不一致', 'error');
        return;
      }
      try {
        var res = await fetch('/admin/api/password', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
          body: JSON.stringify({ old_password: oldPwd, new_password: newPwd }),
        });
        var data = await res.json().catch(function () { return {}; });
        if (res.ok) {
          UI.closeModal(pwdModal);
          pwdForm.reset();
          UI.toast('密码已修改', 'success');
        } else {
          UI.alert((data && data.error) || '修改失败');
        }
      } catch (err) {
        UI.alert('修改失败：网络错误或服务不可用');
      }
    });
  }

  // 后台窄屏导航抽屉
  (function bindNavDrawer() {
    var toggle = document.getElementById('navToggle');
    var drawer = document.getElementById('navDrawer');
    var drawerLinks = document.getElementById('navDrawerLinks');
    var srcLinks = document.getElementById('navLinks');
    if (!toggle || !drawer || !drawerLinks || !srcLinks) return;
    function markActive(root) {
      var path = location.pathname;
      root.querySelectorAll('a').forEach(function (a) {
        var href = a.getAttribute('href') || '';
        var on = href === path;
        if (!on && href !== '/' && href !== '/admin') {
          on = path === href || path.indexOf(href + '/') === 0;
        }
        a.classList.toggle('active', on);
      });
    }
    function closeNav() {
      drawer.hidden = true;
      toggle.setAttribute('aria-expanded', 'false');
      document.body.classList.remove('nav-drawer-open');
    }
    function openNav() {
      drawerLinks.innerHTML = '';
      srcLinks.querySelectorAll('a').forEach(function (a) {
        drawerLinks.appendChild(a.cloneNode(true));
      });
      markActive(drawerLinks);
      drawer.hidden = false;
      toggle.setAttribute('aria-expanded', 'true');
      document.body.classList.add('nav-drawer-open');
    }
    toggle.addEventListener('click', function () {
      if (drawer.hidden) openNav(); else closeNav();
    });
    drawer.addEventListener('click', function (e) {
      if (e.target === drawer || e.target.closest('[data-close-nav]')) closeNav();
    });
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape' && !drawer.hidden) closeNav();
    });
  })();

  // 列表窄屏卡片：从 thead 注入 data-label；空行/合并格跳过
  (function enhanceTableCards() {
    document.querySelectorAll('.panel > table.table').forEach(function (table) {
      if (table.closest('.api-doc, .share-access-list, .pending-apply-scroll')) return;
      var ths = Array.prototype.map.call(table.querySelectorAll('thead th'), function (th) {
        return (th.textContent || '').replace(/\s+/g, ' ').trim();
      });
      if (!ths.length) return;
      table.querySelectorAll('tbody tr').forEach(function (tr) {
        if (tr.classList.contains('empty-row') || tr.querySelector('td[colspan]')) return;
        Array.prototype.forEach.call(tr.children, function (td, i) {
          if (ths[i]) td.setAttribute('data-label', ths[i]);
        });
      });
      table.classList.add('table-cards');
      if (table.parentElement && table.parentElement.classList.contains('panel')) {
        table.parentElement.classList.add('panel-cards');
      }
    });
  })();

  // API 文档：窄屏用下拉代替左侧目录
  (function bindDocTocMobile() {
    var side = document.querySelector('.doc-layout > .doc-side');
    var main = document.querySelector('.doc-layout > .doc-main');
    if (!side || !main) return;
    var links = side.querySelectorAll('a[href^="#"]');
    if (!links.length) return;
    var sel = document.createElement('select');
    sel.className = 'doc-toc-mobile';
    sel.setAttribute('aria-label', (window.UI && UI.t) ? UI.t('nav.menu') : 'Menu');
    var opt0 = document.createElement('option');
    opt0.value = '';
    opt0.textContent = (window.UI && UI.t) ? UI.t('apidoc.tocPick') : '目录';
    sel.appendChild(opt0);
    links.forEach(function (a) {
      var o = document.createElement('option');
      o.value = a.getAttribute('href');
      o.textContent = (a.textContent || '').replace(/\s+/g, ' ').trim();
      sel.appendChild(o);
    });
    sel.addEventListener('change', function () {
      if (!sel.value) return;
      var el = document.querySelector(sel.value);
      if (el) el.scrollIntoView({ behavior: 'smooth', block: 'start' });
    });
    main.insertBefore(sel, main.firstChild);
  })();
});
