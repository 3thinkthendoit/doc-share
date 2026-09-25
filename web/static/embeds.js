// Markdown 围栏嵌入：```mindmap / ```excalidraw
// 编辑页用卡片占位；预览/分享页若有 previewUrl 则展示大图。
// 弹层保存时导出 PNG 上传，URL 写入围栏 JSON 的 previewUrl。
(function () {
  var FENCE_LANGS = { mindmap: true, excalidraw: true };
  var CDN = {
    mindmapJs: 'https://cdn.jsdelivr.net/npm/simple-mind-map@0.14.0/dist/simpleMindMap.umd.min.js',
    mindmapCss: 'https://cdn.jsdelivr.net/npm/simple-mind-map@0.14.0/dist/simpleMindMap.esm.css',
    mindmapThemesJs: 'https://cdn.jsdelivr.net/npm/simple-mind-map-plugin-themes@1.0.1/dist/themes.iife.min.js',
    excalidrawJs: 'https://cdn.jsdelivr.net/npm/excalidraw-embed@0.18.4/dist/excalidraw-embed.umd.js',
    excalidrawCss: 'https://cdn.jsdelivr.net/npm/excalidraw-embed@0.18.4/dist/excalidraw-embed.css',
    // 高清导出：需 React + ExcalidrawLib.exportToBlob（与截屏降级区分）
    reactJs: 'https://cdn.jsdelivr.net/npm/react@18.2.0/umd/react.production.min.js',
    reactDomJs: 'https://cdn.jsdelivr.net/npm/react-dom@18.2.0/umd/react-dom.production.min.js',
    excalidrawLibJs: 'https://cdn.jsdelivr.net/npm/@excalidraw/excalidraw@0.17.6/dist/excalidraw.production.min.js'
  };
  var payloads = [];
  var markedInstalled = false;
  var loaders = {};

  function t(key, fallback) {
    if (window.UI && UI.t) {
      var v = UI.t(key);
      if (v && v !== key) return v;
    }
    return fallback || key;
  }

  function escapeHtml(s) {
    return String(s || '')
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;');
  }

  function defaultMindmap() {
    return {
      data: { text: t('edit.embedMindmapRoot', '中心主题') },
      children: [
        { data: { text: t('edit.embedMindmapChild', '分支') }, children: [] }
      ]
    };
  }

  function defaultExcalidraw() {
    return {
      type: 'excalidraw',
      version: 2,
      source: 'doc-share',
      elements: [],
      appState: { viewBackgroundColor: '#ffffff' },
      files: {}
    };
  }

  function defaultBody(kind) {
    var data = kind === 'excalidraw' ? defaultExcalidraw() : defaultMindmap();
    return JSON.stringify(data, null, 2);
  }

  function parseJSON(text, kind) {
    var raw = String(text || '').trim();
    if (!raw) return kind === 'excalidraw' ? defaultExcalidraw() : defaultMindmap();
    try {
      return JSON.parse(raw);
    } catch (e) {
      return kind === 'excalidraw' ? defaultExcalidraw() : defaultMindmap();
    }
  }

  function mindmapData(text) {
    var parsed = parseJSON(text, 'mindmap');
    // getData(true) 会带 root；编辑器构造需要根节点本身
    if (parsed && parsed.root) return parsed.root;
    // 扁平树若被写入 previewUrl，需剥离以免污染节点结构
    if (parsed && parsed.previewUrl) {
      var copy = {};
      for (var k in parsed) {
        if (Object.prototype.hasOwnProperty.call(parsed, k) && k !== 'previewUrl') copy[k] = parsed[k];
      }
      return copy;
    }
    return parsed;
  }

  function mindmapConfig(text) {
    var parsed = parseJSON(text, 'mindmap');
    if (parsed && parsed.root) {
      return {
        layout: parsed.layout,
        theme: parsed.theme,
        view: parsed.view
      };
    }
    return null;
  }

  function fenceOpenRe(lang) {
    return new RegExp('^```' + lang + '(?:\\s|$)');
  }

  function parseFenceTitle(openLine, kind) {
    var m = String(openLine || '').match(new RegExp('^```' + kind + '(?:\\s+(.*))?\\s*$'));
    if (!m || !m[1]) return '';
    return String(m[1]).trim();
  }

  function sanitizeTitle(title) {
    return String(title || '').replace(/[\r\n`]/g, ' ').replace(/\s+/g, ' ').trim().slice(0, 80);
  }

  // 仅允许站内上传路径或 http(s)，避免 javascript: 等伪协议进 img src
  function sanitizePreviewUrl(url) {
    var u = String(url || '').trim();
    if (!u) return '';
    if (u.indexOf('/uploads/') === 0) return u;
    if (/^https?:\/\//i.test(u)) return u;
    return '';
  }

  function previewUrlFromBody(bodyText, kind) {
    var parsed = parseJSON(bodyText, kind || 'mindmap');
    return sanitizePreviewUrl(parsed && parsed.previewUrl);
  }

  function dataURLToBlob(dataURL) {
    var m = String(dataURL || '').match(/^data:([^;,]+)?(;base64)?,(.*)$/);
    if (!m) return null;
    var mime = m[1] || 'image/png';
    var raw = m[3] || '';
    try {
      if (m[2]) {
        var bin = atob(raw);
        var arr = new Uint8Array(bin.length);
        for (var i = 0; i < bin.length; i++) arr[i] = bin.charCodeAt(i);
        return new Blob([arr], { type: mime });
      }
      return new Blob([decodeURIComponent(raw)], { type: mime });
    } catch (e) {
      return null;
    }
  }

  function uploadPreviewBlob(blob, replaceUrl) {
    if (!blob) return Promise.resolve('');
    var fd = new FormData();
    fd.append('file', blob, 'embed-preview.png');
    fd.append('purpose', 'embed-preview');
    var old = sanitizePreviewUrl(replaceUrl);
    // 仅请求清理 embed/ 前缀旧图；普通插图 URL 服务端会拒绝删除
    if (old) fd.append('replace_url', old);
    var upload = fetch('/admin/api/upload', {
      method: 'POST',
      headers: { 'X-Requested-With': 'XMLHttpRequest' },
      body: fd
    }).then(function (res) {
      return res.json().then(function (data) {
        if (!res.ok) throw new Error((data && data.error) || 'upload fail');
        return sanitizePreviewUrl(data && data.url);
      }, function () {
        throw new Error('upload parse fail');
      });
    });
    return Promise.race([
      upload,
      new Promise(function (_, reject) {
        setTimeout(function () { reject(new Error('upload timeout')); }, 20000);
      })
    ]);
  }

  function attachPreviewUrl(data, url) {
    if (!data || typeof data !== 'object') return data;
    var next = data;
    var clean = sanitizePreviewUrl(url);
    if (clean) next.previewUrl = clean;
    else delete next.previewUrl;
    return next;
  }

  function exportMindmapBlob(instance) {
    if (!instance) return Promise.resolve(null);
    function fromResult(data) {
      if (!data) return null;
      if (typeof Blob !== 'undefined' && data instanceof Blob) return data;
      if (typeof data === 'string') return dataURLToBlob(data);
      return null;
    }
    try {
      // 导出前适配视口，并加大边距，避免节点贴边被裁切
      if (instance.renderer && typeof instance.renderer.forceLoadNode === 'function') {
        instance.renderer.forceLoadNode();
      }
      if (instance.opt) {
        instance.opt.exportPaddingX = Math.max(Number(instance.opt.exportPaddingX) || 0, 48);
        instance.opt.exportPaddingY = Math.max(Number(instance.opt.exportPaddingY) || 0, 48);
        instance.opt.exportPadding = Math.max(Number(instance.opt.exportPadding) || 0, 48);
      }
      if (instance.resize) instance.resize();
      if (instance.view && instance.view.fit) instance.view.fit();
    } catch (e) {}

    function doPng() {
      if (instance.doExport && typeof instance.doExport.png === 'function') {
        return Promise.resolve(instance.doExport.png()).then(fromResult);
      }
      if (typeof instance.export === 'function') {
        return Promise.resolve(instance.export('png', false)).then(fromResult);
      }
      return Promise.resolve(null);
    }

    // fit/resize 后稍等一帧再导出，保证布局已稳定
    return new Promise(function (resolve) {
      requestAnimationFrame(function () {
        setTimeout(function () {
          doPng().then(resolve).catch(function () { resolve(null); });
        }, 80);
      });
    });
  }

  function ensureExcalidrawExportToBlob() {
    function pick(mod) {
      if (!mod) return null;
      if (typeof mod.exportToBlob === 'function') return mod.exportToBlob;
      if (mod.default && typeof mod.default.exportToBlob === 'function') return mod.default.exportToBlob;
      return null;
    }
    var existing = pick(window.ExcalidrawLib) || pick(window.__dsExcalLib);
    if (existing) return Promise.resolve(existing);

    // 优先 ESM（带依赖图）；失败再退到 UMD React + ExcalidrawLib
    var esm = Promise.resolve()
      .then(function () {
        return import('https://esm.sh/@excalidraw/excalidraw@0.17.6');
      })
      .then(function (mod) {
        var fn = pick(mod);
        if (!fn) throw new Error('esm exportToBlob missing');
        window.__dsExcalLib = mod.default || mod;
        return fn;
      });

    return esm.catch(function () {
      return loadScript(CDN.reactJs)
        .then(function () { return loadScript(CDN.reactDomJs); })
        .then(function () { return loadScript(CDN.excalidrawLibJs); })
        .then(function () {
          var fn = pick(window.ExcalidrawLib);
          if (!fn) throw new Error('umd exportToBlob missing');
          return fn;
        });
    }).catch(function () {
      return null;
    });
  }

  function captureMountCanvasBlob(mount) {
    if (!mount) return Promise.resolve(null);
    // 优先静态层：交互层常含半透明 overlay，且分辨率不一定按 DPR
    var best = mount.querySelector('canvas.excalidraw__canvas.static')
      || mount.querySelector('canvas.Static')
      || null;
    if (!best) {
      var canvases = mount.querySelectorAll('canvas');
      var area = 0;
      for (var i = 0; i < canvases.length; i++) {
        var c = canvases[i];
        var a = (c.width || 0) * (c.height || 0);
        if (a > area) { area = a; best = c; }
      }
    }
    if (!best || (best.width * best.height) < 100) return Promise.resolve(null);
    return new Promise(function (resolve) {
      var done = false;
      function finish(blob) {
        if (done) return;
        done = true;
        resolve(blob || null);
      }
      var timer = setTimeout(function () { finish(null); }, 4000);
      try {
        if (typeof best.toBlob === 'function') {
          best.toBlob(function (blob) {
            clearTimeout(timer);
            finish(blob);
          }, 'image/png');
          return;
        }
        clearTimeout(timer);
        finish(dataURLToBlob(best.toDataURL('image/png')));
      } catch (e) {
        clearTimeout(timer);
        finish(null);
      }
    });
  }

  function exportExcalidrawBlob(api, mount, scene) {
    var elements = ((scene && scene.elements) || []).filter(function (el) {
      return el && !el.isDeleted;
    });
    var bg = (scene && scene.appState && scene.appState.viewBackgroundColor) || '#ffffff';
    var files = (scene && scene.files) || {};
    var scale = Math.max(2, Math.min(4, Math.round(window.devicePixelRatio || 2)));

    return ensureExcalidrawExportToBlob().then(function (exportToBlob) {
      if (!exportToBlob || !elements.length) return null;
      return exportToBlob({
        elements: elements,
        appState: {
          exportBackground: true,
          exportEmbedScene: false,
          exportWithDarkMode: false,
          exportScale: scale,
          viewBackgroundColor: bg
        },
        files: files,
        mimeType: 'image/png',
        exportPadding: 24
      }).catch(function () { return null; });
    }).then(function (blob) {
      if (blob) {
        if (typeof blob === 'string') return dataURLToBlob(blob);
        return blob;
      }
      // 降级：截静态画布（可能含视口空白，清晰度受 DPR 限制）
      if (api && typeof api.scrollToContent === 'function') {
        try { api.scrollToContent(undefined, { fitToContent: true }); } catch (e) {}
      }
      return new Promise(function (r) { setTimeout(r, 150); }).then(function () {
        return captureMountCanvasBlob(mount);
      });
    });
  }

  var activeLightbox = null;

  function openLightbox(url, title) {
    var src = sanitizePreviewUrl(url);
    if (!src) return;
    if (activeLightbox) {
      try { activeLightbox.close(); } catch (e) {}
    }
    var overlay = document.createElement('div');
    overlay.className = 'modal-overlay embed-lightbox-overlay';
    overlay.setAttribute('role', 'dialog');
    overlay.setAttribute('aria-modal', 'true');
    var img = document.createElement('img');
    img.className = 'embed-lightbox-img';
    img.src = src;
    img.alt = title || '';
    overlay.appendChild(img);
    function close() {
      if (activeLightbox && activeLightbox.overlay === overlay) activeLightbox = null;
      document.removeEventListener('keydown', onKey);
      if (overlay.parentNode) overlay.parentNode.removeChild(overlay);
      if (window.UI && UI.syncBodyLock) UI.syncBodyLock();
      else if (!document.querySelector('.modal-overlay:not([hidden])')) {
        document.body.classList.remove('modal-open');
      }
    }
    function onKey(e) {
      if (e.key === 'Escape') close();
    }
    overlay.addEventListener('click', function (e) {
      if (e.target === overlay || e.target === img) close();
    });
    document.addEventListener('keydown', onKey);
    document.body.appendChild(overlay);
    document.body.classList.add('modal-open');
    activeLightbox = { overlay: overlay, close: close };
  }

  // 扫描 Markdown，返回全部 mindmap/excalidraw 围栏
  function findFences(src) {
    var text = String(src || '');
    var lines = text.split('\n');
    var out = [];
    var i = 0;
    var offset = 0;
    while (i < lines.length) {
      var line = lines[i];
      var kind = null;
      if (fenceOpenRe('mindmap').test(line)) kind = 'mindmap';
      else if (fenceOpenRe('excalidraw').test(line)) kind = 'excalidraw';
      if (!kind) {
        offset += line.length + 1;
        i++;
        continue;
      }
      var title = parseFenceTitle(line, kind);
      var startOffset = offset;
      offset += line.length + 1;
      i++;
      var bodyLines = [];
      while (i < lines.length && !/^```\s*$/.test(lines[i])) {
        bodyLines.push(lines[i]);
        offset += lines[i].length + 1;
        i++;
      }
      var endOffset = offset;
      if (i < lines.length) {
        endOffset = offset + lines[i].length;
        offset += lines[i].length + 1;
        i++;
      }
      out.push({
        kind: kind,
        title: title,
        start: startOffset,
        end: endOffset,
        body: bodyLines.join('\n'),
        index: out.length
      });
    }
    return out;
  }

  function replaceFence(src, fence, newBody, newTitle) {
    var body = String(newBody || '').replace(/\s+$/, '');
    var title = sanitizeTitle(newTitle !== undefined ? newTitle : fence.title);
    var open = '```' + fence.kind + (title ? (' ' + title) : '');
    var block = open + '\n' + body + '\n```';
    return String(src || '').slice(0, fence.start) + block + String(src || '').slice(fence.end);
  }

  function removeFence(src, fence) {
    if (!fence) return String(src || '');
    var before = String(src || '').slice(0, fence.start).replace(/[ \t]+$/g, '');
    var after = String(src || '').slice(fence.end).replace(/^[ \t]+/g, '');
    if (before && after) {
      before = before.replace(/\n{3,}$/, '\n\n');
      after = after.replace(/^\n{3,}/, '\n\n');
      if (/\n$/.test(before) && /^\n/.test(after)) {
        after = after.replace(/^\n+/, '\n');
      } else if (!/\n$/.test(before)) {
        before += '\n';
      }
    }
    return (before + after).replace(/^\n+/, '').replace(/\n{3,}/g, '\n\n');
  }

  function deletePreviewUrlAsync(url) {
    var u = sanitizePreviewUrl(url);
    if (!u) return;
    fetch('/admin/api/upload', {
      method: 'DELETE',
      headers: {
        'Content-Type': 'application/json',
        'X-Requested-With': 'XMLHttpRequest'
      },
      body: JSON.stringify({ url: u })
    }).catch(function () {});
  }

  function buildFence(kind, body, title) {
    var t0 = sanitizeTitle(title);
    var open = '```' + kind + (t0 ? (' ' + t0) : '');
    return open + '\n' + String(body || defaultBody(kind)).replace(/\s+$/, '') + '\n```';
  }

  function installMarked() {
    if (markedInstalled || !window.marked) return;
    markedInstalled = true;
    if (marked.setOptions) marked.setOptions({ gfm: true, breaks: false });

    function renderCode(code, info) {
      var raw = String(info || '').trim();
      var parts = raw.split(/\s+/);
      var lang = (parts[0] || '').toLowerCase();
      if (!FENCE_LANGS[lang]) return null;
      var title = sanitizeTitle(raw.slice(parts[0].length));
      var idx = payloads.push({ kind: lang, body: code, title: title }) - 1;
      return '<div class="md-embed" data-embed="' + lang + '" data-idx="' + idx + '" data-title="' + escapeHtml(title) + '"></div>';
    }

    // marked v9+ token API；旧版 (code, lang) 兼容
    try {
      marked.use({
        renderer: {
          code: function (token) {
            var code = typeof token === 'object' ? (token.text || '') : String(token || '');
            var lang = typeof token === 'object'
              ? (token.lang || '')
              : (arguments.length > 1 ? arguments[1] : '');
            var html = renderCode(code, lang);
            if (html) return html;
            return false;
          }
        }
      });
    } catch (e) {
      var renderer = new marked.Renderer();
      var orig = renderer.code.bind(renderer);
      renderer.code = function (code, infostring, escaped) {
        var html = renderCode(code, infostring);
        if (html) return html;
        return orig(code, infostring, escaped);
      };
      marked.setOptions({ renderer: renderer });
    }
  }

  function parseMarkdown(src) {
    installMarked();
    payloads = [];
    if (!window.marked || !window.DOMPurify) return null;
    var html = marked.parse(src || '');
    return DOMPurify.sanitize(html, {
      ADD_ATTR: ['data-embed', 'data-idx', 'data-title']
    });
  }

  function loadCss(href) {
    if (document.querySelector('link[data-embed-css="' + href + '"]')) return;
    var link = document.createElement('link');
    link.rel = 'stylesheet';
    link.href = href;
    link.setAttribute('data-embed-css', href);
    document.head.appendChild(link);
  }

  function loadScript(src) {
    if (loaders[src]) return loaders[src];
    loaders[src] = new Promise(function (resolve, reject) {
      var existed = document.querySelector('script[data-embed-src="' + src + '"]');
      if (existed) {
        if (existed.getAttribute('data-loaded') === '1') { resolve(); return; }
        existed.addEventListener('load', function () { resolve(); });
        existed.addEventListener('error', function () {
          delete loaders[src];
          reject(new Error('load fail'));
        });
        return;
      }
      var s = document.createElement('script');
      s.src = src;
      s.async = true;
      s.setAttribute('data-embed-src', src);
      s.onload = function () { s.setAttribute('data-loaded', '1'); resolve(); };
      s.onerror = function () {
        if (s.parentNode) s.parentNode.removeChild(s);
        delete loaders[src];
        reject(new Error('load fail'));
      };
      document.head.appendChild(s);
    });
    return loaders[src];
  }

  function ensureMindmap() {
    loadCss(CDN.mindmapCss);
    function pick() {
      var M = window.simpleMindMap;
      if (!M) return null;
      return M.default || M;
    }
    function withThemes(MindMap) {
      // UMD 全量包通常已带 Export；若未注册则尽量挂上，供保存时导出 PNG
      try {
        var ExportPlug = MindMap.Export
          || (window.simpleMindMap && (window.simpleMindMap.Export || (window.simpleMindMap.default && window.simpleMindMap.default.Export)));
        if (ExportPlug && typeof MindMap.usePlugin === 'function' && !MindMap.__dsExportReady) {
          MindMap.usePlugin(ExportPlug);
          MindMap.__dsExportReady = true;
        }
      } catch (e) {}
      if (MindMap.__dsThemesReady) return Promise.resolve(MindMap);
      return loadScript(CDN.mindmapThemesJs).then(function () {
        var Themes = window.simpleMindMapPluginThemes;
        if (Themes && Themes.default) Themes = Themes.default;
        if (Themes && typeof Themes.init === 'function') {
          Themes.init(MindMap);
          MindMap.__dsThemes = Themes;
        }
        MindMap.__dsThemesReady = true;
        return MindMap;
      }).catch(function () {
        // 主题插件失败时仍可用默认主题
        MindMap.__dsThemesReady = true;
        return MindMap;
      });
    }
    var ready = pick();
    if (ready) return withThemes(ready);
    return loadScript(CDN.mindmapJs).then(function () {
      var M = pick();
      if (!M) throw new Error('simpleMindMap missing');
      return withThemes(M);
    });
  }

  var MINDMAP_LAYOUTS = [
    { value: 'logicalStructure', labelKey: 'edit.layoutLogical', fallback: '逻辑结构' },
    { value: 'logicalStructureLeft', labelKey: 'edit.layoutLogicalLeft', fallback: '逻辑结构（左）' },
    { value: 'mindMap', labelKey: 'edit.layoutMindMap', fallback: '思维导图' },
    { value: 'catalogOrganization', labelKey: 'edit.layoutCatalog', fallback: '目录组织' },
    { value: 'organizationStructure', labelKey: 'edit.layoutOrg', fallback: '组织结构' },
    { value: 'timeline', labelKey: 'edit.layoutTimeline', fallback: '时间轴' },
    { value: 'timeline2', labelKey: 'edit.layoutTimeline2', fallback: '交替时间轴' },
    { value: 'fishbone', labelKey: 'edit.layoutFishbone', fallback: '鱼骨图' },
    { value: 'verticalTimeline', labelKey: 'edit.layoutVTimeline', fallback: '竖向时间轴' }
  ];

  function mindmapThemeOptions(MindMap) {
    var list = [{ name: t('edit.themeDefault', '默认'), value: 'default', dark: false }];
    var Themes = MindMap && MindMap.__dsThemes;
    if (Themes) {
      (Themes.lightList || []).forEach(function (item) {
        list.push({ name: item.name, value: item.value, dark: false });
      });
      (Themes.darkList || []).forEach(function (item) {
        list.push({ name: item.name, value: item.value, dark: true });
      });
    }
    return list;
  }

  function buildMindmapToolbar(ui, MindMap, opts) {
    opts = opts || {};
    var bar = document.createElement('div');
    bar.className = 'embed-toolbar';

    function field(label, select) {
      var wrap = document.createElement('label');
      wrap.className = 'embed-toolbar-field';
      var span = document.createElement('span');
      span.textContent = label;
      wrap.appendChild(span);
      wrap.appendChild(select);
      return wrap;
    }

    var themeSel = document.createElement('select');
    themeSel.className = 'embed-toolbar-select';
    mindmapThemeOptions(MindMap).forEach(function (item) {
      var opt = document.createElement('option');
      opt.value = item.value;
      opt.textContent = item.name + (item.dark ? ' · dark' : '');
      themeSel.appendChild(opt);
    });
    themeSel.value = opts.theme || 'classicBlue';
    if (![].some.call(themeSel.options, function (o) { return o.value === themeSel.value; })) {
      themeSel.value = 'default';
    }

    var layoutSel = document.createElement('select');
    layoutSel.className = 'embed-toolbar-select';
    MINDMAP_LAYOUTS.forEach(function (item) {
      var opt = document.createElement('option');
      opt.value = item.value;
      opt.textContent = t(item.labelKey, item.fallback);
      layoutSel.appendChild(opt);
    });
    layoutSel.value = opts.layout || 'mindMap';
    if (![].some.call(layoutSel.options, function (o) { return o.value === layoutSel.value; })) {
      layoutSel.value = 'logicalStructure';
    }

    themeSel.disabled = !!opts.readonly;
    layoutSel.disabled = !!opts.readonly;
    bar.appendChild(field(t('edit.embedTheme', '主题'), themeSel));
    bar.appendChild(field(t('edit.embedLayout', '结构'), layoutSel));

    // 插在提示行与画布之间
    if (ui.mount && ui.mount.parentNode) {
      ui.mount.parentNode.insertBefore(bar, ui.mount);
    }

    return {
      themeSel: themeSel,
      layoutSel: layoutSel,
      getTheme: function () { return themeSel.value; },
      getLayout: function () { return layoutSel.value; },
      bind: function (instance) {
        if (!instance) return;
        themeSel.addEventListener('change', function () {
          try {
            instance.setTheme(themeSel.value);
            if (instance.view && instance.view.fit) instance.view.fit();
          } catch (e) {}
        });
        layoutSel.addEventListener('change', function () {
          try {
            instance.setLayout(layoutSel.value);
            if (instance.view && instance.view.fit) instance.view.fit();
          } catch (e) {}
        });
      }
    };
  }

  function ensureExcalidraw() {
    loadCss(CDN.excalidrawCss);
    if (window.ExcalidrawEmbed && ExcalidrawEmbed.renderExcalidraw) {
      return Promise.resolve(window.ExcalidrawEmbed);
    }
    return loadScript(CDN.excalidrawJs).then(function () {
      if (!window.ExcalidrawEmbed) throw new Error('ExcalidrawEmbed missing');
      return window.ExcalidrawEmbed;
    });
  }

  function labelFor(kind) {
    return kind === 'excalidraw'
      ? t('edit.embedBoard', '画板')
      : t('edit.embedMindmap', '思维导图');
  }

  function fillPlaceholder(el, kind, editable, customTitle, previewUrl) {
    var title = sanitizeTitle(customTitle) || labelFor(kind);
    var typeHint = labelFor(kind);
    var imgUrl = sanitizePreviewUrl(previewUrl);
    var delBtn = editable
      ? ('<button type="button" class="md-embed-del" title="' +
          escapeHtml(t('common.delete', '删除')) +
          '" aria-label="' + escapeHtml(t('common.delete', '删除')) +
          '">×</button>')
      : '';
    el.className = 'md-embed md-embed-' + kind + (editable ? ' is-editable' : '') + (imgUrl ? ' has-preview' : '');
    if (imgUrl) {
      var hint = editable
        ? t('edit.embedClickEdit', '点击编辑')
        : t('edit.embedClickZoom', '点击放大');
      el.innerHTML =
        '<div class="md-embed-preview">' +
          delBtn +
          '<img src="' + escapeHtml(imgUrl) + '" alt="' + escapeHtml(title) + '" loading="lazy">' +
          '<div class="md-embed-preview-caption">' +
            '<strong>' + escapeHtml(title) + '</strong>' +
            '<span>' + escapeHtml(
              (sanitizeTitle(customTitle) ? typeHint + ' · ' : '') + hint
            ) + '</span>' +
          '</div>' +
        '</div>';
      return;
    }
    el.innerHTML =
      '<div class="md-embed-card">' +
        '<div class="md-embed-icon" aria-hidden="true">' + (kind === 'excalidraw' ? '◇' : '◎') + '</div>' +
        '<div class="md-embed-meta">' +
          '<strong>' + escapeHtml(title) + '</strong>' +
          '<span>' + escapeHtml(
            (sanitizeTitle(customTitle) ? typeHint + ' · ' : '') +
            (editable ? t('edit.embedClickEdit', '点击编辑') : t('edit.embedClickView', '点击查看'))
          ) + '</span>' +
        '</div>' +
        delBtn +
      '</div>';
  }

  function closeOverlay(overlay) {
    if (!overlay || !overlay.parentNode) return;
    overlay.parentNode.removeChild(overlay);
    if (window.UI && UI.syncBodyLock) UI.syncBodyLock();
    else if (!document.querySelector('.modal-overlay:not([hidden])')) {
      document.body.classList.remove('modal-open');
    }
  }

  function openModalShell(title, opts) {
    opts = opts || {};
    var overlay = document.createElement('div');
    overlay.className = 'modal-overlay embed-modal-overlay';
    var modal = document.createElement('div');
    modal.className = 'modal embed-modal';
    modal.setAttribute('role', 'dialog');
    modal.setAttribute('aria-modal', 'true');

    var head = document.createElement('div');
    head.className = 'modal-head';
    var titleInput = null;
    var h2 = null;
    var typeLabel = title || '';
    var initialName = sanitizeTitle(opts.embedTitle);

    if (opts.allowTitle !== false && !opts.readonly) {
      // 可编辑：标题区直接改名，不再单独放「名称」输入框
      titleInput = document.createElement('input');
      titleInput.type = 'text';
      titleInput.className = 'embed-head-title';
      titleInput.maxLength = 80;
      titleInput.placeholder = typeLabel || t('edit.embedTitlePlaceholder', '给这块图起个名字');
      titleInput.value = initialName;
      titleInput.setAttribute('aria-label', t('edit.embedTitle', '名称'));
      titleInput.title = t('edit.embedTitleHint', '点击修改名称');
      titleInput.addEventListener('keydown', function (e) {
        // 避免 Enter 冒泡到导图快捷键；Esc 失焦即可
        if (e.key === 'Enter') {
          e.preventDefault();
          titleInput.blur();
        }
      });
      head.appendChild(titleInput);
    } else {
      h2 = document.createElement('h2');
      h2.textContent = initialName || typeLabel;
      head.appendChild(h2);
    }

    var closeBtn = document.createElement('button');
    closeBtn.type = 'button';
    closeBtn.className = 'modal-close';
    closeBtn.setAttribute('aria-label', t('关闭', '关闭'));
    closeBtn.textContent = '✕';
    head.appendChild(closeBtn);

    var body = document.createElement('div');
    body.className = 'modal-body embed-modal-body';
    var mount = document.createElement('div');
    mount.className = 'embed-mount' + (opts.mountClass ? (' ' + opts.mountClass) : '');
    var status = document.createElement('div');
    status.className = 'embed-status muted';
    status.textContent = t('edit.embedLoading', '加载编辑器…');
    body.appendChild(status);
    if (opts.hint) {
      var hint = document.createElement('div');
      hint.className = 'embed-hint';
      hint.textContent = opts.hint;
      body.appendChild(hint);
    }
    body.appendChild(mount);

    var foot = document.createElement('div');
    foot.className = 'modal-foot';
    var cancelBtn = document.createElement('button');
    cancelBtn.type = 'button';
    cancelBtn.className = 'btn';
    cancelBtn.textContent = t('取消', '取消');
    var saveBtn = document.createElement('button');
    saveBtn.type = 'button';
    saveBtn.className = 'btn btn-primary';
    saveBtn.textContent = t('保存', '保存');
    foot.appendChild(cancelBtn);
    foot.appendChild(saveBtn);

    modal.appendChild(head);
    modal.appendChild(body);
    modal.appendChild(foot);
    overlay.appendChild(modal);
    document.body.appendChild(overlay);
    if (window.UI && UI.syncBodyLock) UI.syncBodyLock();
    else document.body.classList.add('modal-open');

    return {
      overlay: overlay,
      mount: mount,
      status: status,
      saveBtn: saveBtn,
      cancelBtn: cancelBtn,
      closeBtn: closeBtn,
      titleInput: titleInput,
      getTitle: function () {
        if (titleInput) return sanitizeTitle(titleInput.value);
        return sanitizeTitle(opts.embedTitle);
      },
      setReadonly: function (ro) {
        saveBtn.hidden = !!ro;
        cancelBtn.textContent = ro ? t('关闭', '关闭') : t('取消', '取消');
        if (titleInput) titleInput.disabled = !!ro;
      }
    };
  }

  function openMindmapEditor(bodyText, opts) {
    opts = opts || {};
    var readonly = !!opts.readonly;
    var ui = openModalShell(labelFor('mindmap'), {
      mountClass: 'embed-mount-mindmap',
      embedTitle: opts.title || '',
      readonly: readonly,
      hint: readonly
        ? t('edit.embedMindmapViewHint', '只读预览：可拖动画布、滚轮缩放')
        : t('edit.embedMindmapHint', '双击节点编辑文字；Tab 添加子节点；Enter 添加同级节点；Delete 删除')
    });
    ui.setReadonly(readonly);
    var instance = null;
    var closed = false;
    var saving = false;
    var prevPreviewUrl = previewUrlFromBody(bodyText, 'mindmap');

    function destroy() {
      if (saving) return; // 导出/上传进行中禁止关闭，避免半截写回或挂起
      closed = true;
      try { if (instance && instance.destroy) instance.destroy(); } catch (e) {}
      closeOverlay(ui.overlay);
    }

    ui.closeBtn.addEventListener('click', destroy);
    ui.cancelBtn.addEventListener('click', destroy);
    ui.overlay.addEventListener('click', function (e) {
      if (e.target === ui.overlay) destroy();
    });

    ui.saveBtn.addEventListener('click', function () {
      if (!instance || readonly || saving) return;
      var data = instance.getData(true);
      saving = true;
      ui.saveBtn.disabled = true;
      ui.cancelBtn.disabled = true;
      ui.closeBtn.disabled = true;
      ui.status.hidden = false;
      ui.status.textContent = t('edit.embedPreviewSaving', '正在生成预览图…');

      var exportP = exportMindmapBlob(instance);
      // 导图导出也可能卡住，整体超时后降级为无预览图保存
      var timed = Promise.race([
        exportP,
        new Promise(function (resolve) { setTimeout(function () { resolve(null); }, 15000); })
      ]);

      timed.then(function (blob) {
        if (!blob) return '';
        return uploadPreviewBlob(blob, prevPreviewUrl);
      }).then(function (url) {
        attachPreviewUrl(data, url || prevPreviewUrl || '');
        if (!url && window.UI && UI.toast) {
          UI.toast(t('edit.embedPreviewFail', '预览图生成失败，内容已保存'), 'info');
        }
        var json = JSON.stringify(data, null, 2);
        saving = false;
        if (typeof opts.onSave === 'function') opts.onSave(json, ui.getTitle());
        destroy();
      }).catch(function () {
        attachPreviewUrl(data, prevPreviewUrl || '');
        if (window.UI && UI.toast) {
          UI.toast(t('edit.embedPreviewFail', '预览图生成失败，内容已保存'), 'info');
        }
        var json = JSON.stringify(data, null, 2);
        saving = false;
        if (typeof opts.onSave === 'function') opts.onSave(json, ui.getTitle());
        destroy();
      });
    });

    ensureMindmap().then(function (MindMap) {
      if (closed) return;
      ui.status.hidden = true;
      var cfg = mindmapConfig(bodyText) || {};
      // 新图默认用更耐看的主题/放射结构；已有数据保留原 theme/layout
      var theme = cfg.theme || 'classicBlue';
      var layout = cfg.layout || 'mindMap';
      var toolbar = buildMindmapToolbar(ui, MindMap, {
        theme: theme,
        layout: layout,
        readonly: readonly
      });
      theme = toolbar.getTheme();
      layout = toolbar.getLayout();

      requestAnimationFrame(function () {
        if (closed) return;
        var rect = ui.mount.getBoundingClientRect();
        if (rect.width < 40 || rect.height < 40) {
          ui.mount.style.height = Math.max(480, Math.floor(window.innerHeight * 0.65)) + 'px';
        }
        try {
          instance = new MindMap({
            el: ui.mount,
            data: mindmapData(bodyText),
            readonly: readonly,
            fit: true,
            theme: theme,
            layout: layout,
            viewData: cfg.view,
            customInnerElsAppendTo: ui.mount
          });
          toolbar.bind(instance);
        } catch (err) {
          ui.status.hidden = false;
          ui.status.textContent = t('edit.embedLoadFail', '编辑器加载失败');
          ui.saveBtn.disabled = true;
          return;
        }
        setTimeout(function () {
          if (!instance || closed) return;
          try {
            if (instance.resize) instance.resize();
            if (instance.view && instance.view.fit) instance.view.fit();
          } catch (e) {}
        }, 50);
      });
    }).catch(function () {
      ui.status.textContent = t('edit.embedLoadFail', '编辑器加载失败');
      ui.saveBtn.disabled = true;
    });
  }

  function openExcalidrawEditor(bodyText, opts) {
    opts = opts || {};
    var readonly = !!opts.readonly;
    var ui = openModalShell(labelFor('excalidraw'), {
      mountClass: 'embed-mount-excalidraw',
      embedTitle: opts.title || '',
      readonly: readonly
    });
    ui.setReadonly(readonly);
    var latest = parseJSON(bodyText, 'excalidraw');
    var prevPreviewUrl = sanitizePreviewUrl(latest && latest.previewUrl);
    var apiRef = null;
    var rootRef = null;
    var closed = false;
    var saving = false;

    function destroyHost() {
      try {
        if (rootRef && typeof rootRef.unmount === 'function') rootRef.unmount();
        else if (apiRef && typeof apiRef.unmount === 'function') apiRef.unmount();
      } catch (e) {}
      try {
        if (ui.mount) ui.mount.innerHTML = '';
      } catch (e2) {}
      rootRef = null;
      apiRef = null;
    }

    function destroy() {
      if (saving) return;
      closed = true;
      destroyHost();
      closeOverlay(ui.overlay);
    }

    ui.closeBtn.addEventListener('click', destroy);
    ui.cancelBtn.addEventListener('click', destroy);
    ui.overlay.addEventListener('click', function (e) {
      if (e.target === ui.overlay) destroy();
    });

    ui.saveBtn.addEventListener('click', function () {
      if (readonly || saving) return;
      var scene = latest;
      if (apiRef && typeof apiRef.getSceneElements === 'function') {
        scene = {
          type: 'excalidraw',
          version: 2,
          source: 'doc-share',
          elements: apiRef.getSceneElements(),
          appState: apiRef.getAppState ? apiRef.getAppState() : (latest.appState || {}),
          files: apiRef.getFiles ? apiRef.getFiles() : (latest.files || {})
        };
      }
      // 精简 appState，避免把 UI 瞬态状态写进文档
      if (scene && scene.appState) {
        scene.appState = {
          viewBackgroundColor: scene.appState.viewBackgroundColor || '#ffffff'
        };
      }
      var prevUrl = prevPreviewUrl;
      saving = true;
      ui.saveBtn.disabled = true;
      ui.cancelBtn.disabled = true;
      ui.closeBtn.disabled = true;
      ui.status.hidden = false;
      ui.status.textContent = t('edit.embedPreviewSaving', '正在生成预览图…');

      var exportP = exportExcalidrawBlob(apiRef, ui.mount, scene);
      var timed = Promise.race([
        exportP,
        // 首次可能需拉取导出库，超时放宽
        new Promise(function (resolve) { setTimeout(function () { resolve(null); }, 45000); })
      ]);

      timed.then(function (blob) {
        if (!blob) return '';
        return uploadPreviewBlob(blob, prevPreviewUrl);
      }).then(function (url) {
        attachPreviewUrl(scene, url || prevUrl || '');
        if (!url && window.UI && UI.toast) {
          UI.toast(t('edit.embedPreviewFail', '预览图生成失败，内容已保存'), 'info');
        }
        var json = JSON.stringify(scene, null, 2);
        saving = false;
        if (typeof opts.onSave === 'function') opts.onSave(json, ui.getTitle());
        destroy();
      }).catch(function () {
        attachPreviewUrl(scene, prevUrl || '');
        if (window.UI && UI.toast) {
          UI.toast(t('edit.embedPreviewFail', '预览图生成失败，内容已保存'), 'info');
        }
        var json = JSON.stringify(scene, null, 2);
        saving = false;
        if (typeof opts.onSave === 'function') opts.onSave(json, ui.getTitle());
        destroy();
      });
    });

    ensureExcalidraw().then(function (Embed) {
      if (closed) return;
      ui.status.hidden = true;
      var initial = parseJSON(bodyText, 'excalidraw');
      latest = initial;
      var props = {
        initialData: {
          elements: initial.elements || [],
          appState: Object.assign({ viewBackgroundColor: '#ffffff' }, initial.appState || {}),
          files: initial.files || {}
        },
        viewModeEnabled: readonly,
        zenModeEnabled: false,
        gridModeEnabled: false,
        excalidrawAPI: function (api) {
          apiRef = api;
        },
        onChange: function (elements, appState, files) {
          latest = {
            type: 'excalidraw',
            version: 2,
            source: 'doc-share',
            elements: elements,
            appState: appState,
            files: files || {}
          };
        }
      };
      var ret = Embed.renderExcalidraw(ui.mount, props);
      Promise.resolve(ret).then(function (out) {
        if (!out || closed) return;
        // UMD 可能返回 React Root、ImperativeAPI，或 { root, api }
        if (out.root && typeof out.root.unmount === 'function') rootRef = out.root;
        if (out.api && typeof out.api.getSceneElements === 'function') apiRef = out.api;
        if (typeof out.unmount === 'function' && typeof out.getSceneElements !== 'function') {
          rootRef = out;
        } else if (typeof out.getSceneElements === 'function') {
          apiRef = out;
        } else if (typeof out.unmount === 'function') {
          rootRef = out;
        }
      });
    }).catch(function () {
      ui.status.textContent = t('edit.embedLoadFail', '编辑器加载失败');
      ui.saveBtn.disabled = true;
    });
  }

  function openEditor(kind, bodyText, opts) {
    if (kind === 'excalidraw') openExcalidrawEditor(bodyText, opts);
    else openMindmapEditor(bodyText, opts);
  }

  function hydrate(root, opts) {
    if (!root) return;
    opts = opts || {};
    var editable = !!opts.editable;
    var showPreview = !!opts.showPreview;
    var nodes = root.querySelectorAll('.md-embed[data-embed]');
    nodes.forEach(function (el) {
      if (el.dataset.embedBound) return;
      el.dataset.embedBound = '1';
      var kind = el.getAttribute('data-embed');
      var idx = parseInt(el.getAttribute('data-idx'), 10);
      var cardTitle = '';
      var body0 = '';
      if (!isNaN(idx) && payloads[idx]) {
        if (payloads[idx].title) cardTitle = payloads[idx].title;
        body0 = payloads[idx].body || '';
      } else {
        cardTitle = el.getAttribute('data-title') || '';
      }
      var previewUrl = showPreview ? previewUrlFromBody(body0, kind) : '';
      fillPlaceholder(el, kind, editable, cardTitle, previewUrl);

      function locateFence() {
        var src = typeof opts.getSource === 'function' ? opts.getSource() : '';
        var fences = findFences(src).filter(function (f) { return f.kind === kind; });
        var ord = 0;
        var siblings = root.querySelectorAll('.md-embed[data-embed="' + kind + '"]');
        for (var i = 0; i < siblings.length; i++) {
          if (siblings[i] === el) { ord = i; break; }
        }
        return { src: src, fence: fences[ord] || null, ord: ord };
      }

      el.addEventListener('click', function (e) {
        var delBtn = e.target && e.target.closest ? e.target.closest('.md-embed-del') : null;
        if (delBtn) {
          e.preventDefault();
          e.stopPropagation();
          if (!editable) return;
          if (typeof opts.getSource !== 'function' || typeof opts.setSource !== 'function') return;

          var name = sanitizeTitle(cardTitle) || labelFor(kind);
          var msg = (window.UI && UI.t)
            ? UI.t('确认删除「{0}」？', name)
            : t('edit.embedConfirmDel', '确认删除「{0}」？').split('{0}').join(name);

          var ask = (window.UI && UI.confirm)
            ? UI.confirm(msg, { danger: true })
            : Promise.resolve(window.confirm(msg));

          Promise.resolve(ask).then(function (ok) {
            if (!ok) return;
            var located = locateFence();
            if (!located.fence) return; // 源码已变/找不到围栏时不删图，避免误删
            var img = previewUrlFromBody(located.fence.body, kind);
            opts.setSource(removeFence(located.src, located.fence));
            if (img) deletePreviewUrlAsync(img);
            if (typeof opts.onSaved === 'function') opts.onSaved();
          });
          return;
        }

        var body = body0;
        var title = cardTitle;
        if (!isNaN(idx) && payloads[idx]) {
          body = payloads[idx].body;
          if (payloads[idx].title) title = payloads[idx].title;
        } else body = defaultBody(kind);

        var located = locateFence();
        if (located.fence) {
          body = located.fence.body;
          title = located.fence.title || title;
        }
        var ord = located.ord;

        var imgUrl = showPreview ? previewUrlFromBody(body, kind) : '';
        // 只读且有预览图：灯箱放大，不打开交互编辑器
        if (!editable && imgUrl) {
          openLightbox(imgUrl, title || labelFor(kind));
          return;
        }

        openEditor(kind, body, {
          readonly: !editable,
          title: title,
          onSave: function (json, newTitle) {
            if (!editable) return;
            if (typeof opts.getSource !== 'function' || typeof opts.setSource !== 'function') return;
            var fresh = findFences(opts.getSource()).filter(function (f) { return f.kind === kind; });
            var target = fresh[ord];
            if (target) {
              opts.setSource(replaceFence(opts.getSource(), target, json, newTitle));
            } else {
              var cur = opts.getSource() || '';
              var block = buildFence(kind, json, newTitle);
              opts.setSource(cur ? (cur.replace(/\s*$/, '') + '\n\n' + block + '\n') : (block + '\n'));
            }
            if (typeof opts.onSaved === 'function') opts.onSaved();
          }
        });
      });
    });
  }

  function insertAtCursor(textarea, text) {
    var s = textarea.selectionStart;
    var e = textarea.selectionEnd;
    var v = textarea.value;
    var before = v.slice(0, s);
    var after = v.slice(e);
    var padBefore = before && !/\n$/.test(before) ? '\n\n' : (before && !/\n\n$/.test(before) ? '\n' : '');
    var padAfter = after && !/^\n/.test(after) ? '\n\n' : '';
    var block = padBefore + text + padAfter;
    textarea.value = before + block + after;
    var pos = (before + padBefore + text).length;
    textarea.selectionStart = textarea.selectionEnd = pos;
    textarea.focus();
  }

  function insertEmbed(textarea, kind, openAfter) {
    if (!textarea) return;
    var body = defaultBody(kind);
    var ask = (window.UI && UI.prompt)
      ? UI.prompt(t('edit.embedTitlePrompt', '给这块图起个名字（可留空）'), 'text', t('edit.embedTitlePlaceholder', '给这块图起个名字'), '')
      : Promise.resolve('');
    Promise.resolve(ask).then(function (name) {
      if (name === null) return; // 用户取消
      var title = sanitizeTitle(name);
      var fenceMd = buildFence(kind, body, title);
      var insertAt = textarea.selectionStart;
      insertAtCursor(textarea, fenceMd);

      var kindFences = findFences(textarea.value).filter(function (f) { return f.kind === kind; });
      var fenceOrd = kindFences.length - 1;
      for (var i = 0; i < kindFences.length; i++) {
        if (kindFences[i].start >= insertAt - 2) {
          fenceOrd = i;
          break;
        }
      }

      if (openAfter === false) {
        if (typeof openAfter === 'function') openAfter();
        return;
      }
      openEditor(kind, body, {
        readonly: false,
        title: title,
        onSave: function (json, newTitle) {
          var fresh = findFences(textarea.value).filter(function (f) { return f.kind === kind; });
          var fenceInfo = fresh[fenceOrd];
          if (fenceInfo) {
            textarea.value = replaceFence(textarea.value, fenceInfo, json, newTitle);
          }
          if (typeof openAfter === 'function') openAfter();
        }
      });
    });
  }

  window.Embeds = {
    findFences: findFences,
    replaceFence: replaceFence,
    removeFence: removeFence,
    buildFence: buildFence,
    defaultBody: defaultBody,
    parseMarkdown: parseMarkdown,
    installMarked: installMarked,
    hydrate: hydrate,
    openEditor: openEditor,
    insertEmbed: insertEmbed,
    labelFor: labelFor
  };
})();
