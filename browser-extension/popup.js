// 从storage读取捕获的cookie
async function getCookies() {
  const result = await chrome.storage.local.get(['mimoCookies', 'capturedAt', 'rawResponse']);
  return result;
}

// 显示状态
function showStatus(message, isError = false) {
  const statusEl = document.getElementById('status');
  statusEl.textContent = message;
  statusEl.className = 'status ' + (isError ? 'error' : 'success');
}

// 更新UI
document.addEventListener('DOMContentLoaded', async () => {
  const uidEl = document.getElementById('uid');
  const tokenEl = document.getElementById('token');
  const phEl = document.getElementById('ph');
  const timeEl = document.getElementById('captureTime');
  const copyBtn = document.getElementById('copyBtn');
  const refreshBtn = document.getElementById('refreshBtn');
  const grabBtn = document.getElementById('grabBtn');

  // 加载已保存的cookie
  async function loadCookies() {
    const { mimoCookies, capturedAt, rawResponse } = await getCookies();

    if (mimoCookies && (mimoCookies.ph || mimoCookies.userId)) {
      uidEl.textContent = mimoCookies.userId || '未捕获';
      tokenEl.textContent = mimoCookies.serviceToken ? '✓ 已获取 (' + mimoCookies.serviceToken.substring(0, 20) + '...)' : '未捕获';
      phEl.textContent = mimoCookies.ph ? mimoCookies.ph.substring(0, 30) + '...' : '未捕获';
      timeEl.textContent = '捕获时间: ' + (capturedAt || '未知');

      const isComplete = mimoCookies.ph && mimoCookies.userId;
      showStatus(isComplete ? '✅ 已获取完整Cookie' : '⚠️ 部分数据缺失', !isComplete);
    } else {
      uidEl.textContent = '-';
      tokenEl.textContent = '-';
      phEl.textContent = '-';
      timeEl.textContent = '暂无数据';
      showStatus('⏳ 点击"立即抓取"按钮获取Cookie', true);
    }
  }

  // 立即抓取按钮
  grabBtn.addEventListener('click', async () => {
    try {
      showStatus('🔄 正在抓取...', true);

      // 获取当前标签页
      const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });

      if (!tab.url.includes('xiaomimimo.com')) {
        showStatus('❌ 请在 MiMo 网站使用', true);
        return;
      }

      // 发送消息给content script
      const response = await chrome.tabs.sendMessage(tab.id, { action: 'GRAB_COOKIES' });

      if (response) {
        // 辅助函数：去除引号
        const stripQuotes = (v) => {
          if (!v) return v;
          v = v.trim();
          if ((v.startsWith('"') && v.endsWith('"')) || (v.startsWith("'") && v.endsWith("'"))) {
            return v.slice(1, -1);
          }
          return v;
        };

        const cookies = {
          serviceToken: stripQuotes(response.serviceToken) || '',
          ph: stripQuotes(response.ph || response.localStorage?.ph) || '',
          userId: stripQuotes(response.userId || response.localStorage?.userId) || ''
        };

        await chrome.storage.local.set({
          mimoCookies: cookies,
          capturedAt: new Date().toLocaleString('zh-CN'),
          rawResponse: response
        });

        loadCookies();
      }
    } catch (error) {
      showStatus('❌ 抓取失败，请刷新页面重试', true);
      console.error(error);
    }
  });

  // 刷新按钮
  refreshBtn.addEventListener('click', () => {
    loadCookies();
  });

  // 复制配置按钮
  copyBtn.addEventListener('click', async () => {
    const { mimoCookies } = await getCookies();

    if (!mimoCookies || !mimoCookies.ph) {
      showStatus('❌ 没有可复制的数据，请先抓取', true);
      return;
    }

    const config = {
      accounts: [{
        id: `account-${Date.now()}`,
        service_token: mimoCookies.serviceToken || '',
        user_id: mimoCookies.userId || '',
        ph: mimoCookies.ph || '',
        active: true
      }]
    };

    const jsonStr = JSON.stringify(config, null, 2);

    try {
      await navigator.clipboard.writeText(jsonStr);
      showStatus('✅ 配置已复制到剪贴板');
      copyBtn.textContent = '✓ 已复制!';
      setTimeout(() => {
        copyBtn.textContent = '📋 复制配置';
      }, 2000);
    } catch (err) {
      showStatus('❌ 复制失败', true);
    }
  });

  // 初始加载
  loadCookies();
});
