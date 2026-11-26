const promptDialog = document.querySelector('#prompt-dialog');
const openPrompt = document.querySelector('#show-prompt');
const copyButton = document.querySelector('#copy-prompt');
const selectButton = document.querySelector('#select-prompt');
const copyResult = document.querySelector('#copy-result');
const taskChoices = promptDialog.querySelector('.prompt-tasks');
const promptTexts = promptDialog.querySelectorAll('.prompt-text');
const source = document.querySelector('#prompt-source');
const hints = {
  add: document.querySelector('#task-description').textContent,
  update: '准备好现有应用的域名，并说明要修改哪些字段。不会自动新建应用。',
  temporary: '准备好管理员签发的回执。部署密钥单独提供，不要粘贴进提示词。',
};

let activePrompt = promptDialog.querySelector('#prompt-add');
let copyRequest = 0;

function resetFeedback() {
  copyRequest++;
  copyResult.textContent = '';
}

function selectPromptText() {
  source.open = true;
  activePrompt.focus({ preventScroll: true });
  const selection = window.getSelection();
  const range = document.createRange();
  range.selectNodeContents(activePrompt);
  selection.removeAllRanges();
  selection.addRange(range);
}

taskChoices.addEventListener('change', event => {
  const selection = window.getSelection();
  if (selection && activePrompt.contains(selection.anchorNode)) selection.removeAllRanges();
  activePrompt = document.getElementById(event.target.getAttribute('aria-controls'));
  for (const text of promptTexts) text.hidden = text !== activePrompt;
  document.querySelector('#task-description').textContent = hints[event.target.value];
  activePrompt.scrollTop = 0;
  resetFeedback();
});

openPrompt.hidden = false;
openPrompt.addEventListener('click', () => {
  resetFeedback();
  promptDialog.showModal();
  taskChoices.querySelector('input:checked').focus();
});
promptDialog.addEventListener('close', () => {
  const selection = window.getSelection();
  if (selection && promptDialog.contains(selection.anchorNode)) selection.removeAllRanges();
  openPrompt.focus();
});

selectButton.addEventListener('click', () => {
  resetFeedback();
  selectPromptText();
  copyResult.textContent = '已选中文本，请手动复制。';
});

copyButton.addEventListener('click', async () => {
  resetFeedback();
  const request = copyRequest;
  try {
    await navigator.clipboard.writeText(activePrompt.textContent);
    if (promptDialog.open && request === copyRequest) copyResult.textContent = '已复制提示词。';
  } catch {
    if (!promptDialog.open || request !== copyRequest) return;
    selectPromptText();
    copyResult.textContent = '无法自动复制，已选中文本，请手动复制。';
  }
});
