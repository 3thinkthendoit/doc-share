// 项目管理：新建/编辑走弹窗，保存后刷新列表
// 注意：必须用 form.elements 取控件——form.id / form.name 会被 HTMLFormElement 内建属性遮蔽
var projectModal = document.getElementById('projectModal');
var projectForm = document.getElementById('projectForm');
UI.bindModal(projectModal);

function pf() { return projectForm.elements; }

document.getElementById('btnNewProject').onclick = function () {
  projectForm.reset();
  pf().id.value = '';
  document.getElementById('projectModalTitle').textContent = UI.t('新建项目');
  UI.openModal(projectModal);
};

function editProject(btn) {
  pf().id.value = btn.dataset.id;
  pf().name.value = btn.dataset.name;
  pf().description.value = btn.dataset.desc || '';
  document.getElementById('projectModalTitle').textContent = UI.t('编辑项目');
  UI.openModal(projectModal);
}

projectForm.addEventListener('submit', async function (e) {
  e.preventDefault();
  if (!UI.validateForm(projectForm)) return;
  var id = pf().id.value;
  var payload = {
    name: pf().name.value.trim(),
    description: pf().description.value.trim()
  };
  try {
    var res = await fetch(id ? '/admin/api/projects/' + id : '/admin/api/projects', {
      method: id ? 'PUT' : 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
      body: JSON.stringify(payload)
    });
    var data = await res.json();
    if (!res.ok) { UI.alert(data.error || '保存失败', { title: '保存失败' }); return; }
    UI.closeModal(projectModal);
    location.reload();
  } catch (err) {
    UI.toast('网络错误，保存失败', 'error');
  }
});

async function delProject(btn) {
  var ok = await UI.confirm(UI.t('确定删除项目「{0}」？其下文档将回到未分组。', btn.dataset.name));
  if (!ok) return;
  try {
    var res = await fetch('/admin/api/projects/' + btn.dataset.id, {
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
