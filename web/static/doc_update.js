// 阅读中探测 content_version：轻量轮询 + 非打断横幅（Markdown / 画布 / HTML 共用）
(function () {
  var knownVersion = 0;
  var updateDismissedFor = -1;
  var pendingRemoteVersion = 0;
  var metaURL = '';
  var onAdopt = null;
  var UPDATE_POLL_MS = 30000;
  var started = false;

  function bannerEl() {
    return document.getElementById('docUpdateBanner');
  }
  function hideBanner() {
    var el = bannerEl();
    if (el) el.hidden = true;
  }
  function showBanner(remoteVer) {
    var el = bannerEl();
    if (!el || remoteVer <= knownVersion || remoteVer === updateDismissedFor) return;
    pendingRemoteVersion = remoteVer;
    el.hidden = false;
  }
  function adopt(ver) {
    if (typeof ver !== 'number' || isNaN(ver)) return;
    knownVersion = ver;
    updateDismissedFor = -1;
    pendingRemoteVersion = 0;
    hideBanner();
    if (typeof onAdopt === 'function') onAdopt(ver);
  }
  async function checkUpdated() {
    if (document.hidden || !metaURL) return;
    try {
      var res = await fetch(metaURL, {
        headers: { 'X-Requested-With': 'XMLHttpRequest' },
        credentials: 'same-origin'
      });
      if (!res.ok) return;
      var data = await res.json().catch(function () { return {}; });
      var remote = typeof data.content_version === 'number' ? data.content_version : parseInt(data.content_version, 10);
      if (!isNaN(remote) && remote > knownVersion) showBanner(remote);
    } catch (e) { /* 网络抖动忽略 */ }
  }
  function start(opts) {
    if (started || !opts || !opts.metaURL) return;
    started = true;
    metaURL = opts.metaURL;
    knownVersion = typeof opts.contentVersion === 'number'
      ? opts.contentVersion
      : parseInt(opts.contentVersion, 10) || 0;
    onAdopt = opts.onAdopt || null;
    if (!bannerEl()) return;
    var reloadBtn = document.getElementById('docUpdateReload');
    var dismissBtn = document.getElementById('docUpdateDismiss');
    if (reloadBtn) {
      reloadBtn.addEventListener('click', function () { location.reload(); });
    }
    if (dismissBtn) {
      dismissBtn.addEventListener('click', function () {
        if (pendingRemoteVersion > 0) updateDismissedFor = pendingRemoteVersion;
        hideBanner();
      });
    }
    setInterval(checkUpdated, UPDATE_POLL_MS);
    document.addEventListener('visibilitychange', function () {
      if (!document.hidden) checkUpdated();
    });
    setTimeout(checkUpdated, 8000);
  }

  window.DocUpdate = { start: start, adopt: adopt };
})();
