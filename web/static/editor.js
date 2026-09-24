// 文档编辑页：实时预览 + 保存 + 分享设置
(function () {
  var titleEl = document.getElementById('docTitle');
  var contentEl = document.getElementById('docContent');
  var previewEl = document.getElementById('preview');
  var saveBtn = document.getElementById('saveBtn');
  if (!contentEl) return;

  if (previewEl) previewEl.classList.add('edit-preview');

  function doPreview() {
    if (!previewEl) return;
    if (window.marked && window.DOMPurify) {
      previewEl.innerHTML = DOMPurify.sanitize(marked.parse(contentEl.value || ''));
    } else {
      // C1：消毒组件未就绪时绝不往 innerHTML 写未净化内容，降级为纯文本
      previewEl.textContent = contentEl.value || '';
    }
  }
  var timer = null;
  contentEl.addEventListener('input', function () {
    clearTimeout(timer);
    timer = setTimeout(doPreview, 150);
  });
  doPreview();

  // Markdown 工具栏
  var uploadInput = document.getElementById('uploadInput');
  var toolbar = document.querySelector('.md-toolbar');
  if (toolbar) {
    toolbar.addEventListener('click', function (e) {
      var btn = e.target.closest('button[data-md]');
      if (btn) mdAction(btn.dataset.md);
    });
  }
  contentEl.addEventListener('keydown', function (e) {
    if (!(e.ctrlKey || e.metaKey)) return;
    var k = e.key.toLowerCase();
    if (k === 'b') { e.preventDefault(); mdAction('bold'); }
    else if (k === 'i') { e.preventDefault(); mdAction('italic'); }
    else if (k === 'k') { e.preventDefault(); mdAction('link'); }
  });

  function mdAction(action) {
    switch (action) {
      case 'bold': wrapSel('**', '**', UI.t('加粗文字')); break;
      case 'italic': wrapSel('*', '*', UI.t('斜体文字')); break;
      case 'strike': wrapSel('~~', '~~', UI.t('删除文字')); break;
      case 'code': wrapSel('`', '`', 'code'); break;
      case 'codeblock': wrapSel('\n```\n', '\n```\n', UI.t('代码')); break;
      case 'h1': prefixLine('# '); break;
      case 'h2': prefixLine('## '); break;
      case 'h3': prefixLine('### '); break;
      case 'quote': prefixLine('> '); break;
      case 'ul': prefixLine('- '); break;
      case 'ol': prefixLine('1. '); break;
      case 'link': wrapSel('[', '](https://)', UI.t('链接文字')); break;
      case 'hr': insertAtCursor('\n---\n'); return;
      // 表格脚手架：列头 / 单元格文字跟着界面语言走
      case 'table': {
        var col = UI.t('列'), cell = UI.t('内容');
        insertAtCursor('\n| ' + col + '1 | ' + col + '2 | ' + col + '3 |\n| --- | --- | --- |\n| ' + cell + ' | ' + cell + ' | ' + cell + ' |\n');
        return;
      }
      case 'image': if (uploadInput) uploadInput.click(); return;
      case 'import': if (importInput) importInput.click(); return;
      default: return;
    }
    doPreview();
    contentEl.focus();
  }
  // 用标记包裹选区（无选区时插入占位文本并选中，方便直接改）
  function wrapSel(before, after, placeholder) {
    var s = contentEl.selectionStart, e = contentEl.selectionEnd;
    var sel = contentEl.value.slice(s, e) || placeholder;
    contentEl.value = contentEl.value.slice(0, s) + before + sel + after + contentEl.value.slice(e);
    contentEl.selectionStart = s + before.length;
    contentEl.selectionEnd = s + before.length + sel.length;
  }
  // 在当前行行首加前缀（标题/引用/列表）
  function prefixLine(prefix) {
    var s = contentEl.selectionStart;
    var lineStart = contentEl.value.lastIndexOf('\n', s - 1) + 1;
    contentEl.value = contentEl.value.slice(0, lineStart) + prefix + contentEl.value.slice(lineStart);
    contentEl.selectionStart = contentEl.selectionEnd = s + prefix.length;
  }

  // 图片上传：工具栏按钮 / 粘贴 / 拖拽，成功后在光标处插入 Markdown
  var importInput = document.getElementById('importInput');
  if (importInput) {
    importInput.addEventListener('change', function () {
      if (importInput.files.length) importDoc(importInput.files[0]);
      importInput.value = '';
    });
  }
  if (uploadInput) {
    uploadInput.addEventListener('change', function () {
      if (uploadInput.files.length) uploadImage(uploadInput.files[0]);
      uploadInput.value = '';
    });
  }
  contentEl.addEventListener('paste', function (e) {
    var items = (e.clipboardData || {}).items || [];
    for (var i = 0; i < items.length; i++) {
      if (items[i].type.indexOf('image/') === 0) {
        var f = items[i].getAsFile();
        if (f) { e.preventDefault(); uploadImage(f); }
        return;
      }
    }
  });
  contentEl.addEventListener('dragover', function (e) { e.preventDefault(); });
  contentEl.addEventListener('drop', function (e) {
    var files = (e.dataTransfer || {}).files || [];
    if (!files.length) return;
    if (files[0].type.indexOf('image/') === 0) {
      e.preventDefault();
      uploadImage(files[0]);
      return;
    }
    // 文档拖拽：pdf / doc / docx 转为 Markdown 插入
    if (/\.(pdf|docx?)$/i.test(files[0].name)) {
      e.preventDefault();
      importDoc(files[0]);
    }
  });

  // 文档导入：pdf / doc / docx 上传转换，结果插入光标处；标题为空时用文件名填充
  async function importDoc(file) {
    var fd = new FormData();
    fd.append('file', file);
    UI.toast('文档转换中，请稍候…', 'info');
    try {
      var res = await fetch('/admin/api/convert', {
        method: 'POST',
        headers: { 'X-Requested-With': 'XMLHttpRequest' },
        body: fd,
      });
      var data = await res.json();
      if (!res.ok) { UI.alert((data && data.error) || '文档转换失败'); return; }
      if (!data || !data.markdown) { UI.alert('文档转换失败：未获得内容'); return; }
      if (titleEl && !titleEl.value) {
        titleEl.value = file.name.replace(/\.[^.]+$/, '');
      }
      insertAtCursor('\n' + data.markdown + '\n');
      UI.toast('已转换为 Markdown 并插入', 'success');
    } catch (e) {
      UI.alert('文档转换失败：网络错误或服务不可用');
    }
  }

  async function uploadImage(file) {
    var fd = new FormData();
    fd.append('file', file);
    UI.toast('图片上传中…', 'info');
    try {
      var res = await fetch('/admin/api/upload', {
        method: 'POST',
        headers: { 'X-Requested-With': 'XMLHttpRequest' },
        body: fd,
      });
      var data = await res.json();
      if (!res.ok) { UI.alert((data && data.error) || '上传失败'); return; }
      insertAtCursor('\n![' + (file.name || 'image') + '](' + data.url + ')\n');
      UI.toast('图片已插入', 'success');
    } catch (e) {
      UI.alert('上传失败：网络错误或服务不可用');
    }
  }
  function insertAtCursor(text) {
    var start = contentEl.selectionStart, end = contentEl.selectionEnd;
    contentEl.value = contentEl.value.slice(0, start) + text + contentEl.value.slice(end);
    contentEl.selectionStart = contentEl.selectionEnd = start + text.length;
    doPreview();
    contentEl.focus();
  }

  var docID = function () { return window.DOC_ID || 0; }; // 惰性读取，避免脚本加载顺序问题

  async function save() {
    var id = docID();
    var payload = {
      title: titleEl.value,
      content: contentEl.value,
      project_id: parseInt(document.getElementById('docProject').value, 10) || 0,
      category_id: parseInt(document.getElementById('docCategory').value, 10) || 0
    };
    var url = id ? '/admin/api/docs/' + id : '/admin/api/docs';
    var method = id ? 'PUT' : 'POST';
    var res, data;
    try {
      res = await fetch(url, {
        method: method,
        headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
        body: JSON.stringify(payload),
      });
      data = await res.json();
    } catch (e) {
      UI.toast('网络错误，保存失败，内容仍在编辑页，请勿刷新', 'error');
      return;
    }
    if (!res.ok) { UI.alert((data && data.error) || '保存失败'); return; }
    if (!id) {
      // 新建成功后跳转到编辑页（含 ID），以便配置分享
      UI.toast('创建成功', 'success');
      setTimeout(function () { window.location.href = '/admin/docs/' + data.data.id + '/edit'; }, 400);
    } else {
      UI.toast('保存成功', 'success');
    }
  }
  if (saveBtn) saveBtn.addEventListener('click', save);

  // 分享设置弹窗（开关/遮罩/ESC 由 UI.bindModal 统一处理）
  var shareModal = document.getElementById('shareModal');
  var openShareBtn = document.getElementById('shareBtn');
  UI.bindModal(shareModal);
  if (openShareBtn) openShareBtn.addEventListener('click', function () { UI.openModal(shareModal); });

  var enabledEl = document.getElementById('shareEnabled');
  var configEl = document.getElementById('shareConfig');
  var saveShareBtn = document.getElementById('saveShareBtn');
  if (enabledEl) {
    enabledEl.addEventListener('change', function () {
      configEl.style.display = enabledEl.checked ? 'block' : 'none';
    });
  }
  if (saveShareBtn) {
    saveShareBtn.addEventListener('click', async function () {
      var payload = {
        enabled: enabledEl.checked,
        can_edit: document.getElementById('shareCanEdit').checked,
        password: document.getElementById('sharePassword').value,
        expire_days: parseInt(document.getElementById('shareExpire').value, 10) || 0,
      };
      var res, data;
      try {
        res = await fetch('/admin/api/docs/' + docID() + '/share', {
          method: enabledEl.checked ? 'POST' : 'DELETE',
          headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
          body: JSON.stringify(payload),
        });
        data = await res.json();
      } catch (e) {
        UI.toast('网络错误，分享设置失败', 'error');
        return;
      }
      if (!res.ok) { UI.alert((data && data.error) || '操作失败'); return; }
      if (data.url) {
        document.getElementById('shareURL').textContent = data.url;
        document.getElementById('shareLink').style.display = 'flex';
      } else if (!enabledEl.checked) {
        document.getElementById('shareLink').style.display = 'none';
      }
      // 密码明文本机记忆（供复制用），关闭分享时清除
      var pwdKey = sharePwdKey(data.url || (document.getElementById('shareURL') || {}).textContent);
      try {
        if (enabledEl.checked && payload.password.trim()) {
          localStorage.setItem(pwdKey, payload.password.trim());
        } else if (!enabledEl.checked && pwdKey) {
          localStorage.removeItem(pwdKey);
        }
      } catch (e) { /* 隐私模式忽略 */ }
      UI.toast(enabledEl.checked ? '分享设置已更新' : '已关闭分享', 'success');
    });
  }

  /* ---- 分享密码：生成 / 重置 / 勾选开启时自动填充 ---- */
  function genPassword() {
    // 去掉易混淆字符（0/o、1/l/I），6 位数字+字母
    var chars = 'abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789';
    var out = '';
    var buf = new Uint32Array(6);
    (window.crypto || window.msCrypto).getRandomValues(buf);
    for (var i = 0; i < 6; i++) out += chars[buf[i] % chars.length];
    return out;
  }
  var pwdInput = document.getElementById('sharePassword');
  var genPwdBtn = document.getElementById('genPwdBtn');
  if (genPwdBtn && pwdInput) {
    genPwdBtn.addEventListener('click', function () {
      pwdInput.value = genPassword();
      pwdInput.focus();
    });
  }
  if (enabledEl && pwdInput) {
    enabledEl.addEventListener('change', function () {
      // 首次开启且没有历史密码时自动生成，省得手动想
      if (enabledEl.checked && !pwdInput.value.trim() && !pwdInput.dataset.hasPwd) {
        pwdInput.value = genPassword();
      }
    });
  }

  function flash(btn, text) {
    if (!btn) return;
    var old = btn.textContent;
    btn.textContent = text;
    setTimeout(function () { btn.textContent = old; }, 1200);
  }

  /* ---- 历史版本：查看修订列表 + 一键回滚 ---- */
  var revisionsModal = document.getElementById('revisionsModal');
  var revisionsBtn = document.getElementById('revisionsBtn');
  if (revisionsModal && revisionsBtn) {
    UI.bindModal(revisionsModal);
    var revList = document.getElementById('revisionsList');
    revisionsBtn.addEventListener('click', async function () {
      UI.openModal(revisionsModal);
      revList.innerHTML = '<div class="muted">加载中…</div>';
      try {
        var res = await fetch('/admin/api/docs/' + docID() + '/revisions', {
          headers: { 'X-Requested-With': 'XMLHttpRequest' }
        });
        var data = await res.json();
        var revs = (data && data.revisions) || [];
        revList.innerHTML = '';
        if (!revs.length) {
          var empty = document.createElement('div');
          empty.className = 'muted';
          empty.textContent = UI.t('暂无历史版本');
          revList.appendChild(empty);
          return;
        }
        revs.forEach(function (r) {
          var row = document.createElement('div');
          row.className = 'revision-row';
          var info = document.createElement('span');
          info.className = 'revision-info';
          info.textContent = (r.created_at || '').replace('T', ' ').substring(0, 16) + ' · ' + (r.editor_name || '-');
          row.appendChild(info);
          var btn = document.createElement('button');
          btn.type = 'button';
          btn.className = 'btn btn-sm';
          btn.textContent = UI.t('恢复此版本');
          btn.addEventListener('click', async function () {
            if (!await UI.confirm('回滚到此版本？当前内容会先存为新版本。')) return;
            btn.disabled = true;
            var res2 = await fetch('/admin/api/docs/' + docID() + '/revisions/' + r.id + '/rollback', {
              method: 'POST', headers: { 'X-Requested-With': 'XMLHttpRequest' }
            });
            if (res2.ok) { location.reload(); return; }
            var d2 = await res2.json().catch(function () { return {}; });
            UI.alert((d2 && d2.error) || '回滚失败');
            btn.disabled = false;
          });
          row.appendChild(btn);
          revList.appendChild(row);
        });
      } catch (e) {
        revList.innerHTML = '<div class="muted">加载失败</div>';
      }
    });
  }
})();

