(() => {
  const tbody = document.querySelector('#files');
  const search = document.querySelector('#file-search');
  const sort = document.querySelector('#file-sort');
  const selectAll = document.querySelector('#select-all-files');
  const selectionCount = document.querySelector('#selection-count');
  const table = document.querySelector('.file-table');
  const menu = document.querySelector('#file-context-menu');
  let clipboard = null;
  let contextEntries = [];

  const rows = () => [...tbody.querySelectorAll('tr[data-entry]')];
  const selectedEntries = () => rows()
    .filter(row => row.querySelector('.file-select')?.checked)
    .map(row => JSON.parse(row.dataset.entry));

  async function operation(payload) {
    fileError.textContent = '';
    const response = await fetch('/api/files/operations', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(payload)
    });
    if (!response.ok) {
      fileError.textContent = await response.text();
      return false;
    }
    await loadFiles(currentPath);
    return true;
  }

  function updateSelection() {
    const visibleRows = rows().filter(row => !row.hidden);
    const selected = selectedEntries();
    selectionCount.textContent = selected.length ? `已选择 ${selected.length} 项` : '';
    const visibleSelected = visibleRows.filter(row => row.querySelector('.file-select')?.checked).length;
    selectAll.checked = visibleRows.length > 0 && visibleSelected === visibleRows.length;
    selectAll.indeterminate = visibleSelected > 0 && visibleSelected < visibleRows.length;
  }

  function applyView() {
    const query = search.value.trim().toLocaleLowerCase();
    const [field, direction] = sort.value.split(':');
    const multiplier = direction === 'desc' ? -1 : 1;
    rows().sort((a, b) => {
      const left = JSON.parse(a.dataset.entry);
      const right = JSON.parse(b.dataset.entry);
      if (left.is_dir !== right.is_dir) return left.is_dir ? -1 : 1;
      const leftValue = field === 'size' ? left.size : field === 'modified' ? Date.parse(left.modified) : left.name.toLocaleLowerCase();
      const rightValue = field === 'size' ? right.size : field === 'modified' ? Date.parse(right.modified) : right.name.toLocaleLowerCase();
      const result = typeof leftValue === 'string'
        ? leftValue.localeCompare(rightValue, 'zh-CN', {numeric: true})
        : leftValue - rightValue;
      return result * multiplier;
    }).forEach(row => {
      const entry = JSON.parse(row.dataset.entry);
      row.hidden = Boolean(query && !entry.name.toLocaleLowerCase().includes(query));
      tbody.append(row);
    });
    updateSelection();
  }

  function enhanceRows() {
    rows().forEach(row => {
      if (row.querySelector('.file-select')) return;
      const entry = JSON.parse(row.dataset.entry);
      const cell = document.createElement('td');
      const checkbox = document.createElement('input');
      checkbox.type = 'checkbox';
      checkbox.className = 'file-select';
      checkbox.setAttribute('aria-label', `选择 ${entry.name}`);
      checkbox.addEventListener('change', updateSelection);
      cell.append(checkbox);
      row.prepend(cell);
      row.draggable = true;
      row.addEventListener('dragstart', event => {
        const selected = selectedEntries();
        const paths = selected.some(item => item.path === entry.path)
          ? selected.map(item => item.path)
          : [entry.path];
        event.dataTransfer.effectAllowed = 'move';
        event.dataTransfer.setData('application/x-termdock-paths', JSON.stringify(paths));
      });
      if (entry.is_dir) {
        row.addEventListener('dragover', event => {
          event.preventDefault();
          event.dataTransfer.dropEffect = 'move';
          row.classList.add('drop-target');
        });
        row.addEventListener('dragleave', () => row.classList.remove('drop-target'));
        row.addEventListener('drop', async event => {
          event.preventDefault();
          row.classList.remove('drop-target');
          let paths = [];
          try { paths = JSON.parse(event.dataTransfer.getData('application/x-termdock-paths') || '[]'); } catch (_) {}
          if (paths.length && await customConfirm(`移动 ${paths.length} 项到“${entry.name}”吗？`)) {
            await operation({action: 'move', paths, destination: entry.path});
          }
        });
      }
    });
    applyView();
  }

  function setClipboard(entries, mode) {
    if (!entries.length) return;
    clipboard = {paths: entries.map(entry => entry.path), mode};
    selectionCount.textContent = `${mode === 'move' ? '已剪切' : '已复制'} ${entries.length} 项`;
  }

  async function copyPath(path) {
    try {
      await navigator.clipboard.writeText(path);
    } catch (_) {
      const input = document.createElement('textarea');
      input.value = path;
      input.setAttribute('readonly', '');
      input.style.position = 'fixed';
      input.style.opacity = '0';
      document.body.append(input);
      input.select();
      const copied = document.execCommand('copy');
      input.remove();
      if (!copied) throw new Error('浏览器拒绝访问剪贴板');
    }
    selectionCount.textContent = '路径已复制';
  }

  async function renameEntry(entry) {
    const name = await customPrompt(`将“${entry.name}”重命名为`);
    if (name !== null && name.trim() && name.trim() !== entry.name) {
      await operation({action: 'rename', path: entry.path, name: name.trim()});
    }
  }

  async function removeEntries(entries) {
    if (!entries.length) return;
    if (await customConfirm(`确定删除选中的 ${entries.length} 项吗？此操作不可恢复。`)) {
      await operation({action: 'delete', paths: entries.map(entry => entry.path)});
    }
  }

  async function compressEntries(entries) {
    if (!entries.length) return;
    const name = await customPrompt('压缩包名称');
    if (name !== null) {
      await archiveRequest({action: 'compress', paths: entries.map(entry => entry.path), destination: currentPath, name: name.trim() || 'archive.zip'});
    }
  }

  async function pasteClipboard() {
    if (!clipboard) return;
    if (await operation({action: clipboard.mode, paths: clipboard.paths, destination: currentPath})) {
      if (clipboard.mode === 'move') clipboard = null;
    }
  }

  function hideContextMenu() {
    menu.hidden = true;
    contextEntries = [];
  }

  function configureContextMenu(entries) {
    const single = entries.length === 1;
    const entry = single ? entries[0] : null;
    const visibility = {
      open: single,
      download: single && !entry.is_dir,
      copy: entries.length > 0,
      cut: entries.length > 0,
      paste: entries.length === 0,
      'copy-path': single,
      rename: single,
      extract: single && !entry.is_dir && entry.name.toLowerCase().endsWith('.zip'),
      compress: entries.length > 0,
      delete: entries.length > 0
    };
    menu.querySelectorAll('button[data-action]').forEach(button => {
      button.hidden = !visibility[button.dataset.action];
      button.disabled = button.dataset.action === 'paste' && !clipboard;
    });
  }

  table.addEventListener('contextmenu', event => {
    const row = event.target.closest('tr[data-entry]');
    event.preventDefault();
    if (row) {
      const entry = JSON.parse(row.dataset.entry);
      const selected = selectedEntries();
      contextEntries = selected.some(item => item.path === entry.path) ? selected : [entry];
    } else {
      contextEntries = [];
    }
    configureContextMenu(contextEntries);
    menu.hidden = false;
    const maxLeft = Math.max(8, window.innerWidth - menu.offsetWidth - 8);
    const maxTop = Math.max(8, window.innerHeight - menu.offsetHeight - 8);
    menu.style.left = `${Math.max(8, Math.min(event.clientX, maxLeft))}px`;
    menu.style.top = `${Math.max(8, Math.min(event.clientY, maxTop))}px`;
  });

  menu.addEventListener('click', async event => {
    const button = event.target.closest('button[data-action]');
    if (!button || button.disabled) return;
    const action = button.dataset.action;
    const entries = [...contextEntries];
    const entry = entries[0];
    hideContextMenu();
    if (action === 'paste') await pasteClipboard();
    else if (!entry) return;
    else if (action === 'open') entry.is_dir ? loadFiles(entry.path) : openEditor(entry.path, entry.name);
    else if (action === 'download') location.href = `/api/files/download?path=${encodeURIComponent(entry.path)}`;
    else if (action === 'copy') setClipboard(entries, 'copy');
    else if (action === 'cut') setClipboard(entries, 'move');
    else if (action === 'copy-path') await copyPath(entry.path);
    else if (action === 'rename') await renameEntry(entry);
    else if (action === 'extract') await extractEntry(entry);
    else if (action === 'compress') await compressEntries(entries);
    else if (action === 'delete') await removeEntries(entries);
  });

  new MutationObserver(mutations => {
    const needsEnhancement = mutations.some(mutation => [...mutation.addedNodes].some(node => node.nodeType === 1 && node.matches?.('tr[data-entry]') && !node.querySelector('.file-select')));
    if (needsEnhancement) enhanceRows();
  }).observe(tbody, {childList: true});
  search.addEventListener('input', applyView);
  sort.addEventListener('change', applyView);
  selectAll.addEventListener('change', () => {
    rows().filter(row => !row.hidden).forEach(row => { row.querySelector('.file-select').checked = selectAll.checked; });
    updateSelection();
  });
  document.addEventListener('pointerdown', event => {
    if (!menu.hidden && !menu.contains(event.target)) hideContextMenu();
  });
  document.addEventListener('keydown', event => {
    if (event.key === 'Escape') hideContextMenu();
  });
  addEventListener('blur', hideContextMenu);
  addEventListener('resize', hideContextMenu);
  enhanceRows();
})();
