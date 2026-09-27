// 结构化画布文档（独立类型）：思维导图（mindmap）/ 画板（board）。
// 数据 schema 与 Markdown 围栏（embeds.js）同源：
//   mindmap: {root, layout, theme, view}（getData(true) 包装）
//   board:   标准 Excalidraw scene {type, version, source, elements, appState, files}
// 用法：JsonDoc.init({ kind, mode: 'edit'|'view', content, mount }) → { getJSON, destroy }
window.JsonDoc = (function () {
  'use strict';

  var CDN = {
    mindmapJs: 'https://cdn.jsdelivr.net/npm/simple-mind-map@0.14.0/dist/simpleMindMap.umd.min.js',
    mindmapCss: 'https://cdn.jsdelivr.net/npm/simple-mind-map@0.14.0/dist/simpleMindMap.esm.css',
    mindmapThemesJs: 'https://cdn.jsdelivr.net/npm/simple-mind-map-plugin-themes@1.0.1/dist/themes.iife.min.js',
    excalidrawJs: 'https://cdn.jsdelivr.net/npm/excalidraw-embed@0.18.4/dist/excalidraw-embed.umd.js',
    excalidrawCss: 'https://cdn.jsdelivr.net/npm/excalidraw-embed@0.18.4/dist/excalidraw-embed.css'
  };

  var cssLoaded = {};
  var scriptLoaders = {};

  function loadCss(href) {
    if (cssLoaded[href]) return;
    cssLoaded[href] = true;
    var l = document.createElement('link');
    l.rel = 'stylesheet';
    l.href = href;
    document.head.appendChild(l);
  }

  function loadScript(src) {
    if (!scriptLoaders[src]) {
      scriptLoaders[src] = new Promise(function (resolve, reject) {
        var s = document.createElement('script');
        s.src = src;
        s.onload = function () { resolve(); };
        s.onerror = function () {
          if (s.parentNode) s.parentNode.removeChild(s);
          delete scriptLoaders[src];
          reject(new Error('load fail: ' + src));
        };
        document.head.appendChild(s);
      });
    }
    return scriptLoaders[src];
  }

  function defaultMindmap() {
    return {
      data: { text: '中心主题' },
      children: [{ data: { text: '分支' }, children: [] }]
    };
  }

  function defaultExcalidraw() {
    return {
      type: 'excalidraw', version: 2, source: 'doc-share',
      elements: [], appState: { viewBackgroundColor: '#ffffff' }, files: {}
    };
  }

  function parseJSON(text, kind) {
    var raw = String(text || '').trim();
    if (!raw) return kind === 'board' ? defaultExcalidraw() : defaultMindmap();
    try { return JSON.parse(raw); } catch (e) {
      return kind === 'board' ? defaultExcalidraw() : defaultMindmap();
    }
  }

  // 围栏存储可能是 {root,...} 包装或扁平树（可能带 previewUrl），编辑器构造需要根节点本身
  function mindmapRoot(parsed) {
    if (parsed && parsed.root) return parsed.root;
    if (parsed && parsed.previewUrl) {
      var copy = {};
      for (var k in parsed) {
        if (Object.prototype.hasOwnProperty.call(parsed, k) && k !== 'previewUrl') copy[k] = parsed[k];
      }
      return copy;
    }
    return parsed;
  }

  function ensureMindmap() {
    loadCss(CDN.mindmapCss);
    return loadScript(CDN.mindmapJs).then(function () {
      var M = window.simpleMindMap;
      if (!M) throw new Error('simpleMindMap missing');
      var MindMap = M.default || M;
      if (MindMap.__jdThemesReady) return MindMap;
      return loadScript(CDN.mindmapThemesJs).then(function () {
        var Themes = window.simpleMindMapPluginThemes;
        if (Themes && Themes.default) Themes = Themes.default;
        if (Themes && typeof Themes.init === 'function') {
          Themes.init(MindMap);
          MindMap.__dsThemes = Themes;
        }
        MindMap.__jdThemesReady = true;
        return MindMap;
      });
    });
  }

  function ensureExcalidraw() {
    loadCss(CDN.excalidrawCss);
    if (window.ExcalidrawEmbed && window.ExcalidrawEmbed.renderExcalidraw) {
      return Promise.resolve(window.ExcalidrawEmbed);
    }
    return loadScript(CDN.excalidrawJs).then(function () {
      if (!window.ExcalidrawEmbed) throw new Error('ExcalidrawEmbed missing');
      return window.ExcalidrawEmbed;
    });
  }

  function init(opts) {
    opts = opts || {};
    var kind = opts.kind === 'board' ? 'board' : 'mindmap';
    var readonly = opts.mode === 'view';
    var mount = opts.mount;
    var destroyed = false;
    var apiRef = null, rootRef = null, instance = null;
    var latest = parseJSON(opts.content, kind);

    function getJSON() {
      if (destroyed) return null;
      if (kind === 'mindmap') {
        if (!instance) return null;
        var full = instance.getData(true); // {root, layout, theme, view}
        return JSON.stringify(full);
      }
      // apiRef 未就绪时退回 onChange 捕获的场景，避免保存丢失改动
      if (!apiRef && !latest) return null;
      var scene = {
        type: 'excalidraw', version: 2, source: 'doc-share',
        elements: (apiRef && apiRef.getSceneElements) ? apiRef.getSceneElements() : ((latest && latest.elements) || []),
        appState: (apiRef && apiRef.getAppState) ? apiRef.getAppState() : ((latest && latest.appState) || {}),
        files: (apiRef && apiRef.getFiles) ? apiRef.getFiles() : ((latest && latest.files) || {})
      };
      // 精简 appState，避免把 UI 瞬态状态写进文档
      if (scene.appState) {
        scene.appState = { viewBackgroundColor: scene.appState.viewBackgroundColor || '#ffffff' };
      }
      return JSON.stringify(scene);
    }

    function destroy() {
      destroyed = true;
      try { if (instance && instance.destroy) instance.destroy(); } catch (e) {}
      try { if (rootRef && rootRef.unmount) rootRef.unmount(); } catch (e) {}
    }

    if (kind === 'mindmap') {
      ensureMindmap().then(function (MindMap) {
        if (destroyed) return;
        var parsed = parseJSON(opts.content, 'mindmap');
        var cfg = (parsed && parsed.root) ? parsed : {};
        // 工具栏先于实例构建：回读下拉实际值（缺项补齐后的），保证与实例一致
        var toolbar = window.MindmapToolbar ? window.MindmapToolbar.build({
          MindMap: MindMap,
          mount: mount,
          standalone: true,
          readonly: readonly,
          theme: cfg.theme,
          layout: cfg.layout
        }) : null;
        instance = new MindMap({
          el: mount,
          data: mindmapRoot(parsed),
          readonly: readonly,
          fit: true,
          theme: toolbar ? toolbar.getTheme() : (cfg.theme || 'classicBlue'),
          layout: toolbar ? toolbar.getLayout() : (cfg.layout || 'mindMap'),
          viewData: cfg.view,
          customInnerElsAppendTo: mount
        });
        if (toolbar) toolbar.bind(instance);
        if (typeof opts.onReady === 'function') opts.onReady();
      }).catch(function () {
        if (!destroyed && typeof opts.onError === 'function') opts.onError('编辑器加载失败，请检查网络');
      });
    } else {
      ensureExcalidraw().then(function (Embed) {
        if (destroyed) return;
        var initial = parseJSON(opts.content, 'board');
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
          // renderExcalidraw 只返回 React Root，imperative API 必须经此回调获取（与 embeds.js 弹窗一致）
          excalidrawAPI: function (api) {
            if (api && typeof api.getSceneElements === 'function') apiRef = api;
          },
          onChange: function (elements, appState, files) {
            latest = {
              type: 'excalidraw', version: 2, source: 'doc-share',
              elements: elements, appState: appState, files: files || {}
            };
          }
        };
        var ret = Embed.renderExcalidraw(mount, props);
        Promise.resolve(ret).then(function (out) {
          if (!out || destroyed) return;
          if (out.root && typeof out.root.unmount === 'function') rootRef = out.root;
          if (out.api && typeof out.api.getSceneElements === 'function') apiRef = out.api;
          if (typeof out.unmount === 'function' && typeof out.getSceneElements !== 'function') rootRef = out;
          else if (typeof out.getSceneElements === 'function') apiRef = out;
          if (typeof opts.onReady === 'function') opts.onReady();
        });
      }).catch(function () {
        if (!destroyed && typeof opts.onError === 'function') opts.onError('编辑器加载失败，请检查网络');
      });
    }

    return { getJSON: getJSON, destroy: destroy };
  }

  return { init: init };
})();
