// 思维导图主题/结构选择栏（共用模块）：
//   embeds.js     —— Markdown 编辑器弹窗
//   json_editor.js —— 独立思维导图文档编辑页（json_edit / json_view）
// 依赖：simple-mind-map 主题插件已 init（MindMap.__dsThemes）、全局 UI.t（可选）
window.MindmapToolbar = (function () {
  'use strict';

  function t(key, fallback) {
    if (window.UI && UI.t) {
      var v = UI.t(key);
      if (v && v !== key) return v;
    }
    return fallback;
  }

  var LAYOUTS = [
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

  function themeOptions(MindMap) {
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

  function hasOption(sel, value) {
    return [].some.call(sel.options, function (o) { return o.value === value; });
  }

  // opts: { MindMap, mount, theme, layout, readonly, standalone }
  // standalone=true 时用于独立文档页（弹窗外），附加外边框与下边距
  function build(opts) {
    opts = opts || {};
    var MindMap = opts.MindMap;
    var bar = document.createElement('div');
    bar.className = 'embed-toolbar';
    if (opts.standalone) {
      bar.style.marginBottom = '8px';
      bar.style.border = '1px solid var(--border)';
      bar.style.borderRadius = 'var(--radius-sm)';
    }

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
    themeOptions(MindMap).forEach(function (item) {
      var opt = document.createElement('option');
      opt.value = item.value;
      opt.textContent = item.name + (item.dark ? ' · dark' : '');
      themeSel.appendChild(opt);
    });
    var themeValue = opts.theme || 'classicBlue';
    if (!hasOption(themeSel, themeValue)) {
      // 主题插件列表缺当前值（如内置 classicBlue）时补一项，保证下拉与实例主题一致
      var missing = document.createElement('option');
      missing.value = themeValue;
      missing.textContent = themeValue;
      themeSel.appendChild(missing);
    }
    themeSel.value = themeValue;

    var layoutSel = document.createElement('select');
    layoutSel.className = 'embed-toolbar-select';
    LAYOUTS.forEach(function (item) {
      var opt = document.createElement('option');
      opt.value = item.value;
      opt.textContent = t(item.labelKey, item.fallback);
      layoutSel.appendChild(opt);
    });
    var layoutValue = opts.layout || 'mindMap';
    if (!hasOption(layoutSel, layoutValue)) {
      var missingLayout = document.createElement('option');
      missingLayout.value = layoutValue;
      missingLayout.textContent = layoutValue;
      layoutSel.appendChild(missingLayout);
    }
    layoutSel.value = layoutValue;

    themeSel.disabled = !!opts.readonly;
    layoutSel.disabled = !!opts.readonly;
    bar.appendChild(field(t('edit.embedTheme', '主题'), themeSel));
    bar.appendChild(field(t('edit.embedLayout', '结构'), layoutSel));

    if (opts.mount && opts.mount.parentNode) {
      opts.mount.parentNode.insertBefore(bar, opts.mount);
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

  return { build: build };
})();
