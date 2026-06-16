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
      
      console.log('[Popup] 当前页面:', tab.url);

      if (!tab.url.includes('xiaomimimo.com')) {
        showStatus('error', '请先打开 MiMo 网页 (aistudio.xiaomimimo.com)');
        grabBtn.disabled = false;
        return;
      }

      currentCookies = {};

      // 方法1: 从 background 获取拦截到的 Cookie（最可靠）
      console.log('[Popup] 从 background 获取拦截的 Cookie...');
      try {
        const response = await chrome.runtime.sendMessage({ action: 'getCapturedCookies' });
        console.log('[Popup] Background 返回:', response);
        if (response && response.cookies) {
          currentCookies = { ...currentCookies, ...response.cookies };
        }
      } catch (e) {
        console.log('[Popup] Background 获取失败:', e.message);
      }

      // 方法2: 使用 chrome.cookies API 获取
      if (!currentCookies.service_token || !currentCookies.user_id || !currentCookies.ph) {
        console.log('[Popup] 尝试通过 chrome.cookies API 获取...');
        try {
          const apiCookies = await getCookiesFromAPI(tab.url);
          console.log('[Popup] API 返回:', apiCookies);
          currentCookies = { ...currentCookies, ...apiCookies };
        } catch (e) {
          console.log('[Popup] API 获取失败:', e.message);
        }
      }

      // 方法3: 动态注入脚本获取 document.cookie
      if (!currentCookies.user_id || !currentCookies.ph) {
        console.log('[Popup] 尝试动态注入脚本获取 Cookie...');
        try {
          const results = await chrome.scripting.executeScript({
            target: { tabId: tab.id },
            func: () => {
              const cookies = document.cookie;
              console.log('[Injected] document.cookie:', cookies);
              
              const result = {};
              if (cookies) {
                const pairs = cookies.split(';');
                for (let pair of pairs) {
                  const [name, ...valueParts] = pair.trim().split('=');
                  const value = valueParts.join('=');
                  
                  if (name === 'userId') {
                    result.user_id = value;
                  }
                  if (name === 'xiaomichatbot_ph') {
                    result.ph = value;
                  }
                }
              }
              return result;
            }
          });
          
          console.log('[Popup] 注入脚本返回:', results);
          if (results && results[0] && results[0].result) {
            currentCookies = { ...currentCookies, ...results[0].result };
          }
        } catch (e) {
          console.log('[Popup] 注入脚本失败:', e.message);
        }
      }

      console.log('[Popup] 最终 Cookie:', currentCookies);

      // 显示结果
      displayResults(currentCookies);

      const found = Object.values(currentCookies).filter(v => v && v.length > 0).length;
      if (found === 3) {
        showStatus('success', '✓ 成功抓取全部 Cookie');
        saveRow.style.display = 'flex';
      } else if (found > 0) {
        showStatus('error', `⚠ 只找到 ${found}/3 个 Cookie，请在 MiMo 中发送一条消息后再试`);
        saveRow.style.display = 'flex';
      } else {
        showStatus('error', '✗ 未找到 Cookie，请在 MiMo 中发送一条消息后再试');
      }

    } catch (e) {
      console.error('[Popup] 抓取错误:', e);
      showStatus('error', '错误: ' + e.message);
    } finally {
      grabBtn.disabled = false;
    }
  });

  // 通过 chrome.cookies API 获取
  async function getCookiesFromAPI(url) {
    const result = {};
    
    const urlObj = new URL(url);
    const hostname = urlObj.hostname;
    console.log('[Popup] 目标域名:', hostname);
    
    // 获取当前域名
    const cookies1 = await chrome.cookies.getAll({ domain: hostname });
    console.log('[Popup] 当前域名 Cookie 数量:', cookies1.length);
    extractCookies(cookies1, result);
    
    // 获取父域名
    if (!result.service_token || !result.user_id || !result.ph) {
      const cookies2 = await chrome.cookies.getAll({ domain: '.xiaomimimo.com' });
      console.log('[Popup] 父域名 Cookie 数量:', cookies2.length);
      extractCookies(cookies2, result);
    }
    
    return result;
  }

  function extractCookies(cookies, result) {
    for (const c of cookies) {
      if (c.name === 'serviceToken' || c.name === 'service_token') {
        result.service_token = c.value;
      }
      if (c.name === 'userId') {
        result.user_id = c.value;
      }
      if (c.name === 'xiaomichatbot_ph') {
        result.ph = c.value;
      }
    }
  }

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

    if (cookies.service_token) {
      serviceTokenVal.textContent = cookies.service_token.substring(0, 25) + '...';
      serviceTokenVal.className = 'cookie-value ok';
    } else {
      serviceTokenVal.textContent = '未找到 - 请发送消息后再试';
      serviceTokenVal.className = 'cookie-value miss';
    }

    if (cookies.user_id) {
      userIdVal.textContent = cookies.user_id;
      userIdVal.className = 'cookie-value ok';
    } else {
      userIdVal.textContent = '未找到';
      userIdVal.className = 'cookie-value miss';
    }

    if (cookies.ph) {
      phVal.textContent = cookies.ph.substring(0, 25) + '...';
      phVal.className = 'cookie-value ok';
    } else {
      phVal.textContent = '未找到';
      phVal.className = 'cookie-value miss';
    }

    const config = generateConfig([{
      id: '示例账号',
      ...cookies,
      active: true
    }]);
    configPreview.textContent = JSON.stringify(config, null, 2);
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

  function loadAccounts() {
    chrome.storage.local.get(['accounts'], (data) => {
      savedAccounts = data.accounts || [];
      renderAccounts();
    });
  }

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