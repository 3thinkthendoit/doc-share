// 文档编辑页：实时预览 + 保存 + 分享设置
(function () {
  var titleEl = document.getElementById('docTitle');
  var contentEl = document.getElementById('docContent');
  var previewEl = document.getElementById('preview');
  var saveBtn = document.getElementById('saveBtn');
  if (!contentEl) return;

  if (previewEl) previewEl.classList.add('edit-preview');

  // 窄屏：编辑 / 预览 Tab 切换（桌面 CSS 强制双栏）
  (function bindEditPaneTabs() {
    var grid = document.querySelector('.edit-grid');
    var tabs = document.querySelectorAll('.edit-pane-tab');
    if (!grid || !tabs.length) return;
    grid.classList.add('is-write');
    tabs.forEach(function (tab) {
      tab.addEventListener('click', function () {
        var pane = tab.getAttribute('data-edit-pane') || 'write';
        grid.classList.toggle('is-write', pane === 'write');
        grid.classList.toggle('is-preview', pane === 'preview');
        tabs.forEach(function (t) {
          var on = t === tab;
          t.classList.toggle('active', on);
          t.setAttribute('aria-selected', on ? 'true' : 'false');
        });
        if (pane === 'preview') doPreview();
      });
    });
  })();

  function doPreview() {
    if (!previewEl) return;
    // 正在预览里改单元格时跳过重绘，避免打断输入
    if (previewEl.contains(document.activeElement) && document.activeElement.isContentEditable) {
      return;
    }
    if (window.marked && window.DOMPurify) {
      previewEl.innerHTML = DOMPurify.sanitize(marked.parse(contentEl.value || ''));
    } else {
      previewEl.textContent = contentEl.value || '';
    }
    bindPreviewTables();
  }
  var dirty = false;
  var saving = false;
  var saveLabelDefault = UI.t('保存');
  var saveStatusTimer = null;
  function setSaveBtn(label, disabled) {
    if (!saveBtn) return;
    saveBtn.textContent = label || saveLabelDefault;
    saveBtn.disabled = !!disabled;
  }
  function markDirty() {
    dirty = true;
    if (saveStatusTimer) { clearTimeout(saveStatusTimer); saveStatusTimer = null; }
    if (!saving) setSaveBtn(saveLabelDefault, false);
  }
  function hasSaveableContent() {
    return !!(String(titleEl && titleEl.value || '').trim() || String(contentEl.value || '').trim());
  }
  function effectiveTitle() {
    var t = String(titleEl && titleEl.value || '').trim();
    return t || UI.t('edit.untitled');
  }
  var timer = null;
  contentEl.addEventListener('input', function () {
    markDirty();
    clearTimeout(timer);
    timer = setTimeout(doPreview, 150);
  });
  if (titleEl) titleEl.addEventListener('input', markDirty);
  var projEl = document.getElementById('docProject');
  var catEl = document.getElementById('docCategory');
  if (projEl) projEl.addEventListener('change', markDirty);
  if (catEl) catEl.addEventListener('change', markDirty);
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
      case 'table': insertTableInteractive(); return;
      case 'image': if (uploadInput) uploadInput.click(); return;
      case 'import': if (importInput) importInput.click(); return;
      default: return;
    }
    doPreview();
    contentEl.focus();
  }

  // 插入表格：单框内「列数 × 行数」一并填写
  function promptTableSize() {
    return new Promise(function (resolve) {
      var overlay = document.createElement('div');
      overlay.className = 'modal-overlay ui-dialog';
      var modal = document.createElement('div');
      modal.className = 'dialog';

      var msg = document.createElement('div');
      msg.className = 'dialog-msg';
      msg.textContent = UI.t('edit.tableSizeTitle');
      modal.appendChild(msg);

      var row = document.createElement('div');
      row.className = 'dialog-size';

      function field(labelText, value) {
        var lab = document.createElement('label');
        lab.className = 'dialog-size-field';
        var cap = document.createElement('span');
        cap.textContent = labelText;
        var inp = document.createElement('input');
        inp.type = 'text';
        inp.inputMode = 'numeric';
        inp.className = 'dialog-input';
        inp.value = value;
        lab.appendChild(cap);
        lab.appendChild(inp);
        return { lab: lab, inp: inp };
      }

      var colsF = field(UI.t('edit.tableColsShort'), '5');
      var times = document.createElement('span');
      times.className = 'dialog-size-x';
      times.textContent = '×';
      times.setAttribute('aria-hidden', 'true');
      var rowsF = field(UI.t('edit.tableRowsShort'), '3');
      row.appendChild(colsF.lab);
      row.appendChild(times);
      row.appendChild(rowsF.lab);
      modal.appendChild(row);

      var foot = document.createElement('div');
      foot.className = 'dialog-foot';
      var cancelBtn = document.createElement('button');
      cancelBtn.type = 'button';
      cancelBtn.className = 'btn';
      cancelBtn.textContent = UI.t('取消');
      var okBtn = document.createElement('button');
      okBtn.type = 'button';
      okBtn.className = 'btn btn-primary';
      okBtn.textContent = UI.t('确定');
      foot.appendChild(cancelBtn);
      foot.appendChild(okBtn);
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
      function confirm() {
        var cols = parseInt(String(colsF.inp.value).trim(), 10);
        var rows = parseInt(String(rowsF.inp.value).trim(), 10);
        var bad = false;
        colsF.inp.classList.toggle('error', !(cols >= 1 && cols <= 20));
        rowsF.inp.classList.toggle('error', !(rows >= 1 && rows <= 50));
        if (!(cols >= 1 && cols <= 20)) {
          UI.toast(UI.t('edit.tableColsInvalid'), 'error');
          colsF.inp.focus();
          bad = true;
        } else if (!(rows >= 1 && rows <= 50)) {
          UI.toast(UI.t('edit.tableRowsInvalid'), 'error');
          rowsF.inp.focus();
          bad = true;
        }
        if (bad) return;
        close({ cols: cols, rows: rows });
      }
      function onKey(e) {
        if (e.key === 'Escape') { close(null); return; }
        if (e.key === 'Enter') { e.preventDefault(); confirm(); }
      }
      cancelBtn.addEventListener('click', function () { close(null); });
      okBtn.addEventListener('click', confirm);
      overlay.addEventListener('click', function (e) {
        if (e.target === overlay) close(null);
      });
      document.addEventListener('keydown', onKey);
      document.body.appendChild(overlay);
      requestAnimationFrame(function () {
        overlay.classList.add('show');
        colsF.inp.focus();
        colsF.inp.select();
      });
    });
  }

  async function insertTableInteractive() {
    var size = await promptTableSize();
    if (!size) return;
    var cols = size.cols;
    var rows = size.rows;
    var colLabel = UI.t('列');
    var cell = UI.t('内容');
    var headers = [];
    var seps = [];
    var i;
    for (i = 0; i < cols; i++) {
      headers.push(colLabel + (i + 1));
      seps.push('---');
    }
    var lines = [formatTableRow(headers), formatTableRow(seps)];
    for (i = 0; i < rows; i++) {
      var cells = [];
      for (var c = 0; c < cols; c++) cells.push(cell);
      lines.push(formatTableRow(cells));
    }
    insertAtCursor('\n' + lines.join('\n') + '\n');
  }

  function formatTableRow(cells) {
    return '| ' + cells.join(' | ') + ' |';
  }
  function isTableLine(line) {
    var t = String(line || '').trim();
    return t.charAt(0) === '|' && t.indexOf('|', 1) !== -1;
  }
  function isSepLine(line) {
    if (!isTableLine(line)) return false;
    var cells = splitTableCells(line);
    if (!cells.length) return false;
    for (var i = 0; i < cells.length; i++) {
      if (!/^:?-{1,}:?$/.test(String(cells[i]).trim())) return false;
    }
    return true;
  }
  function splitTableCells(line) {
    var t = String(line || '').trim();
    if (t.charAt(0) === '|') t = t.slice(1);
    if (t.charAt(t.length - 1) === '|') t = t.slice(0, -1);
    return t.split('|').map(function (c) { return c.trim(); });
  }
  function cellIndexAt(line, offset) {
    offset = Math.max(0, Math.min(offset, line.length));
    var pipes = 0;
    for (var i = 0; i < offset; i++) {
      if (line.charAt(i) === '|') pipes++;
    }
    if (/^\s*\|/.test(line)) return Math.max(0, pipes - 1);
    return Math.max(0, pipes);
  }
  // 光标所在 Markdown 表格块；无效则返回 null
  function findTableAtCursor() {
    var text = contentEl.value;
    var pos = contentEl.selectionStart;
    var lines = text.split('\n');
    var lineIdx = 0;
    var charCount = 0;
    for (; lineIdx < lines.length; lineIdx++) {
      var lineEnd = charCount + lines[lineIdx].length;
      if (pos <= lineEnd || lineIdx === lines.length - 1) break;
      charCount = lineEnd + 1;
    }
    if (lineIdx >= lines.length || !isTableLine(lines[lineIdx])) return null;

    var start = lineIdx;
    var end = lineIdx;
    while (start > 0 && isTableLine(lines[start - 1])) start--;
    while (end < lines.length - 1 && isTableLine(lines[end + 1])) end++;

    var block = lines.slice(start, end + 1);
    if (block.length < 2 || !isSepLine(block[1])) return null;

    var rows = block.map(splitTableCells);
    var colCount = rows[0].length;
    if (colCount < 1) return null;
    rows = rows.map(function (r) {
      var copy = r.slice();
      while (copy.length < colCount) copy.push('');
      return copy.slice(0, colCount);
    });

    var absStart = 0;
    for (var i = 0; i < start; i++) absStart += lines[i].length + 1;
    var absEnd = absStart;
    for (var j = start; j <= end; j++) {
      absEnd += lines[j].length;
      if (j < end) absEnd += 1;
    }

    var colIdx = Math.min(cellIndexAt(lines[lineIdx], pos - charCount), colCount - 1);

    return {
      start: absStart,
      end: absEnd,
      rowIdx: lineIdx - start,
      colIdx: colIdx,
      rows: rows,
    };
  }
  // 枚举正文中全部 Markdown 表格（与预览 DOM 顺序一致）
  function findAllTables() {
    var text = contentEl.value;
    var lines = text.split('\n');
    var tables = [];
    var i = 0;
    var abs = 0;
    while (i < lines.length) {
      if (!isTableLine(lines[i])) {
        abs += lines[i].length + 1;
        i++;
        continue;
      }
      var startLine = i;
      var startAbs = abs;
      while (i < lines.length && isTableLine(lines[i])) {
        abs += lines[i].length + 1;
        i++;
      }
      var endLine = i - 1;
      var block = lines.slice(startLine, endLine + 1);
      var endAbs = abs - 1;
      if (block.length >= 2 && isSepLine(block[1])) {
        var rows = block.map(splitTableCells);
        var colCount = rows[0].length;
        if (colCount >= 1) {
          rows = rows.map(function (r) {
            var copy = r.slice();
            while (copy.length < colCount) copy.push('');
            return copy.slice(0, colCount);
          });
          tables.push({ start: startAbs, end: endAbs, rows: rows });
        }
      }
    }
    return tables;
  }
  function writeTable(info, rows, opts) {
    opts = opts || {};
    var colCount = rows[0].length;
    var out = [];
    for (var i = 0; i < rows.length; i++) {
      if (i === 1) {
        var seps = [];
        for (var s = 0; s < colCount; s++) seps.push('---');
        out.push(formatTableRow(seps));
      } else {
        var r = rows[i].slice();
        while (r.length < colCount) r.push('');
        out.push(formatTableRow(r.slice(0, colCount)));
      }
    }
    var block = out.join('\n');
    contentEl.value = contentEl.value.slice(0, info.start) + block + contentEl.value.slice(info.end);
    contentEl.selectionStart = contentEl.selectionEnd = info.start + Math.min(block.length, out[0].length);
    markDirty();
    if (!opts.skipPreview) doPreview();
    if (opts.focusSource) contentEl.focus();
  }
  function applyTableOp(op, info, writeOpts) {
    info = info || findTableAtCursor();
    if (!info) return;
    var rows = info.rows.map(function (r) { return r.slice(); });
    var ri = info.rowIdx;
    var ci = info.colIdx;
    var colCount = rows[0].length;
    var emptyRow = function () {
      var a = [];
      for (var i = 0; i < colCount; i++) a.push('');
      return a;
    };

    if (op === 'row-above') {
      rows.splice(ri <= 1 ? 2 : ri, 0, emptyRow());
    } else if (op === 'row-below') {
      rows.splice(ri <= 1 ? 2 : ri + 1, 0, emptyRow());
    } else if (op === 'row-delete') {
      if (ri <= 1) {
        UI.toast(UI.t('edit.tableNeedDataRow'), 'error');
        return;
      }
      rows.splice(ri, 1);
    } else if (op === 'col-left' || op === 'col-right') {
      var at = op === 'col-left' ? ci : ci + 1;
      var label = UI.t('列') + (colCount + 1);
      rows.forEach(function (r, i) {
        r.splice(at, 0, i === 0 ? label : (i === 1 ? '---' : ''));
      });
    } else if (op === 'col-delete') {
      if (colCount <= 1) {
        UI.toast(UI.t('edit.tableMinCol'), 'error');
        return;
      }
      rows.forEach(function (r) { r.splice(ci, 1); });
    } else {
      return;
    }
    writeTable(info, rows, writeOpts || { focusSource: true });
  }

  var tableMenuEl = null;
  function hideTableMenu() {
    if (tableMenuEl) {
      tableMenuEl.remove();
      tableMenuEl = null;
    }
    document.removeEventListener('click', hideTableMenu);
    document.removeEventListener('keydown', onTableMenuKey);
    window.removeEventListener('resize', hideTableMenu);
    window.removeEventListener('scroll', hideTableMenu, true);
  }
  function onTableMenuKey(e) {
    if (e.key === 'Escape') hideTableMenu();
  }
  function showTableMenu(x, y, getInfo, writeOpts) {
    hideTableMenu();
    var menu = document.createElement('div');
    menu.className = 'md-table-menu';
    menu.setAttribute('role', 'menu');
    var items = [
      { op: 'row-above', label: UI.t('edit.tableRowAbove') },
      { op: 'row-below', label: UI.t('edit.tableRowBelow') },
      { op: 'row-delete', label: UI.t('edit.tableRowDelete'), danger: true },
      { sep: true },
      { op: 'col-left', label: UI.t('edit.tableColLeft') },
      { op: 'col-right', label: UI.t('edit.tableColRight') },
      { op: 'col-delete', label: UI.t('edit.tableColDelete'), danger: true },
    ];
    items.forEach(function (it) {
      if (it.sep) {
        var hr = document.createElement('div');
        hr.className = 'md-table-menu-sep';
        menu.appendChild(hr);
        return;
      }
      var btn = document.createElement('button');
      btn.type = 'button';
      btn.setAttribute('role', 'menuitem');
      if (it.danger) btn.className = 'danger';
      btn.textContent = it.label;
      btn.addEventListener('click', function (e) {
        e.stopPropagation();
        hideTableMenu();
        var info = typeof getInfo === 'function' ? getInfo() : findTableAtCursor();
        applyTableOp(it.op, info, writeOpts || { focusSource: true });
      });
      menu.appendChild(btn);
    });
    document.body.appendChild(menu);
    var pad = 8;
    var rect = menu.getBoundingClientRect();
    var left = Math.min(x, window.innerWidth - rect.width - pad);
    var top = Math.min(y, window.innerHeight - rect.height - pad);
    menu.style.left = Math.max(pad, left) + 'px';
    menu.style.top = Math.max(pad, top) + 'px';
    tableMenuEl = menu;
    setTimeout(function () {
      document.addEventListener('click', hideTableMenu);
      document.addEventListener('keydown', onTableMenuKey);
      window.addEventListener('resize', hideTableMenu);
      window.addEventListener('scroll', hideTableMenu, true);
    }, 0);
  }
  contentEl.addEventListener('contextmenu', function (e) {
    if (!findTableAtCursor()) return;
    e.preventDefault();
    showTableMenu(e.clientX, e.clientY);
  });

  function cellMdText(el) {
    return String(el.textContent || '').replace(/\s+/g, ' ').replace(/\|/g, '｜').trim();
  }
  function rowsFromDOMTable(table) {
    var headers = [];
    if (table.tHead && table.tHead.rows[0]) {
      headers = Array.prototype.map.call(table.tHead.rows[0].cells, cellMdText);
    }
    var bodyRows = [];
    if (table.tBodies[0]) {
      Array.prototype.forEach.call(table.tBodies[0].rows, function (tr) {
        bodyRows.push(Array.prototype.map.call(tr.cells, cellMdText));
      });
    }
    var colCount = headers.length;
    bodyRows.forEach(function (r) { colCount = Math.max(colCount, r.length); });
    if (!colCount) return null;
    while (headers.length < colCount) headers.push('');
    var seps = [];
    for (var i = 0; i < colCount; i++) seps.push('---');
    bodyRows = bodyRows.map(function (r) {
      var copy = r.slice();
      while (copy.length < colCount) copy.push('');
      return copy.slice(0, colCount);
    });
    return [headers.slice(0, colCount), seps].concat(bodyRows);
  }
  function previewTableIndex(table) {
    if (!previewEl) return -1;
    var tables = previewEl.querySelectorAll('table');
    return Array.prototype.indexOf.call(tables, table);
  }
  function mdRowColFromCell(table, cell) {
    var col = cell.cellIndex;
    var tr = cell.parentNode;
    if (table.tHead && table.tHead.contains(tr)) {
      return { rowIdx: 0, colIdx: col };
    }
    var body = table.tBodies[0];
    var ri = body ? Array.prototype.indexOf.call(body.rows, tr) : -1;
    return { rowIdx: ri >= 0 ? ri + 2 : 0, colIdx: col };
  }
  function syncDOMTableToMarkdown(table) {
    var idx = previewTableIndex(table);
    var tables = findAllTables();
    if (idx < 0 || idx >= tables.length) return;
    var rows = rowsFromDOMTable(table);
    if (!rows) return;
    writeTable(tables[idx], rows, { skipPreview: true, focusSource: false });
  }
  function bindPreviewTables() {
    if (!previewEl) return;
    var tables = previewEl.querySelectorAll('table');
    tables.forEach(function (table) {
      if (table.dataset.mdBound) return;
      table.dataset.mdBound = '1';
      table.classList.add('md-preview-table');
      table.querySelectorAll('th,td').forEach(function (cell) {
        cell.contentEditable = 'true';
        cell.spellcheck = false;
        var syncTimer = null;
        cell.addEventListener('input', function () {
          clearTimeout(syncTimer);
          syncTimer = setTimeout(function () { syncDOMTableToMarkdown(table); }, 200);
        });
        cell.addEventListener('blur', function () {
          clearTimeout(syncTimer);
          syncDOMTableToMarkdown(table);
        });
      });
      table.addEventListener('contextmenu', function (e) {
        var cell = e.target.closest('th,td');
        if (!cell || !table.contains(cell)) return;
        e.preventDefault();
        e.stopPropagation();
        var pos = mdRowColFromCell(table, cell);
        showTableMenu(e.clientX, e.clientY, function () {
          var idx = previewTableIndex(table);
          var all = findAllTables();
          if (idx < 0 || idx >= all.length) return null;
          var info = all[idx];
          info.rowIdx = pos.rowIdx;
          info.colIdx = Math.min(pos.colIdx, info.rows[0].length - 1);
          return info;
        }, { focusSource: false });
      });
    });
  }

  // 用标记包裹选区（无选区时插入占位文本并选中，方便直接改）
  function wrapSel(before, after, placeholder) {
    var s = contentEl.selectionStart, e = contentEl.selectionEnd;
    var sel = contentEl.value.slice(s, e) || placeholder;
    contentEl.value = contentEl.value.slice(0, s) + before + sel + after + contentEl.value.slice(e);
    contentEl.selectionStart = s + before.length;
    contentEl.selectionEnd = s + before.length + sel.length;
    markDirty();
  }
  // 在当前行行首加前缀（标题/引用/列表）
  function prefixLine(prefix) {
    var s = contentEl.selectionStart;
    var lineStart = contentEl.value.lastIndexOf('\n', s - 1) + 1;
    contentEl.value = contentEl.value.slice(0, lineStart) + prefix + contentEl.value.slice(lineStart);
    contentEl.selectionStart = contentEl.selectionEnd = s + prefix.length;
    markDirty();
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
    markDirty();
    doPreview();
    contentEl.focus();
  }

  var docID = function () { return window.DOC_ID || 0; }; // 惰性读取，避免脚本加载顺序问题

  // 编辑占用心跳：每 10 秒上报；新建首次保存出 ID 后可延迟启动
  var editingTimer = null;
  function ensureEditingHeartbeat() {
    if (editingTimer || !docID()) return;
    var banner = document.getElementById('editingBanner');
    if (!banner) return;
    async function pollEditing() {
      if (!docID()) return;
      try {
        var res = await fetch('/admin/api/docs/' + docID() + '/editing', {
          method: 'POST', headers: { 'X-Requested-With': 'XMLHttpRequest' }
        });
        if (!res.ok) { banner.hidden = true; return; }
        var data = await res.json();
        var editors = (data && data.editors) || [];
        if (editors.length) {
          banner.textContent = editors.join('、') + ' ' + UI.t('正在编辑此文档');
          banner.hidden = false;
        } else {
          banner.hidden = true;
        }
      } catch (e) { /* 轮询失败忽略 */ }
    }
    pollEditing();
    editingTimer = setInterval(pollEditing, 10000);
  }
  ensureEditingHeartbeat();

  function buildSavePayload() {
    return {
      title: effectiveTitle(),
      content: contentEl.value,
      project_id: parseInt((projEl && projEl.value) || '0', 10) || 0,
      category_id: parseInt((catEl && catEl.value) || '0', 10) || 0
    };
  }

  // 离开页面前尽力补存（keepalive）；取消关闭对话框后允许再次触发
  var leaveFlushed = false;
  function flushSaveOnLeave() {
    if (leaveFlushed || !saveBtn || !dirty || saving || !hasSaveableContent()) return;
    leaveFlushed = true;
    var id = docID();
    var payload = buildSavePayload();
    var url = id ? '/admin/api/docs/' + id : '/admin/api/docs';
    var method = id ? 'PUT' : 'POST';
    try {
      fetch(url, {
        method: method,
        headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
        body: JSON.stringify(payload),
        keepalive: true,
        credentials: 'same-origin',
      });
    } catch (e) { /* 离开时尽力而为 */ }
  }
  window.addEventListener('beforeunload', function (e) {
    if (!saveBtn || !dirty) return;
    flushSaveOnLeave();
    e.preventDefault();
    e.returnValue = '';
    // 用户取消离开时恢复补存资格
    setTimeout(function () { leaveFlushed = false; }, 0);
  });
  window.addEventListener('pagehide', function () {
    if (dirty) flushSaveOnLeave();
  });

  async function save(opts) {
    opts = opts || {};
    if (!saveBtn && !opts.auto) return;
    if (opts.auto && !hasSaveableContent()) return;
    if (saving) return;
    saving = true;
    setSaveBtn(UI.t('edit.saving'), true);

    var id = docID();
    var payload = buildSavePayload();
    var title = payload.title;
    // 记下本次提交快照：保存过程中若用户继续改，成功后不能清 dirty
    var snapContent = payload.content;
    var snapTitle = payload.title;
    var snapProj = payload.project_id;
    var snapCat = payload.category_id;
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
      saving = false;
      setSaveBtn(saveLabelDefault, false);
      if (!opts.auto) UI.toast('网络错误，保存失败，内容仍在编辑页，请勿刷新', 'error');
      return;
    }
    saving = false;
    if (!res.ok) {
      setSaveBtn(saveLabelDefault, false);
      if (!opts.auto) UI.alert((data && data.error) || '保存失败');
      return;
    }
    if (!id && data && data.data && data.data.id) {
      window.DOC_ID = data.data.id;
      try {
        history.replaceState(null, '', '/admin/docs/' + data.data.id + '/edit');
      } catch (e) { /* ignore */ }
      if (titleEl && !String(titleEl.value || '').trim()) {
        titleEl.value = title;
      }
      ensureEditingHeartbeat();
      if (!opts.auto) UI.toast('创建成功', 'success');
    } else if (!opts.auto) {
      UI.toast('保存成功', 'success');
    }
    var stillDirty =
      contentEl.value !== snapContent ||
      effectiveTitle() !== snapTitle ||
      (parseInt((projEl && projEl.value) || '0', 10) || 0) !== snapProj ||
      (parseInt((catEl && catEl.value) || '0', 10) || 0) !== snapCat;
    dirty = stillDirty;
    if (stillDirty) {
      setSaveBtn(saveLabelDefault, false);
    } else {
      setSaveBtn(UI.t('edit.autosaved'), false);
      if (saveStatusTimer) clearTimeout(saveStatusTimer);
      saveStatusTimer = setTimeout(function () {
        saveStatusTimer = null;
        if (!dirty && !saving) setSaveBtn(saveLabelDefault, false);
      }, 2500);
    }
  }
  if (saveBtn) saveBtn.addEventListener('click', function () { save({}); });

  // 有改动且有内容时每 15 秒自动保存到服务端
  if (saveBtn) {
    setInterval(function () {
      if (dirty && !saving) save({ auto: true });
    }, 15000);
  }

  // 分享设置弹窗（开关/遮罩/ESC 由 UI.bindModal 统一处理）
  var shareModal = document.getElementById('shareModal');
  var openShareBtn = document.getElementById('shareBtn');
  UI.bindModal(shareModal);
  if (openShareBtn) openShareBtn.addEventListener('click', function () {
    UI.openModal(shareModal);
    if (enabledEl && enabledEl.checked) loadAccessRequests();
  });

  var enabledEl = document.getElementById('shareEnabled');
  var configEl = document.getElementById('shareConfig');
  var accessReqBox = document.getElementById('accessReqBox');
  var saveShareBtn = document.getElementById('saveShareBtn');
  if (enabledEl) {
    enabledEl.addEventListener('change', function () {
      configEl.style.display = enabledEl.checked ? 'block' : 'none';
      if (accessReqBox) accessReqBox.hidden = !enabledEl.checked;
      if (enabledEl.checked) loadAccessRequests();
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
        if (accessReqBox) accessReqBox.hidden = false;
        loadAccessRequests();
      } else if (!enabledEl.checked) {
        document.getElementById('shareLink').style.display = 'none';
        if (accessReqBox) accessReqBox.hidden = true;
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

  async function loadAccessRequests() {
    var list = document.getElementById('accessReqList');
    if (!list || !docID()) return;
    list.innerHTML = '<div class="muted">' + UI.t('加载中…') + '</div>';
    try {
      var res = await fetch('/admin/api/docs/' + docID() + '/access-requests', {
        headers: { 'X-Requested-With': 'XMLHttpRequest' }
      });
      var data = await res.json();
      if (!res.ok) {
        list.innerHTML = '<div class="muted">' + UI.t((data && data.error) || '加载失败') + '</div>';
        return;
      }
      var items = (data && data.data) || [];
      if (!items.length) {
        list.innerHTML = '';
        var box = document.createElement('div');
        box.className = 'empty-block';
        var title = document.createElement('div');
        title.className = 'empty-title';
        title.textContent = UI.t('share.applyListEmpty');
        box.appendChild(title);
        var tip = document.createElement('div');
        tip.className = 'empty-tip';
        tip.textContent = UI.t('share.applyListEmptyTip');
        box.appendChild(tip);
        list.appendChild(box);
        return;
      }
      var statusMap = { 0: UI.t('share.applyStatusPending'), 1: UI.t('share.applyStatusApproved'), 2: UI.t('share.applyStatusRejected') };
      var table = document.createElement('table');
      table.className = 'table';
      table.innerHTML =
        '<thead><tr>' +
        '<th>' + UI.t('dash.pendingName') + '</th>' +
        '<th>' + UI.t('dash.pendingTime') + '</th>' +
        '<th>' + UI.t('common.status') + '</th>' +
        '<th class="col-actions">' + UI.t('common.actions') + '</th>' +
        '</tr></thead>';
      var tbody = document.createElement('tbody');
      items.forEach(function (r) {
        var tr = document.createElement('tr');
        var tdName = document.createElement('td');
        tdName.textContent = r.name || '—';
        tr.appendChild(tdName);
        var tdTime = document.createElement('td');
        tdTime.className = 'cell-muted';
        tdTime.textContent = r.created_at || '—';
        tr.appendChild(tdTime);
        var tdStatus = document.createElement('td');
        var tag = document.createElement('span');
        tag.className = 'tag' + (r.status === 1 ? ' tag-green' : '');
        tag.textContent = statusMap[r.status] || String(r.status);
        tdStatus.appendChild(tag);
        tr.appendChild(tdStatus);
        var tdActs = document.createElement('td');
        tdActs.className = 'col-actions';
        if (r.status === 0) {
          var ok = document.createElement('button');
          ok.type = 'button';
          ok.className = 'btn btn-sm btn-primary';
          ok.textContent = UI.t('share.applyApprove');
          ok.addEventListener('click', function () { reviewAccess(r.id, 'approve', tdActs); });
          var no = document.createElement('button');
          no.type = 'button';
          no.className = 'btn btn-sm';
          no.textContent = UI.t('share.applyReject');
          no.addEventListener('click', function () { reviewAccess(r.id, 'reject', tdActs); });
          tdActs.appendChild(ok);
          tdActs.appendChild(no);
        } else {
          tdActs.textContent = '—';
        }
        tr.appendChild(tdActs);
        tbody.appendChild(tr);
      });
      table.appendChild(tbody);
      list.innerHTML = '';
      list.appendChild(table);
    } catch (e) {
      list.innerHTML = '<div class="muted">' + UI.t('加载失败') + '</div>';
    }
  }
  async function reviewAccess(rid, action, acts) {
    if (acts) {
      acts.querySelectorAll('button').forEach(function (b) { b.disabled = true; });
    }
    try {
      var res = await fetch('/admin/api/docs/' + docID() + '/access-requests/' + rid, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
        body: JSON.stringify({ action: action })
      });
      var data = await res.json();
      if (!res.ok) {
        UI.toast(UI.t((data && data.error) || '操作失败'), 'error');
        if (acts) {
          acts.querySelectorAll('button').forEach(function (b) { b.disabled = false; });
        }
        return;
      }
      UI.toast(UI.t('操作成功'), 'success');
      loadAccessRequests();
    } catch (e) {
      UI.toast(UI.t('网络错误'), 'error');
      if (acts) {
        acts.querySelectorAll('button').forEach(function (b) { b.disabled = false; });
      }
    }
  }
  var refreshAccessBtn = document.getElementById('refreshAccessReq');
  if (refreshAccessBtn) refreshAccessBtn.addEventListener('click', loadAccessRequests);

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
