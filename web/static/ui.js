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
        btn.className = 'btn' + (b.primary ? ' btn-primary' : '');
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
  function openModal(el) {
    if (!el) return;
    el.hidden = false;
    document.body.classList.add('modal-open');
  }
  function closeModal(el) {
    if (!el) return;
    el.hidden = true;
    document.body.classList.remove('modal-open');
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

  return {
    t: t,
    lang: window.__LANG || 'zh-CN',
    toast: toast,
    validateForm: validateForm,
    bindFormValidation: bindFormValidation,
    openModal: openModal,
    closeModal: closeModal,
    bindModal: bindModal,
    syncSelect: syncXs,
    skinSelect: skinSelect,
    avatarPicker: avatarPicker,
    alert: function (message) {
      return dialog({ message: message, buttons: [{ label: '知道了', primary: true, value: true }] });
    },
    confirm: function (message) {
      return dialog({
        message: message,
        buttons: [
          { label: '取消', primary: false, value: false },
          { label: '确定', primary: true, value: true },
        ],
      });
    },
    prompt: function (message, inputType, placeholder) {
      return dialog({
        message: message,
        input: inputType || 'text',
        placeholder: placeholder || '',
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
  // 全站升级自定义下拉
  document.querySelectorAll('select').forEach(function (sel) { UI.skinSelect ? UI.skinSelect(sel) : null; });
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
          body: JSON.stringify({ nickname: fd.get('nickname'), avatar: fd.get('avatar') }),
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
});
