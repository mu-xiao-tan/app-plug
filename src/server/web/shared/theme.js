(() => {
  const picker = document.querySelector('.appearance');
  if (!picker) return;
  const trigger = picker.querySelector('summary');
  const choices = Array.from(picker.querySelectorAll('input'));
  const system = matchMedia('(prefers-color-scheme: dark)');
  const key = 'app-plug.theme';
  function apply() {
    const selected = choices.find(input => input.checked);
    document.documentElement.dataset.theme = selected.value === 'system'
      ? (system.matches ? 'forest' : 'light') : selected.value;
    trigger.setAttribute('aria-label', `外观：${selected.labels[0].textContent.trim()}`);
  }
  function restore() {
    let value;
    try { value = localStorage.getItem(key); } catch { /* 存储受限时跟随系统。 */ }
    (choices.find(input => input.value === value) || choices[0]).checked = true;
    apply();
  }
  function close() {
    picker.open = false;
    trigger.focus();
  }
  restore();
  picker.hidden = false;
  picker.addEventListener('change', event => {
    apply();
    try { localStorage.setItem(key, event.target.value); } catch { /* 仍在当前页面生效。 */ }
  });
  // 鼠标选择后收起；方向键选择保持面板打开，便于比较。
  picker.addEventListener('click', event => {
    if (event.target.matches('input') && event.detail > 0) close();
  });
  picker.addEventListener('keydown', event => {
    if (picker.open && event.key === 'Escape') { event.preventDefault(); close(); }
  });
  picker.addEventListener('focusout', event => {
    if (event.relatedTarget && !picker.contains(event.relatedTarget)) picker.open = false;
  });
  document.addEventListener('pointerdown', event => {
    if (picker.open && !picker.contains(event.target)) picker.open = false;
  });
  system.addEventListener('change', apply);
  window.addEventListener('storage', event => { if (event.key === key || event.key === null) restore(); });
})();
