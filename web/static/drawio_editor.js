// drawio 文档编辑/查看模块（window.DrawioDoc）：
// 以 iframe 嵌入自托管 drawio webapp（jgraph/drawio，Apache 2.0），经官方 embed
// proto=json 协议通信：{event:'init'} → {action:'load', xml} → {event:'save'|'autosave', xml}，
// 导出走 {action:'export', format} → {event:'export', data}。
// Content 存未压缩 mxfile XML（longtext 直存）。
// 用法：DrawioDoc.init({ content, mode: 'edit'|'view', mount, editorUrl,
//                        onReady, onError }) → { getJSON, destroy, exportPNG }
window.DrawioDoc = (function () {
  'use strict';

  // 空文档初始 mxfile：新文档进入编辑器即有合法画布
  var EMPTY_XML =
    '<mxfile host="doc-share">' +
    '<diagram id="page-1" name="Page-1">' +
    '<mxGraphModel dx="1000" dy="700" grid="1" gridSize="10" guides="1" tooltips="1" ' +
    'connect="1" arrows="1" fold="1" page="1" pageScale="1" pageWidth="850" pageHeight="1100" ' +
    'math="0" shadow="0"><root><mxCell id="0"/><mxCell id="1" parent="0"/></root>' +
    '</mxGraphModel></diagram></mxfile>';

  function normalizeContent(text) {
    var raw = String(text || '').trim();
    // 仅接受 mxfile XML；脏数据（如误存的 JSON/空串）按空文档处理，避免编辑器崩溃
    if (!raw || raw.indexOf('<mxfile') < 0) return EMPTY_XML;
    return raw;
  }

  function editorOrigin(editorUrl) {
    // 内嵌编辑器为相对路径（如 /drawio），与 DocShare 同源
    if (editorUrl.charAt(0) === '/') return window.location.origin;
    try {
      return new URL(editorUrl).origin;
    } catch (e) {
      return '';
    }
  }

  function init(opts) {
    opts = opts || {};
    var readonly = opts.mode === 'view';
    var mount = opts.mount;
    var destroyed = false;
    var ready = false;
    var iframe = null;
    var exportWaiters = [];

    var editorUrl = String(opts.editorUrl || '').replace(/\/+$/, '');
    var origin = editorOrigin(editorUrl);
    if (!editorUrl || !origin) {
      if (typeof opts.onError === 'function') {
        opts.onError('未配置 drawio 编辑器地址（drawio.editor_url / DOC_SHARE_DRAWIO_URL）');
      }
      return { getJSON: function () { return null; }, destroy: function () {}, exportPNG: null };
    }

    // latest = 最近一次来自编辑器的 XML；初始为文档内容（空文档补默认 mxfile）
    var latest = normalizeContent(opts.content);

    // embed=1 开启嵌入协议；proto=json 用 JSON 消息；spin=1 加载指示；ui=min 精简 UI。
    // 只读查看用 lightbox（导航条查看器，仅渲染不可编辑）。
    var src = editorUrl + (/[/?#]$/.test(editorUrl) ? '' : '/') +
      '?embed=1&proto=json&spin=1&libraries=1' +
      (readonly ? '&lightbox=1&nav=1' : '&ui=min');
    iframe = document.createElement('iframe');
    iframe.className = 'drawio-frame';
    iframe.style.cssText = 'width:100%;height:100%;min-height:60vh;border:0;display:block;background:#fff';
    iframe.setAttribute('allow', 'clipboard-read; clipboard-write');
    iframe.setAttribute('title', 'drawio');
    iframe.src = src;
    mount.appendChild(iframe);

    function send(msg) {
      if (iframe && iframe.contentWindow) {
        // 目标 origin 严格取配置的编辑器源；event.origin 侧在 onMessage 校验
        iframe.contentWindow.postMessage(JSON.stringify(msg), origin);
      }
    }

    function onMessage(e) {
      if (destroyed) return;
      if (e.origin !== origin) return; // 只信配置的编辑器源
      if (!iframe || e.source !== iframe.contentWindow) return; // 只信本实例 iframe，防多实例串扰
      var data = e.data;
      if (typeof data === 'string') {
        try { data = JSON.parse(data); } catch (err) { return; }
      }
      if (!data || typeof data !== 'object') return;
      switch (data.event) {
        case 'init': // 编辑器就绪，可以 load
          ready = true;
          send({ action: 'load', autosave: readonly ? 0 : 1, xml: latest });
          if (typeof opts.onReady === 'function') opts.onReady();
          break;
        case 'autosave': // autosave=1 时内容变更持续上报
        case 'save':     // 编辑器内显式保存（Ctrl+S / 完成编辑）
          if (typeof data.xml === 'string' && data.xml) latest = data.xml;
          break;
        case 'export': // {data: dataURI|base64, format}
          var out = typeof data.data === 'string' ? data.data : '';
          var fmt = String(data.format || 'png').toLowerCase();
          // 部分版本 export 返回裸 base64（无 data: 前缀），统一补全为 data URI
          if (out && out.indexOf('data:') !== 0 &&
              (fmt === 'png' || fmt === 'jpeg' || fmt === 'jpg' || fmt === 'svg')) {
            out = 'data:image/' + (fmt === 'jpg' ? 'jpeg' : fmt) + ';base64,' + out;
          }
          for (var i = 0; i < exportWaiters.length; i++) {
            exportWaiters[i].resolve(out);
          }
          exportWaiters = [];
          break;
        default:
          break;
      }
    }
    window.addEventListener('message', onMessage);

    function getJSON() {
      if (destroyed) return null;
      // 未就绪时退回文档原内容，避免自动保存把画布清空
      return normalizeContent(latest);
    }

    // 导出 PNG（data URI）；fillColor 白底。失败/超时 reject，由调用方提示。
    function exportPNG() {
      return new Promise(function (resolve, reject) {
        if (destroyed || !ready) {
          reject(new Error('drawio 编辑器未就绪'));
          return;
        }
        var waiter = { resolve: resolve, reject: reject };
        exportWaiters.push(waiter);
        send({ action: 'export', format: 'png', fill: true, fillColor: '#ffffff', shadow: false, border: true });
        setTimeout(function () {
          var idx = exportWaiters.indexOf(waiter);
          if (idx >= 0) {
            exportWaiters.splice(idx, 1);
            reject(new Error('drawio 导出超时'));
          }
        }, 20000);
      });
    }

    function destroy() {
      destroyed = true;
      exportWaiters = [];
      window.removeEventListener('message', onMessage);
      try { if (iframe && iframe.parentNode) iframe.parentNode.removeChild(iframe); } catch (e) {}
      iframe = null;
    }

    return { getJSON: getJSON, destroy: destroy, exportPNG: exportPNG };
  }

  return { init: init, defaultXml: function () { return EMPTY_XML; } };
})();
