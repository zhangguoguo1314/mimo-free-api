document.addEventListener('DOMContentLoaded', () => {
  const grabBtn = document.getElementById('grabBtn');
  const saveAccountBtn = document.getElementById('saveAccountBtn');
  const copyConfigBtn = document.getElementById('copyConfigBtn');
  const exportAllBtn = document.getElementById('exportAllBtn');
  const statusEl = document.getElementById('status');
  const cookieSection = document.getElementById('cookieSection');
  const configSection = document.getElementById('configSection');
  const accountsList = document.getElementById('accountsList');
  const configOutput = document.getElementById('configOutput');

  let currentCookies = {};
  let savedAccounts = [];

  // 加载已保存的账号
  loadSavedAccounts();

  grabBtn.addEventListener('click', async () => {
    showStatus('loading', '🔍 正在抓取 Cookie...');
    grabBtn.disabled = true;

    try {
      // 获取当前活动标签页
      const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
      
      if (!tab.url.includes('xiaomimimo.com')) {
        showStatus('error', '❌ 请先打开 MiMo 网页 (aistudio.xiaomimimo.com)');
        grabBtn.disabled = false;
        return;
      }

      // 获取所有 Cookie
      const cookies = await chrome.cookies.getAll({ domain: '.xiaomimimo.com' });
      const allCookies = await chrome.cookies.getAll({});
      
      currentCookies = {};
      
      // 搜索 MiMo 相关 Cookie
      for (const c of allCookies) {
        if (c.domain.includes('mimo') || c.domain.includes('xiaomi')) {
          if (c.name === 'serviceToken') {
            currentCookies.service_token = c.value;
          }
          if (c.name === 'userId' || c.name === 'cUserId') {
            if (!currentCookies.user_id) currentCookies.user_id = c.value;
          }
          if (c.name === 'xiaomichatbot_ph' || c.name.includes('_ph')) {
            currentCookies.ph = c.value;
          }
        }
      }

      // 显示结果
      displayCookies(currentCookies);
      
      const foundCount = Object.keys(currentCookies).length;
      if (foundCount === 3) {
        showStatus('success', '✅ 成功抓取全部 Cookie！');
        saveAccountBtn.style.display = 'block';
      } else if (foundCount > 0) {
        showStatus('error', `⚠️ 只找到 ${foundCount}/3 个 Cookie，请确认已登录`);
        saveAccountBtn.style.display = 'block';
      } else {
        showStatus('error', '❌ 未找到任何 Cookie，请确认已登录 MiMo');
      }

    } catch (e) {
      showStatus('error', `❌ 错误: ${e.message}`);
    } finally {
      grabBtn.disabled = false;
    }
  });

  // 保存账号
  saveAccountBtn.addEventListener('click', () => {
    const accountName = prompt('请输入账号名称（如：主账号、小号1）：', `账号${savedAccounts.length + 1}`);
    if (!accountName) return;

    const account = {
      id: accountName,
      service_token: currentCookies.service_token || '',
      user_id: currentCookies.user_id || '',
      ph: currentCookies.ph || '',
      active: true
    };

    savedAccounts.push(account);
    chrome.storage.local.set({ accounts: savedAccounts });
    
    loadSavedAccounts();
    showStatus('success', `✅ 账号 "${accountName}" 已保存！`);
  });

  // 复制单个字段
  document.querySelectorAll('.cookie-value').forEach(el => {
    el.addEventListener('click', () => {
      const value = el.textContent;
      if (value && value !== '点击复制' && !value.includes('未找到')) {
        copyToClipboard(value);
        el.classList.add('copied');
        setTimeout(() => el.classList.remove('copied'), 2000);
      }
    });
  });

  // 复制完整配置
  copyConfigBtn.addEventListener('click', () => {
    const config = generateConfig([{
      id: 'account-1',
      ...currentCookies,
      active: true
    }]);
    copyToClipboard(JSON.stringify(config, null, 2));
    showStatus('success', '✅ 配置已复制到剪贴板！');
  });

  // 导出全部账号
  exportAllBtn.addEventListener('click', () => {
    if (savedAccounts.length === 0) {
      showStatus('error', '❌ 没有保存的账号');
      return;
    }
    
    const config = generateConfig(savedAccounts);
    copyToClipboard(JSON.stringify(config, null, 2));
    showStatus('success', `✅ ${savedAccounts.length} 个账号配置已复制！`);
  });

  function displayCookies(cookies) {
    cookieSection.style.display = 'block';
    configSection.style.display = 'block';

    // service_token
    const hasServiceToken = !!cookies.service_token;
    document.getElementById('serviceTokenItem').className = hasServiceToken ? 'cookie-item' : 'cookie-item missing';
    document.getElementById('serviceTokenStatus').className = hasServiceToken ? 'cookie-status' : 'cookie-status missing';
    document.getElementById('serviceTokenStatus').textContent = hasServiceToken ? '✓ 找到' : '✗ 未找到';
    document.getElementById('serviceTokenValue').textContent = cookies.service_token || '未找到 - 请确认已登录';

    // user_id
    const hasUserId = !!cookies.user_id;
    document.getElementById('userIdItem').className = hasUserId ? 'cookie-item' : 'cookie-item missing';
    document.getElementById('userIdStatus').className = hasUserId ? 'cookie-status' : 'cookie-status missing';
    document.getElementById('userIdStatus').textContent = hasUserId ? '✓ 找到' : '✗ 未找到';
    document.getElementById('userIdValue').textContent = cookies.user_id || '未找到 - 请确认已登录';

    // ph
    const hasPh = !!cookies.ph;
    document.getElementById('phItem').className = hasPh ? 'cookie-item' : 'cookie-item missing';
    document.getElementById('phStatus').className = hasPh ? 'cookie-status' : 'cookie-status missing';
    document.getElementById('phStatus').textContent = hasPh ? '✓ 找到' : '✗ 未找到';
    document.getElementById('phValue').textContent = cookies.ph || '未找到 - 请确认已登录';

    // 生成配置预览
    const config = generateConfig([{
      id: 'account-1',
      ...cookies,
      active: true
    }]);
    configOutput.textContent = JSON.stringify(config, null, 2);
  }

  function generateConfig(accounts) {
    return {
      port: "7860",
      api_key: "sk-mimo",
      default_model: "mimo-v2.5-pro",
      accounts: accounts.map(acc => ({
        id: acc.id,
        service_token: acc.service_token || '',
        user_id: acc.user_id || '',
        ph: acc.ph || '',
        active: acc.active !== false
      }))
    };
  }

  function loadSavedAccounts() {
    chrome.storage.local.get(['accounts'], (data) => {
      savedAccounts = data.accounts || [];
      renderAccountsList();
    });
  }

  function renderAccountsList() {
    if (savedAccounts.length === 0) {
      accountsList.innerHTML = '<div style="color:#666; font-size:12px; text-align:center; padding:20px;">暂无保存的账号</div>';
      return;
    }

    accountsList.innerHTML = savedAccounts.map((acc, index) => `
      <div class="account-item">
        <span class="account-name">${acc.id}</span>
        <div class="account-actions">
          <button class="icon-btn" data-index="${index}" data-action="copy">复制</button>
          <button class="icon-btn delete" data-index="${index}" data-action="delete">删除</button>
        </div>
      </div>
    `).join('');

    // 绑定按钮事件
    accountsList.querySelectorAll('.icon-btn').forEach(btn => {
      btn.addEventListener('click', (e) => {
        const index = parseInt(e.target.dataset.index);
        const action = e.target.dataset.action;
        
        if (action === 'copy') {
          const config = generateConfig([savedAccounts[index]]);
          copyToClipboard(JSON.stringify(config, null, 2));
          showStatus('success', `✅ "${savedAccounts[index].id}" 配置已复制！`);
        } else if (action === 'delete') {
          if (confirm(`确定删除账号 "${savedAccounts[index].id}" 吗？`)) {
            savedAccounts.splice(index, 1);
            chrome.storage.local.set({ accounts: savedAccounts });
            loadSavedAccounts();
            showStatus('success', '✅ 账号已删除');
          }
        }
      });
    });
  }

  function copyToClipboard(text) {
    navigator.clipboard.writeText(text).catch(() => {
      // 降级方案
      const input = document.createElement('textarea');
      input.value = text;
      document.body.appendChild(input);
      input.select();
      document.execCommand('copy');
      document.body.removeChild(input);
    });
  }

  function showStatus(type, message) {
    statusEl.className = `status ${type} show`;
    statusEl.textContent = message;
    
    if (type !== 'loading') {
      setTimeout(() => {
        statusEl.className = 'status';
      }, 3000);
    }
  }
});