// 阅读页（预览/分享）文档绑定对话面板
(function () {
  var R = window.READER || {};
  if (!R.docId) return;

  var fab = document.getElementById('readerChatFab');
  var panel = document.getElementById('readerChatPanel');
  var closeBtn = document.getElementById('readerChatClose');
  var fullLink = document.getElementById('readerChatFullLink');
  var msgsEl = document.getElementById('readerChatMsgs');
  var form = document.getElementById('readerChatForm');
  var input = document.getElementById('readerChatInput');
  var sendBtn = document.getElementById('readerChatSend');
  if (!fab || !panel || !form || !msgsEl || !input) return;

  function t(s) { return window.UI ? UI.t(s) : s; }
  function api(path) {
    if (R.apiBase) return R.apiBase + path;
    return R.token ? ('/s/' + R.token + path) : ('/console/api/docs/' + R.docId + path);
  }

  var sessionId = 0;
  var busy = false;

  function showPanel(open) {
    panel.hidden = !open;
    fab.classList.toggle('is-open', open);
    if (open) input.focus();
  }

  fab.addEventListener('click', async function () {
    if (!R.loggedIn) {
      if (window.UI && UI.confirm) {
        if (await UI.confirm(t('readerChat.needLogin'))) {
          location.href = R.loginURL || '/login';
        }
      } else if (confirm(t('readerChat.needLogin'))) {
        location.href = R.loginURL || '/login';
      }
      return;
    }
    if (!panel.hidden) { showPanel(false); return; }
    showPanel(true);
    if (!sessionId) await ensureSession();
    else await loadMessages();
  });
  if (closeBtn) closeBtn.addEventListener('click', function () { showPanel(false); });

  async function ensureSession() {
    msgsEl.innerHTML = (window.UI && UI.loadingHTML) ? UI.loadingHTML('readerChat.loading') : ('<div class="muted">' + t('readerChat.loading') + '</div>');
    try {
      var res = await fetch(api('/chat-session'), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
        body: '{}'
      });
      var data = await res.json().catch(function () { return {}; });
      if (!res.ok) {
        msgsEl.innerHTML = '<div class="muted">' + ((data && data.error) || t('readerChat.openFail')) + '</div>';
        return;
      }
      sessionId = data.session && data.session.id;
      if (fullLink && sessionId) {
        fullLink.href = '/console/chat?sid=' + encodeURIComponent(sessionId);
        fullLink.hidden = false;
      }
      await loadMessages();
    } catch (e) {
      msgsEl.innerHTML = '<div class="muted">' + t('readerChat.openFail') + '</div>';
    }
  }

  function appendBubble(role, text) {
    var empty = msgsEl.querySelector('.muted');
    if (empty && !msgsEl.querySelector('.reader-chat-bubble')) msgsEl.innerHTML = '';
    var div = document.createElement('div');
    div.className = 'reader-chat-bubble is-' + role;
    div.textContent = text || '';
    msgsEl.appendChild(div);
    msgsEl.scrollTop = msgsEl.scrollHeight;
    return div;
  }

  // 流式助手气泡：可挂思考过程（与完整对话页同源 SSE 的 think 事件）
  function appendAssistantStream() {
    var empty = msgsEl.querySelector('.muted');
    if (empty && !msgsEl.querySelector('.reader-chat-bubble')) msgsEl.innerHTML = '';
    var wrap = document.createElement('div');
    wrap.className = 'reader-chat-bubble is-assistant is-streaming';
    var body = document.createElement('div');
    body.className = 'reader-chat-body is-waiting';
    body.textContent = t('chat.working');
    wrap.appendChild(body);
    msgsEl.appendChild(wrap);
    msgsEl.scrollTop = msgsEl.scrollHeight;
    return { wrap: wrap, body: body, think: null, thinkBody: null, waiting: true };
  }
  function clearWaiting(ai) {
    if (!ai.waiting) return;
    ai.waiting = false;
    ai.body.classList.remove('is-waiting');
    ai.body.textContent = '';
  }
  function showThink(ai, s) {
    if (!ai.think) {
      ai.think = document.createElement('details');
      ai.think.className = 'reader-chat-think';
      ai.think.open = true;
      var sum = document.createElement('summary');
      sum.textContent = t('chat.thinking');
      ai.thinkBody = document.createElement('div');
      ai.thinkBody.className = 'reader-chat-think-body';
      ai.think.appendChild(sum);
      ai.think.appendChild(ai.thinkBody);
      ai.wrap.insertBefore(ai.think, ai.body);
    }
    if (!ai.think.open) ai.think.open = true;
    ai.thinkBody.textContent += s;
    ai.thinkBody.scrollTop = ai.thinkBody.scrollHeight;
    msgsEl.scrollTop = msgsEl.scrollHeight;
  }

  async function loadMessages() {
    if (!sessionId) return;
    try {
      var res = await fetch('/console/api/chat/sessions/' + sessionId + '/messages', {
        headers: { 'X-Requested-With': 'XMLHttpRequest' }
      });
      var data = await res.json().catch(function () { return {}; });
      if (!res.ok) {
        msgsEl.innerHTML = '<div class="muted">' + ((data && data.error) || t('readerChat.loadFail')) + '</div>';
        return;
      }
      var list = (data && data.messages) || [];
      msgsEl.innerHTML = '';
      if (!list.length) {
        msgsEl.innerHTML = '<div class="muted">' + t('readerChat.hint') + '</div>';
        return;
      }
      list.forEach(function (m) {
        if (m.role === 'user' || m.role === 'assistant') {
          appendBubble(m.role === 'user' ? 'user' : 'assistant', m.content);
        }
      });
    } catch (e) {
      msgsEl.innerHTML = '<div class="muted">' + t('readerChat.loadFail') + '</div>';
    }
  }

  async function streamCompletion(content) {
    appendBubble('user', content);
    var ai = appendAssistantStream();
    busy = true;
    sendBtn.disabled = true;
    try {
      var res = await fetch('/console/api/chat/sessions/' + sessionId + '/completions', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
        body: JSON.stringify({ content: content })
      });
      if (!res.ok) {
        var err = await res.json().catch(function () { return {}; });
        clearWaiting(ai);
        ai.body.textContent = (err && err.error) || t('readerChat.sendFail');
        ai.wrap.classList.remove('is-streaming');
        return;
      }
      var reader = res.body && res.body.getReader();
      if (!reader) {
        clearWaiting(ai);
        ai.body.textContent = t('readerChat.sendFail');
        ai.wrap.classList.remove('is-streaming');
        return;
      }
      var decoder = new TextDecoder();
      var buf = '';
      var acc = '';
      while (true) {
        var chunk = await reader.read();
        if (chunk.done) break;
        buf += decoder.decode(chunk.value, { stream: true });
        var parts = buf.split('\n');
        buf = parts.pop() || '';
        for (var i = 0; i < parts.length; i++) {
          var line = parts[i].trim();
          if (line.indexOf('data:') !== 0) continue;
          var payload = line.slice(5).trim();
          if (!payload || payload === '[DONE]') continue;
          try {
            var ev = JSON.parse(payload);
            if (ev.think) {
              clearWaiting(ai);
              showThink(ai, ev.think);
            } else if (ev.delta) {
              clearWaiting(ai);
              if (ai.think) ai.think.open = false;
              acc += ev.delta;
              ai.body.textContent = acc;
              msgsEl.scrollTop = msgsEl.scrollHeight;
            } else if (ev.error) {
              clearWaiting(ai);
              ai.body.textContent = ev.error;
            } else if (ev.reset) {
              acc = '';
              clearWaiting(ai);
              ai.body.textContent = '';
              if (ai.think) {
                ai.think.remove();
                ai.think = null;
                ai.thinkBody = null;
              }
            } else if (ev.approval && ev.approval.id) {
              var tip = t('readerChat.approve') + ' ' + (ev.approval.tool || '');
              var ok = window.UI && UI.confirm
                ? await UI.confirm(tip)
                : confirm(tip);
              try {
                await fetch('/console/api/chat/approvals/' + encodeURIComponent(ev.approval.id), {
                  method: 'POST',
                  headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
                  body: JSON.stringify({ approve: !!ok })
                });
              } catch (_) { /* ignore */ }
            }
          } catch (_) { /* ignore malformed */ }
        }
      }
      if (!acc && !ai.think) {
        clearWaiting(ai);
        ai.body.textContent = t('readerChat.emptyReply');
      }
      ai.wrap.classList.remove('is-streaming');
    } catch (e) {
      clearWaiting(ai);
      ai.body.textContent = t('readerChat.sendFail');
      ai.wrap.classList.remove('is-streaming');
    } finally {
      busy = false;
      sendBtn.disabled = false;
    }
  }

  form.addEventListener('submit', async function (e) {
    e.preventDefault();
    if (busy) return;
    var content = (input.value || '').trim();
    if (!content) return;
    if (!sessionId) await ensureSession();
    if (!sessionId) return;
    input.value = '';
    await streamCompletion(content);
  });

  input.addEventListener('keydown', function (e) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      form.requestSubmit();
    }
  });
})();
