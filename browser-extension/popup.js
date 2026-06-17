document.addEventListener('DOMContentLoaded', () => {
  const grabBtn = document.getElementById('grabBtn');
  const statusEl = document.getElementById('status');
  const resultBox = document.getElementById('resultBox');
  const configSection = document.getElementById('configSection');
  const configOutput = document.getElementById('configOutput');
  const copyConfigBtn = document.getElementById('copyConfigBtn');
  const serviceTokenVal = document.getElementById('serviceTokenVal');
  const userIdVal = document.getElementById('userIdVal');
  const phVal = document.getElementById('phVal');

  let currentCookies = {};

  grabBtn.addEventListener('click', async () => {
    showStatus('loading', '正在抓取...');
    grabBtn.disabled = true;

    try {
      const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
      
      if (!tab.url.includes('xiaomimimo.com')) {
        showStatus('error', '请先打开 MiMo 网页');
        grabBtn.disabled = false;
        return;
      }

      // 注入脚本读取 document.cookie
      const results = await chrome.scripting.executeScript({
        target: { tabId: tab.id },
        func: () => {
          const cookies = document.cookie;
          const result = {};
          if (cookies) {
            cookies.split(';').forEach(pair => {
              const [name, ...valueParts] = pair.trim().split('=');
              const value = valueParts.join('=');
              if (name === 'serviceToken') result.service_token = value;
              if (name === 'userId') result.user_id = value;
              if (name === 'xiaomichatbot_ph') result.ph = value;
            });
          }
          return result;
        }
      });

      currentCookies = results[0]?.result || {};
      displayResults(currentCookies);

      const found = Object.keys(currentCookies).length;
      if (found === 3) {
        showStatus('success', '✓ 抓取成功');
      } else if (found > 0) {
        showStatus('error', `⚠ 只找到 ${found}/3，请发送消息后再试`);
      } else {
        showStatus('error', '✗ 未找到，请发送消息后再试');
      }

    } catch (e) {
      showStatus('error', '错误: ' + e.message);
    } finally {
      grabBtn.disabled = false;
    }
  });

  function displayResults(cookies) {
    resultBox.classList.add('show');
    configSection.classList.add('show');

    serviceTokenVal.textContent = cookies.service_token ? cookies.service_token.substring(0, 25) + '...' : '未找到';
    serviceTokenVal.className = 'cookie-value ' + (cookies.service_token ? 'ok' : 'miss');

    userIdVal.textContent = cookies.user_id || '未找到';
    userIdVal.className = 'cookie-value ' + (cookies.user_id ? 'ok' : 'miss');

    phVal.textContent = cookies.ph ? cookies.ph.substring(0, 25) + '...' : '未找到';
    phVal.className = 'cookie-value ' + (cookies.ph ? 'ok' : 'miss');

    const config = {
      accounts: [{
        id: 'my-account',
        service_token: cookies.service_token || '',
        user_id: cookies.user_id || '',
        ph: cookies.ph || '',
        active: true
      }]
    };
    configOutput.textContent = JSON.stringify(config, null, 2);
  }

  // 复制按钮
  document.querySelectorAll('.copy-btn').forEach(btn => {
    btn.addEventListener('click', () => {
      const field = btn.dataset.field;
      const value = currentCookies[field] || '';
      if (value) {
        navigator.clipboard.writeText(value);
        btn.textContent = '已复制';
        setTimeout(() => btn.textContent = '复制', 1500);
      }
    });
  });

  // 复制完整配置
  copyConfigBtn.addEventListener('click', () => {
    navigator.clipboard.writeText(configOutput.textContent);
    copyConfigBtn.textContent = '✓ 已复制';
    setTimeout(() => copyConfigBtn.textContent = '一键复制', 2000);
  });

  function showStatus(type, msg) {
    statusEl.className = 'status ' + type + ' show';
    statusEl.textContent = msg;
    if (type !== 'loading') {
      setTimeout(() => statusEl.className = 'status', 3000);
    }
  }
});