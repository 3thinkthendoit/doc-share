// 阅读页（分享/预览）协作逻辑：访客提示、分享编辑、评论
(function () {
  var R = window.READER || {};
  if (!R.docId) return;
  // 评论/编辑接口前缀：分享页走 /s/:token（公开），预览页走 /admin/api/docs/:id（登录态）
  function api(path) {
    return R.token ? ('/s/' + R.token + path) : ('/admin/api/docs/' + R.docId + path);
  }
  function t(s) { return window.UI ? UI.t(s) : s; }

  /* ---- 访客头像：点击弹出昵称/访问信息 ---- */
  var tip = document.getElementById('visitorTip');
  if (tip) {
    document.querySelectorAll('.visitor-avatar').forEach(function (btn) {
      btn.addEventListener('click', function () {
        if (!tip.hidden && tip.dataset.name === btn.dataset.name) { tip.hidden = true; return; }
        tip.dataset.name = btn.dataset.name;
        tip.textContent = btn.dataset.name + ' · ' + t('累计访问') + ' ' + btn.dataset.visits + ' · ' + btn.dataset.last;
        tip.hidden = false;
      });
    });
    document.addEventListener('click', function (e) {
      if (!e.target.closest('.visitor-avatar')) tip.hidden = true;
    });
  }

  /* ---- 分享编辑：登录用户 + 分享开启编辑权限时可见 ---- */
  if (R.canEdit) {
    var editBtn = document.getElementById('editDocBtn');
    var saveBtn = document.getElementById('saveEdit');
    var cancelBtn = document.getElementById('cancelEdit');
    var previewEl = document.getElementById('preview');
    var editArea = document.getElementById('editContent');
    var raw = document.getElementById('raw-markdown');
    if (editBtn && editArea && raw) {
      editBtn.addEventListener('click', function () {
        editArea.value = raw.value;
        previewEl.hidden = true;
        editArea.hidden = false;
        editBtn.hidden = true;
        saveBtn.hidden = false;
        cancelBtn.hidden = false;
        editArea.focus();
      });
      cancelBtn.addEventListener('click', function () {
        previewEl.hidden = false;
        editArea.hidden = true;
        editBtn.hidden = false;
        saveBtn.hidden = true;
        cancelBtn.hidden = true;
      });
      saveBtn.addEventListener('click', async function () {
        var content = editArea.value;
        if (content === raw.value) { cancelBtn.click(); return; }
        saveBtn.disabled = true;
        try {
          var res = await fetch('/s/' + R.token + '/content', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
            body: JSON.stringify({ content: content })
          });
          var data = await res.json();
          if (!res.ok) { UI.alert((data && data.error) || '保存失败'); saveBtn.disabled = false; return; }
          UI.toast('已保存', 'success');
          setTimeout(function () { location.reload(); }, 600);
        } catch (e) {
          UI.alert('保存失败：网络错误');
          saveBtn.disabled = false;
        }
      });
    }
  }

  /* ---- 评论：一级回复，游客以游客身份发表 ---- */
  var listEl = document.getElementById('commentList');
  if (!listEl) return;
  var form = document.getElementById('commentForm');
  var contentEl = document.getElementById('commentContent');
  var replyingEl = document.getElementById('replyingTo');
  var replyTo = 0;
  var comments = [];

  function el(tag, cls, text) {
    var n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text !== undefined) n.textContent = text;
    return n;
  }
  function avatarNode(name, avatarUrl) {
    var wrap = el('span', 'comment-avatar');
    if (avatarUrl) {
      var img = document.createElement('img');
      img.src = avatarUrl;
      img.alt = '';
      wrap.appendChild(img);
    } else {
      wrap.textContent = (name || '?').charAt(0);
    }
    return wrap;
  }
  function fmtTime(iso) {
    return (iso || '').replace('T', ' ').substring(0, 16);
  }

  function render() {
    listEl.innerHTML = '';
    if (!comments.length) {
      listEl.appendChild(el('div', 'muted', t('还没有评论')));
      return;
    }
    var tops = comments.filter(function (c) { return !c.parent_id; });
    tops.forEach(function (c) {
      listEl.appendChild(commentNode(c));
      var replies = comments.filter(function (r) { return r.parent_id === c.id; });
      if (replies.length) {
        var box = el('div', 'comment-replies');
        replies.forEach(function (r) { box.appendChild(commentNode(r, true, c.name)); });
        listEl.appendChild(box);
      }
    });
  }

  // isReply 时传入被回复的顶层评论昵称，内容前展示 @昵称
  function commentNode(c, isReply, parentName) {
    var item = el('div', 'comment-item' + (isReply ? ' is-reply' : ''));
    var head = el('div', 'comment-head');
    head.appendChild(avatarNode(c.name, c.avatar));
    var name = el('span', 'comment-name', c.name);
    head.appendChild(name);
    if (c.is_guest) head.appendChild(el('span', 'comment-guest-tag', t('游客')));
    head.appendChild(el('span', 'comment-time', fmtTime(c.created_at)));
    var actions = el('span', 'comment-actions');
    // 不能回复自己：自己的评论不出现回复按钮（游客无稳定身份，不限制）
    var isSelf = c.user_id > 0 && c.user_id === R.userId;
    if (!isSelf) {
      var replyBtn = el('button', 'btn-link', t('回复'));
      replyBtn.type = 'button';
      replyBtn.addEventListener('click', function () {
        replyTo = c.id;
        replyingEl.hidden = false;
        replyingEl.textContent = t('回复') + ' ' + c.name;
        contentEl.focus();
      });
      actions.appendChild(replyBtn);
    }
    if (R.canModerate) {
      var delBtn = el('button', 'btn-link comment-del', t('删除'));
      delBtn.type = 'button';
      delBtn.addEventListener('click', async function () {
        if (!window.UI || !await UI.confirm(t('确认删除该评论？'), { danger: true })) return;
        var res = await fetch('/admin/api/docs/' + R.docId + '/comments/' + c.id, {
          method: 'DELETE', headers: { 'X-Requested-With': 'XMLHttpRequest' }
        });
        if (res.ok) load(); else UI.toast('删除失败', 'error');
      });
      actions.appendChild(delBtn);
    }
    head.appendChild(actions);
    item.appendChild(head);
    var body = el('div', 'comment-content');
    if (isReply && parentName) {
      var at = el('span', 'comment-reply-to', '@' + parentName);
      body.appendChild(at);
      body.appendChild(document.createTextNode(' '));
    }
    body.appendChild(document.createTextNode(c.content));
    item.appendChild(body);
    return item;
  }

  async function load() {
    try {
      var res = await fetch(api('/comments'), { headers: { 'X-Requested-With': 'XMLHttpRequest' } });
      var data = await res.json();
      comments = (data && data.comments) || [];
    } catch (e) { comments = []; }
    render();
  }

  if (replyingEl) {
    replyingEl.addEventListener('click', function () {
      replyTo = 0;
      replyingEl.hidden = true;
    });
  }
  form.addEventListener('submit', async function (e) {
    e.preventDefault();
    var content = contentEl.value.trim();
    if (!content) { contentEl.focus(); return; }
    var btn = form.querySelector('button[type=submit]');
    btn.disabled = true;
    try {
      var res = await fetch(api('/comments'), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
        body: JSON.stringify({ content: content, parent_id: replyTo })
      });
      var data = await res.json();
      if (!res.ok) { UI.alert((data && data.error) || '发表失败'); return; }
      contentEl.value = '';
      replyTo = 0;
      replyingEl.hidden = true;
      await load();
    } catch (err) {
      UI.alert('发表失败：网络错误');
    } finally {
      btn.disabled = false;
    }
  });
  load();
})();
