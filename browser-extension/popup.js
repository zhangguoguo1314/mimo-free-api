document.addEventListener('DOMContentLoaded', () => {
  // DOM 元素
  const grabBtn = document.getElementById('grabBtn');
  const statusEl = document.getElementById('status');
  const resultBox = document.getElementById('resultBox');
  const quickCopy = document.getElementById('quickCopy');
  const configPreview = document.getElementById('configPreview');
  const copyConfigBtn = document.getElementById('copyConfigBtn');
  const accountList = document.getElementById('accountList');
  const accountCount = document.getElementById('accountCount');
  const saveRow = document.getElementById('saveRow');
  const accountName = document.getElementById('accountName');
  const saveBtn = document.getElementById('saveBtn');
  const exportAllBtn = document.getElementById('exportAllBtn');

  // Cookie 显示元素
  const serviceTokenVal = document.getElementById('serviceTokenVal');
  const userIdVal = document.getElementById('userIdVal');
  const phVal = document.getElementById('phVal');

  // 状态
  let currentCookies = {};
  let savedAccounts = [];

  // 初始化
  loadAccounts();

  // 抓取按钮
  grabBtn.addEventListener('click', async () => {
    showStatus('loading', '正在抓取 Cookie...');
    grabBtn.disabled = true;

    try {
      const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
      
      console.log('当前页面:', tab.url);

      // 获取所有 Cookie（不限制域名）
      const allCookies = await chrome.cookies.getAll({});
      console.log('所有 Cookie 数量:', allCookies.length);
      
      // 打印所有 Cookie 用于调试
      const debugInfo = allCookies.map(c => ({
        domain: c.domain,
        name: c.name,
        value: c.value.substring(0, 30) + '...'
      }));
      console.log('所有 Cookie:', debugInfo);

      currentCookies = {};

      // 方法1: 精确匹配域名
      const mimoCookies = await chrome.cookies.getAll({ domain: '.xiaomimimo.com' });
      console.log('xiaomimimo.com 域名 Cookie:', mimoCookies);

      // 方法2: 遍历所有 Cookie，模糊匹配
      for (const c of allCookies) {
        const domain = c.domain.toLowerCase();
        const name = c.name.toLowerCase();
        
        // 匹配 MiMo/Xiaomi 相关域名
        if (domain.includes('mimo') || domain.includes('xiaomi') || domain.includes('xiaomimimo')) {
          console.log(`检查 Cookie: ${c.name} @ ${c.domain}`);
          
          // serviceToken - 多种可能的名字
          if (name === 'servicetoken' || name === 'service_token' || name.includes('token')) {
            if (c.value && c.value.length > 10) {
              currentCookies.service_token = c.value;
              console.log('✓ 找到 service_token');
            }
          }
          
          // userId - 多种可能的名字
          if (name === 'userid' || name === 'user_id' || name === 'cuserid' || name === 'uid') {
            if (c.value && /^\d+$/.test(c.value)) {
              currentCookies.user_id = c.value;
              console.log('✓ 找到 user_id:', c.value);
            }
          }
          
          // ph - 多种可能的名字
          if (name === 'xiaomichatbot_ph' || name === 'ph' || name.includes('_ph') || name.includes('chatbot')) {
            if (c.value && c.value.includes('=')) {
              currentCookies.ph = c.value;
              console.log('✓ 找到 ph');
            }
          }
        }
      }

      // 方法3: 再次检查，更宽松的匹配
      if (!currentCookies.service_token) {
        for (const c of allCookies) {
          if (c.name.toLowerCase().includes('servicetoken') && c.value.length > 20) {
            currentCookies.service_token = c.value;
            console.log('✓ 宽松匹配找到 service_token');
            break;
          }
        }
      }

      console.log('最终抓取结果:', currentCookies);

      // 显示结果
      displayResults(currentCookies);

      const found = Object.keys(currentCookies).length;
      if (found === 3) {
        showStatus('success', '✓ 成功抓取全部 Cookie');
        saveRow.style.display = 'flex';
      } else if (found > 0) {
        showStatus('error', `⚠ 只找到 ${found}/3 个 Cookie`);
        saveRow.style.display = 'flex';
      } else {
        showStatus('error', '✗ 未找到 Cookie，请确认已登录');
        // 显示调试信息
        const allNames = allCookies.filter(c => 
          c.domain.includes('mimo') || c.domain.includes('xiaomi')
        ).map(c => c.name);
        console.log('MiMo/Xiaomi 相关 Cookie 名称:', allNames);
      }

    } catch (e) {
      console.error('抓取错误:', e);
      showStatus('error', '错误: ' + e.message);
    } finally {
      grabBtn.disabled = false;
    }
  });

  // 单个复制按钮
  document.querySelectorAll('.copy-btn').forEach(btn => {
    btn.addEventListener('click', () => {
      const field = btn.dataset.field;
      const value = currentCookies[field] || '';
      if (value) {
        copyToClipboard(value);
        btn.textContent = '已复制';
        btn.classList.add('copied');
        setTimeout(() => {
          btn.textContent = '复制';
          btn.classList.remove('copied');
        }, 1500);
      }
    });
  });

  // 一键复制完整配置
  copyConfigBtn.addEventListener('click', () => {
    const config = generateConfig([{
      id: accountName.value.trim() || '账号' + (savedAccounts.length + 1),
      ...currentCookies,
      active: true
    }]);
    copyToClipboard(JSON.stringify(config, null, 2));
    
    copyConfigBtn.textContent = '✓ 已复制到剪贴板';
    setTimeout(() => {
      copyConfigBtn.textContent = '一键复制完整配置';
    }, 2000);
  });

  // 保存账号
  saveBtn.addEventListener('click', () => {
    const name = accountName.value.trim() || '账号' + (savedAccounts.length + 1);
    
    const account = {
      id: name,
      service_token: currentCookies.service_token || '',
      user_id: currentCookies.user_id || '',
      ph: currentCookies.ph || '',
      active: true
    };

    savedAccounts.push(account);
    chrome.storage.local.set({ accounts: savedAccounts });
    
    loadAccounts();
    accountName.value = '';
    showStatus('success', `✓ 账号 "${name}" 已保存`);
  });

  // 导出全部
  exportAllBtn.addEventListener('click', () => {
    if (savedAccounts.length === 0) {
      showStatus('error', '没有保存的账号');
      return;
    }
    
    const config = generateConfig(savedAccounts);
    copyToClipboard(JSON.stringify(config, null, 2));
    showStatus('success', `✓ ${savedAccounts.length} 个账号配置已复制`);
  });

  // 显示抓取结果
  function displayResults(cookies) {
    resultBox.classList.add('show');
    quickCopy.classList.add('show');

    // service_token
    if (cookies.service_token) {
      serviceTokenVal.textContent = cookies.service_token.substring(0, 25) + '...';
      serviceTokenVal.className = 'cookie-value ok';
    } else {
      serviceTokenVal.textContent = '未找到';
      serviceTokenVal.className = 'cookie-value miss';
    }

    // user_id
    if (cookies.user_id) {
      userIdVal.textContent = cookies.user_id;
      userIdVal.className = 'cookie-value ok';
    } else {
      userIdVal.textContent = '未找到';
      userIdVal.className = 'cookie-value miss';
    }

    // ph
    if (cookies.ph) {
      phVal.textContent = cookies.ph.substring(0, 25) + '...';
      phVal.className = 'cookie-value ok';
    } else {
      phVal.textContent = '未找到';
      phVal.className = 'cookie-value miss';
    }

    // 更新配置预览
    const config = generateConfig([{
      id: '示例账号',
      ...cookies,
      active: true
    }]);
    configPreview.textContent = JSON.stringify(config, null, 2);
  }

  // 生成配置
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

  // 加载账号列表
  function loadAccounts() {
    chrome.storage.local.get(['accounts'], (data) => {
      savedAccounts = data.accounts || [];
      renderAccounts();
    });
  }

  // 渲染账号列表
  function renderAccounts() {
    accountCount.textContent = savedAccounts.length;
    
    if (savedAccounts.length === 0) {
      accountList.innerHTML = '<div style="color:#666;font-size:12px;text-align:center;padding:15px;">暂无保存的账号</div>';
      return;
    }

    accountList.innerHTML = savedAccounts.map((acc, idx) => `
      <div class="account-item">
        <span class="account-name">${acc.id}</span>
        <div class="account-btns">
          <button class="small-btn" data-idx="${idx}" data-action="copy">复制</button>
          <button class="small-btn del" data-idx="${idx}" data-action="del">删除</button>
        </div>
      </div>
    `).join('');

    // 绑定按钮事件
    accountList.querySelectorAll('.small-btn').forEach(btn => {
      btn.addEventListener('click', (e) => {
        const idx = parseInt(e.target.dataset.idx);
        const action = e.target.dataset.action;
        
        if (action === 'copy') {
          const config = generateConfig([savedAccounts[idx]]);
          copyToClipboard(JSON.stringify(config, null, 2));
          showStatus('success', `✓ "${savedAccounts[idx].id}" 配置已复制`);
        } else if (action === 'del') {
          savedAccounts.splice(idx, 1);
          chrome.storage.local.set({ accounts: savedAccounts });
          renderAccounts();
          showStatus('success', '✓ 账号已删除');
        }
      });
    });
  }

  // 复制到剪贴板
  function copyToClipboard(text) {
    navigator.clipboard.writeText(text).catch(() => {
      const input = document.createElement('textarea');
      input.value = text;
      document.body.appendChild(input);
      input.select();
      document.execCommand('copy');
      document.body.removeChild(input);
    });
  }

  // 显示状态
  function showStatus(type, msg) {
    statusEl.className = 'status ' + type + ' show';
    statusEl.textContent = msg;
    
    if (type !== 'loading') {
      setTimeout(() => {
        statusEl.className = 'status';
      }, 2500);
    }
  }
});