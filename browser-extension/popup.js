// 从storage读取捕获的cookie
async function getCookies() {
  const result = await chrome.storage.local.get(['mimoCookies', 'capturedAt']);
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

  // 加载已保存的cookie
  async function loadCookies() {
    const { mimoCookies, capturedAt } = await getCookies();

    if (mimoCookies) {
      uidEl.textContent = mimoCookies.userId || '未捕获';
      tokenEl.textContent = mimoCookies.serviceToken ? mimoCookies.serviceToken.substring(0, 30) + '...' : '未捕获';
      phEl.textContent = mimoCookies.ph ? mimoCookies.ph.substring(0, 30) + '...' : '未捕获';
      timeEl.textContent = '捕获时间: ' + (capturedAt || '未知');
      showStatus('✅ 已获取Cookie');
    } else {
      uidEl.textContent = '-';
      tokenEl.textContent = '-';
      phEl.textContent = '-';
      timeEl.textContent = '请在 MiMo 网站发送消息以捕获Cookie';
      showStatus('⏳ 等待捕获...', true);
    }
  }

  // 刷新按钮
  refreshBtn.addEventListener('click', () => {
    loadCookies();
  });

  // 去除引号
  function stripQuotes(value) {
    if (!value || typeof value !== 'string') return value;
    value = value.trim();
    if ((value.startsWith('"') && value.endsWith('"')) ||
        (value.startsWith("'") && value.endsWith("'"))) {
      return value.slice(1, -1);
    }
    return value;
  }

  // 复制配置按钮
  copyBtn.addEventListener('click', async () => {
    const { mimoCookies } = await getCookies();

    if (!mimoCookies || !mimoCookies.serviceToken) {
      showStatus('❌ 没有可复制的数据', true);
      return;
    }

    const config = {
      accounts: [{
        id: `account-${Date.now()}`,
        service_token: stripQuotes(mimoCookies.serviceToken),
        user_id: stripQuotes(mimoCookies.userId),
        ph: stripQuotes(mimoCookies.ph),
        active: true
      }]
    };

    const jsonStr = JSON.stringify(config, null, 2);

    try {
      await navigator.clipboard.writeText(jsonStr);
      showStatus('✅ 配置已复制到剪贴板');
      copyBtn.textContent = '已复制!';
      setTimeout(() => {
        copyBtn.textContent = '复制配置';
      }, 2000);
    } catch (err) {
      showStatus('❌ 复制失败', true);
    }
  });

  // 初始加载
  loadCookies();
});
