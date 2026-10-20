// 阅读页（分享/预览）协作逻辑：访客提示、分享编辑、评论
(function () {
  var R = window.READER || {};
  if (!R.docId) return;
  // 评论/编辑接口前缀：项目分享页走 apiBase（/ps/:token/d/:docId），
  // 文档分享页走 /s/:token（公开），预览页走 /console/api/docs/:id（登录态）
  function api(path) {
    if (R.apiBase) return R.apiBase + path;
    return R.token ? ('/s/' + R.token + path) : ('/console/api/docs/' + R.docId + path);
  }
  function t(s) { return window.UI ? UI.t(s) : s; }

  /* ---- 顶栏 PDF/DOCX：fetch 触发下载，失败提示（避免浏览器直接打开 JSON） ---- */
  document.querySelectorAll('.doc-export-btn').forEach(function (a) {
    a.addEventListener('click', async function (e) {
      e.preventDefault();
      if (a.dataset.busy) return;
      a.dataset.busy = '1';
      a.setAttribute('aria-busy', 'true');
      try {
        var res = await fetch(a.href, { credentials: 'same-origin' });
        var ct = (res.headers.get('Content-Type') || '').toLowerCase();
        if (!res.ok || ct.indexOf('application/json') === 0) {
          var data = {};
          try { data = await res.json(); } catch (_) {}
          if (window.UI && UI.alert) UI.alert((data && data.error) || t('share.exportFail'));
          else alert((data && data.error) || t('share.exportFail'));
          return;
        }
        var blob = await res.blob();
        var dispo = res.headers.get('Content-Disposition') || '';
        var name = 'document';
        var m = /filename\*=UTF-8''([^;]+)/i.exec(dispo) || /filename="([^"]+)"/i.exec(dispo);
        if (m) {
          try { name = decodeURIComponent(m[1]); } catch (_) { name = m[1]; }
        }
        var url = URL.createObjectURL(blob);
        var link = document.createElement('a');
        link.href = url;
        link.download = name;
        document.body.appendChild(link);
        link.click();
        link.remove();
        URL.revokeObjectURL(url);
      } catch (err) {
        if (window.UI && UI.alert) UI.alert(t('share.exportFail'));
        else alert(t('share.exportFail'));
      } finally {
        delete a.dataset.busy;
        a.removeAttribute('aria-busy');
      }
    });
  });

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
        if (window.DocRender) {
          DocRender.renderMarkdown(raw.value, previewEl);
          DocRender.hydrateEmbeds(previewEl);
        }
      });
      saveBtn.addEventListener('click', async function () {
        var content = editArea.value;
        if (content === raw.value) { cancelBtn.click(); return; }
        saveBtn.disabled = true;
        try {
          var res = await fetch(api('/content'), {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
            body: JSON.stringify({ content: content })
          });
          var data = await res.json();
          if (!res.ok) { UI.alert((data && data.error) || '保存失败'); saveBtn.disabled = false; return; }
          UI.toast('已保存', 'success');
          raw.value = content;
          previewEl.hidden = false;
          editArea.hidden = true;
          editBtn.hidden = false;
          saveBtn.hidden = true;
          cancelBtn.hidden = true;
          saveBtn.disabled = false;
          if (window.DocRender) {
            DocRender.renderMarkdown(content, previewEl);
            DocRender.hydrateEmbeds(previewEl);
          } else {
            setTimeout(function () { location.reload(); }, 600);
          }
        } catch (e) {
          UI.alert('保存失败：网络错误');
          saveBtn.disabled = false;
        }
      });
    }
  }

  /* ---- 评论：一级回复；登录用户可附最多 3 张图（工具栏 + 粘贴） ---- */
  var listEl = document.getElementById('commentList');
  if (!listEl) return;
  var form = document.getElementById('commentForm');
  var contentEl = document.getElementById('commentContent');
  var replyingEl = document.getElementById('replyingTo');
  var imgBtn = document.getElementById('commentImageBtn');
  var imgInput = document.getElementById('commentImageInput');
  var imgPreviews = document.getElementById('commentImagePreviews');
  var replyTo = 0;
  var comments = [];
  var pendingImages = []; // { url, uploading? }
  var COMMENT_MAX_IMAGES = 3;
  var uploadChain = Promise.resolve(); // 串行上传，避免并发占位超过上限

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
  function pendingImageCount() {
    return pendingImages.filter(function (x) { return x.url || x.uploading; }).length;
  }
  function safeCommentImageURL(url) {
    if (!url || typeof url !== 'string') return '';
    var u = url.trim();
    if (!u) return '';
    // 与后端一致：本站 /uploads/…；RustFS 等绝对 http(s)（列表接口已二次校验）
    if (u.indexOf('/uploads/') === 0) return u;
    if (/^https?:\/\//i.test(u)) return u;
    return '';
  }

  function renderPendingImages() {
    if (!imgPreviews) return;
    imgPreviews.innerHTML = '';
    if (!pendingImages.length) {
      imgPreviews.hidden = true;
      return;
    }
    imgPreviews.hidden = false;
    pendingImages.forEach(function (item, idx) {
      var card = el('div', 'comment-img-card' + (item.uploading ? ' is-uploading' : ''));
      if (item.url) {
        var img = document.createElement('img');
        img.src = item.url;
        img.alt = '';
        card.appendChild(img);
      } else {
        card.appendChild(el('span', 'comment-img-loading', '…'));
      }
      var rm = el('button', 'comment-img-remove', '×');
      rm.type = 'button';
      rm.setAttribute('aria-label', t('移除图片'));
      rm.addEventListener('click', function () {
        pendingImages.splice(idx, 1);
        renderPendingImages();
      });
      card.appendChild(rm);
      imgPreviews.appendChild(card);
    });
  }

  async function uploadCommentFile(file) {
    if (!R.loggedIn) {
      UI.toast(t('登录后才能上传图片'), 'error');
      return null;
    }
    if (pendingImageCount() >= COMMENT_MAX_IMAGES) {
      UI.toast(t('每条评论最多 3 张图片'), 'error');
      return null;
    }
    var placeholder = { url: '', uploading: true };
    pendingImages.push(placeholder);
    renderPendingImages();
    try {
      var fd = new FormData();
      fd.append('file', file, file.name || 'paste.png');
      var res = await fetch('/console/api/upload', {
        method: 'POST',
        headers: { 'X-Requested-With': 'XMLHttpRequest' },
        body: fd,
        credentials: 'same-origin'
      });
      var data = await res.json().catch(function () { return {}; });
      if (!res.ok || !data.url) {
        pendingImages = pendingImages.filter(function (x) { return x !== placeholder; });
        renderPendingImages();
        UI.toast((data && data.error) || t('图片上传失败'), 'error');
        return null;
      }
      var safe = safeCommentImageURL(data.url);
      if (!safe) {
        pendingImages = pendingImages.filter(function (x) { return x !== placeholder; });
        renderPendingImages();
        UI.toast(t('图片上传失败'), 'error');
        return null;
      }
      placeholder.url = safe;
      placeholder.uploading = false;
      renderPendingImages();
      return safe;
    } catch (e) {
      pendingImages = pendingImages.filter(function (x) { return x !== placeholder; });
      renderPendingImages();
      UI.toast(t('图片上传失败'), 'error');
      return null;
    }
  }

  function queueCommentFiles(fileList) {
    var files = Array.prototype.slice.call(fileList || []).filter(function (f) {
      return f && f.type && f.type.indexOf('image/') === 0;
    });
    if (!files.length) return uploadChain;
    uploadChain = uploadChain.then(async function () {
      for (var i = 0; i < files.length; i++) {
        if (pendingImageCount() >= COMMENT_MAX_IMAGES) {
          UI.toast(t('每条评论最多 3 张图片'), 'error');
          break;
        }
        await uploadCommentFile(files[i]);
      }
    }).catch(function () { /* 单次失败不阻断后续队列 */ });
    return uploadChain;
  }

  if (imgBtn && imgInput && R.loggedIn) {
    imgBtn.addEventListener('click', function () { imgInput.click(); });
    imgInput.addEventListener('change', function () {
      if (imgInput.files && imgInput.files.length) queueCommentFiles(imgInput.files);
      imgInput.value = '';
    });
  }
  if (contentEl && R.loggedIn) {
    contentEl.addEventListener('paste', function (e) {
      var items = e.clipboardData && e.clipboardData.items;
      if (!items) return;
      var files = [];
      for (var i = 0; i < items.length; i++) {
        if (items[i].type && items[i].type.indexOf('image/') === 0) {
          var f = items[i].getAsFile();
          if (f) files.push(f);
        }
      }
      if (!files.length) return;
      e.preventDefault();
      queueCommentFiles(files);
    });
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

  function appendMentionedText(body, text) {
    if (!text) return;
    var re = /@([A-Za-z0-9_\u4e00-\u9fff.-]{1,64})/g;
    var last = 0;
    var m;
    while ((m = re.exec(text))) {
      if (m.index > last) body.appendChild(document.createTextNode(text.slice(last, m.index)));
      var mark = el('span', 'comment-mention', m[0]);
      body.appendChild(mark);
      last = m.index + m[0].length;
    }
    if (last < text.length) body.appendChild(document.createTextNode(text.slice(last)));
  }

  // isReply 时传入被回复的顶层评论昵称，内容前展示 @昵称
  function commentNode(c, isReply, parentName) {
    var item = el('div', 'comment-item' + (isReply ? ' is-reply' : '') + (c.is_bot ? ' is-bot' : ''));
    var head = el('div', 'comment-head');
    head.appendChild(avatarNode(c.name, c.avatar));
    var name = el('span', 'comment-name', c.name);
    head.appendChild(name);
    if (c.is_bot) head.appendChild(el('span', 'comment-bot-tag', t('comments.bot')));
    else if (c.is_guest) head.appendChild(el('span', 'comment-guest-tag', t('游客')));
    head.appendChild(el('span', 'comment-time', fmtTime(c.created_at)));
    var actions = el('span', 'comment-actions');
    // 不能回复自己：自己的评论不出现回复按钮（游客无稳定身份，不限制）；智能体可回复
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
        var res = await fetch('/console/api/docs/' + R.docId + '/comments/' + c.id, {
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
    if (c.content) appendMentionedText(body, c.content);
    item.appendChild(body);
    var imgs = Array.isArray(c.images) ? c.images : [];
    if (imgs.length) {
      var gallery = el('div', 'comment-images');
      imgs.forEach(function (raw) {
        var src = safeCommentImageURL(raw);
        if (!src) return;
        var btn = document.createElement('button');
        btn.type = 'button';
        btn.className = 'comment-image-link';
        btn.title = t('查看大图');
        var im = document.createElement('img');
        im.src = src;
        im.alt = '';
        im.loading = 'lazy';
        btn.appendChild(im);
        btn.addEventListener('click', function () {
          if (window.Embeds && typeof Embeds.openLightbox === 'function') {
            Embeds.openLightbox(src, '');
            return;
          }
          window.open(src, '_blank', 'noopener,noreferrer');
        });
        gallery.appendChild(btn);
      });
      if (gallery.childNodes.length) item.appendChild(gallery);
    }
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
    hideMentionPopup();
    var content = contentEl.value.trim();
    var images = pendingImages.filter(function (x) { return x.url && !x.uploading; }).map(function (x) { return x.url; });
    if (pendingImages.some(function (x) { return x.uploading; })) {
      UI.toast(t('图片上传中，请稍候'), 'info');
      return;
    }
    if (!content && !images.length) { contentEl.focus(); return; }
    var wantsAgent = /@DocShare\b/i.test(content);
    if (wantsAgent && !R.loggedIn) {
      if (window.UI && UI.alert) UI.alert(t('comments.loginForAgent'));
      else alert(t('comments.loginForAgent'));
      return;
    }
    var btn = form.querySelector('button[type=submit]');
    btn.disabled = true;
    try {
      var res = await fetch(api('/comments'), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
        body: JSON.stringify({ content: content, parent_id: replyTo, images: images })
      });
      var data = await res.json();
      if (!res.ok) { UI.alert((data && data.error) || '发表失败'); return; }
      contentEl.value = '';
      pendingImages = [];
      renderPendingImages();
      replyTo = 0;
      replyingEl.hidden = true;
      await load();
      if (wantsAgent) pollAgentReply(data && data.id, 8);
    } catch (err) {
      UI.alert('发表失败：网络错误');
    } finally {
      btn.disabled = false;
    }
  });

  /* ---- @ 提及自动补全 ---- */
  var mentionPopup = document.getElementById('mentionPopup');
  var mentionItems = [];
  var mentionIndex = -1;
  var mentionTimer = null;
  var mentionActive = null; // { start, end }

  function hideMentionPopup() {
    if (mentionPopup) mentionPopup.hidden = true;
    mentionItems = [];
    mentionIndex = -1;
    mentionActive = null;
  }

  function mentionQueryAt(text, caret) {
    var before = text.slice(0, caret);
    var m = /(^|[\s\u3000])@([A-Za-z0-9_\u4e00-\u9fff.-]{0,64})$/.exec(before);
    if (!m) return null;
    return { start: caret - m[2].length - 1, end: caret, q: m[2] };
  }

  function renderMentionPopup(list) {
    if (!mentionPopup) return;
    mentionItems = list || [];
    mentionIndex = mentionItems.length ? 0 : -1;
    mentionPopup.innerHTML = '';
    if (!mentionItems.length) {
      mentionPopup.hidden = true;
      return;
    }
    mentionItems.forEach(function (item, i) {
      var btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'mention-item' + (i === mentionIndex ? ' is-active' : '');
      btn.setAttribute('role', 'option');
      var label = item.kind === 'bot'
        ? '@' + item.username + ' · ' + t('comments.bot')
        : '@' + item.username + (item.nickname && item.nickname !== item.username ? '（' + item.nickname + '）' : '');
      btn.textContent = label;
      btn.addEventListener('mousedown', function (ev) {
        ev.preventDefault();
        pickMention(item);
      });
      mentionPopup.appendChild(btn);
    });
    mentionPopup.hidden = false;
  }

  function pickMention(item) {
    if (!contentEl || !mentionActive) return;
    var v = contentEl.value;
    var insert = '@' + item.username + ' ';
    contentEl.value = v.slice(0, mentionActive.start) + insert + v.slice(mentionActive.end);
    var pos = mentionActive.start + insert.length;
    contentEl.focus();
    contentEl.setSelectionRange(pos, pos);
    hideMentionPopup();
  }

  async function fetchMentions(q) {
    try {
      var res = await fetch(api('/mention-candidates?q=' + encodeURIComponent(q || '')), {
        headers: { 'X-Requested-With': 'XMLHttpRequest' }
      });
      var data = await res.json();
      return (data && data.candidates) || [];
    } catch (e) {
      return [];
    }
  }

  function scheduleMentionLookup() {
    if (!contentEl || !mentionPopup) return;
    var caret = contentEl.selectionStart || 0;
    var hit = mentionQueryAt(contentEl.value, caret);
    if (!hit) { hideMentionPopup(); return; }
    mentionActive = hit;
    clearTimeout(mentionTimer);
    mentionTimer = setTimeout(async function () {
      var list = await fetchMentions(hit.q);
      if (!mentionActive || mentionActive.start !== hit.start) return;
      renderMentionPopup(list);
    }, 120);
  }

  if (contentEl && mentionPopup) {
    contentEl.addEventListener('input', scheduleMentionLookup);
    contentEl.addEventListener('click', scheduleMentionLookup);
    contentEl.addEventListener('keyup', function (e) {
      if (e.key === 'Escape') { hideMentionPopup(); return; }
      if (mentionPopup.hidden) {
        if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') scheduleMentionLookup();
        return;
      }
      if (e.key === 'ArrowDown') {
        e.preventDefault();
        mentionIndex = Math.min(mentionIndex + 1, mentionItems.length - 1);
        renderMentionPopup(mentionItems);
      } else if (e.key === 'ArrowUp') {
        e.preventDefault();
        mentionIndex = Math.max(mentionIndex - 1, 0);
        renderMentionPopup(mentionItems);
      } else if (e.key === 'Enter' || e.key === 'Tab') {
        if (mentionIndex >= 0 && mentionItems[mentionIndex]) {
          e.preventDefault();
          pickMention(mentionItems[mentionIndex]);
        }
      } else {
        scheduleMentionLookup();
      }
    });
    document.addEventListener('click', function (e) {
      if (!e.target.closest('.comment-compose')) hideMentionPopup();
    });
  }

  function pollAgentReply(commentId, left) {
    if (!commentId || left <= 0) return;
    setTimeout(async function () {
      await load();
      // 智能体回复挂在顶层评论下；派工可能是顶层也可能是回复
      var hasBot = comments.some(function (c) {
        return c.is_bot && c.id > commentId;
      });
      if (hasBot) return;
      pollAgentReply(commentId, left - 1);
    }, 2500);
 }

  load();
})();
