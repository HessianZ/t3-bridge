(() => {
  const fragment = new URLSearchParams(location.hash.slice(1));
  const launchKey = fragment.get('workspaceSession');
  if (launchKey) {
    sessionStorage.setItem('workspaceSession', launchKey);
    history.replaceState(history.state, '', location.pathname + location.search);
  }
  const keyField = document.querySelector('input[name="workspaceSession"]');
  if (!keyField) return;
  keyField.value = sessionStorage.getItem('workspaceSession') || '';
  const form = keyField.form;
  const button = form.querySelector('button[type="submit"]');
  const status = form.querySelector('[role="status"]');
  if (!keyField.value) {
    button.disabled = true;
    status.textContent = '请从终端打印的完整地址打开核验页面，或重新打开桌面工作台。';
  }
  form.addEventListener('submit', () => {
    button.disabled = true;
    status.textContent = '正在核验本机飞书工具身份…';
  });
})();
