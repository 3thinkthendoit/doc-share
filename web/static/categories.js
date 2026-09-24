// 分类管理：新建/编辑走弹窗（页面文案统一经 UI.t 查“中文原文→译文”表）
// 注意：必须用 form.elements 取控件——form.id / form.name 会被 HTMLFormElement 内建属性遮蔽
var categoryModal = document.getElementById('categoryModal');
var categoryForm = document.getElementById('categoryForm');
UI.bindModal(categoryModal);

function cf() { return categoryForm.elements; }

document.getElementById('btnNewCategory').onclick = function () {
  categoryForm.reset();
  cf().id.value = '';
  cf().sort.value = '0';
  document.getElementById('categoryModalTitle').textContent = UI.t('新建分类');
  UI.openModal(categoryModal);
};

function editCategory(btn) {
  cf().id.value = btn.dataset.id;
  cf().name.value = btn.dataset.name;
  cf().sort.value = btn.dataset.sort || '0';
  document.getElementById('categoryModalTitle').textContent = UI.t('编辑分类');
  UI.openModal(categoryModal);
}

categoryForm.addEventListener('submit', async function (e) {
  e.preventDefault();
  if (!UI.validateForm(categoryForm)) return;
  var id = cf().id.value;
  var payload = {
    name: cf().name.value.trim(),
    sort: parseInt(cf().sort.value, 10) || 0
  };
  try {
    var res = await fetch(id ? '/admin/api/categories/' + id : '/admin/api/categories', {
      method: id ? 'PUT' : 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
      body: JSON.stringify(payload)
    });
    var data = await res.json();
    if (!res.ok) { UI.alert(data.error || '保存失败', { title: '保存失败' }); return; }
    UI.closeModal(categoryModal);
    location.reload();
  } catch (err) {
    UI.toast('网络错误，保存失败', 'error');
  }
});

async function delCategory(btn) {
  var ok = await UI.confirm(UI.t('确定删除分类「{0}」？其下文档将变为未分类。', btn.dataset.name));
  if (!ok) return;
  try {
    var res = await fetch('/admin/api/categories/' + btn.dataset.id, {
      method: 'DELETE',
      headers: { 'X-Requested-With': 'XMLHttpRequest' }
    });
    if (res.ok) { location.reload(); }
    else {
      var data = await res.json();
      UI.toast(data.error || '删除失败', 'error');
    }
  } catch (err) {
    UI.toast('网络错误，删除失败', 'error');
  }
}