// 分享密码本机记忆的存储键（从 /s/<token> 链接中提取 token）
function sharePwdKey(url) {
  var m = String(url || '').match(/\/s\/([A-Za-z0-9]+)/);
  return m ? 'ds_share_pwd_' + m[1] : '';
}
function storedSharePwd(url) {
  var key = sharePwdKey(url);
  if (!key) return '';
  try { return localStorage.getItem(key) || ''; } catch (e) { return ''; }
}

async function copyShare() {
  var urlEl = document.getElementById('shareURL');
  var url = (urlEl && urlEl.textContent || '').trim();
  if (!url) { UI.toast('暂无分享链接', 'error'); return; }
  // 组合复制：标题 + 链接 + 访问密码（输入框里的新密码优先，其次本机记忆的密码）
  var titleEl = document.getElementById('docTitle');
  var title = (titleEl && titleEl.value || '').trim();
  var pwdInput = document.getElementById('sharePassword');
  var pwd = (pwdInput && pwdInput.value.trim()) || storedSharePwd(url);
  var lines = [];
  if (title) lines.push(UI.t('标题：') + title);
  lines.push(UI.t('链接：') + url);
  lines.push(UI.t('访问密码：') + (pwd || UI.t('无')));
  var text = lines.join('\n');
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
    } else {
      var ta = document.createElement('textarea');
      ta.value = text;
      ta.style.position = 'fixed';
      ta.style.opacity = '0';
      document.body.appendChild(ta);
      ta.select();
      var ok = document.execCommand('copy');
      ta.remove();
      if (!ok) throw new Error('copy failed');
    }
    UI.toast('复制成功', 'success');
  } catch (e) {
    UI.toast('复制失败，请手动选择链接复制', 'error');
  }
}
