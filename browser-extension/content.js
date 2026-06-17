// Content Script - 持续运行在页面中
// 用于拦截fetch请求获取serviceToken

let capturedServiceToken = '';
let capturedPh = '';
let capturedUserId = '';

// 拦截fetch获取token
const originalFetch = window.fetch;
window.fetch = function(...args) {
  const [url, options] = args;

  // 检查是否是MiMo API请求
  if (typeof url === 'string' && url.includes('xiaomimimo.com')) {
    // 尝试从请求头中获取cookie信息
    if (options && options.headers) {
      const headers = options.headers;
      if (headers instanceof Headers) {
        const cookie = headers.get('cookie') || headers.get('Cookie');
        if (cookie) {
          parseAndStoreCookies(cookie);
        }
      }
    }
  }

  return originalFetch.apply(this, args).then(response => {
    // 克隆响应以便读取
    const clone = response.clone();

    // 如果是登录或用户信息请求，尝试提取数据
    if (typeof url === 'string') {
      if (url.includes('/user') || url.includes('/login') || url.includes('/chat')) {
        clone.json().then(data => {
          if (data && data.data) {
            // 存储到localStorage和chrome storage
            chrome.storage.local.set({
              mimoApiResponse: data.data,
              capturedAt: new Date().toLocaleString('zh-CN')
            });
          }
        }).catch(() => {});
      }
    }

    return response;
  });
};

// 拦截XHR
const originalXHROpen = XMLHttpRequest.prototype.open;
const originalXHRSend = XMLHttpRequest.prototype.send;

XMLHttpRequest.prototype.open = function(method, url, ...rest) {
  this._url = url;
  return originalXHROpen.call(this, method, url, ...rest);
};

XMLHttpRequest.prototype.send = function(body) {
  this.addEventListener('load', function() {
    if (this._url && this._url.includes('xiaomimimo.com')) {
      try {
        const data = JSON.parse(this.responseText);
        if (data && data.data) {
          chrome.storage.local.set({
            mimoApiResponse: data.data,
            capturedAt: new Date().toLocaleString('zh-CN')
          });
        }
      } catch (e) {}
    }
  });
  return originalXHRSend.call(this, body);
};

// 解析cookie字符串
function parseAndStoreCookies(cookieStr) {
  const serviceTokenMatch = cookieStr.match(/serviceToken=([^;]+)/);
  const userIdMatch = cookieStr.match(/userId=([^;]+)/);
  const phMatch = cookieStr.match(/xiaomichatbot_ph=([^;]+)/);

  const cookies = {};
  if (serviceTokenMatch) cookies.serviceToken = decodeURIComponent(serviceTokenMatch[1]);
  if (userIdMatch) cookies.userId = userIdMatch[1];
  if (phMatch) cookies.ph = decodeURIComponent(phMatch[1]);

  if (Object.keys(cookies).length > 0) {
    chrome.storage.local.set({
      mimoCookies: cookies,
      capturedAt: new Date().toLocaleString('zh-CN')
    });
  }
}

// 去除值两端的引号
function stripQuotes(value) {
  if (!value) return value;
  value = value.trim();
  if ((value.startsWith('"') && value.endsWith('"')) ||
      (value.startsWith("'") && value.endsWith("'"))) {
    return value.slice(1, -1);
  }
  return value;
}

// 监听来自background的消息
chrome.runtime.onMessage.addListener((request, sender, sendResponse) => {
  if (request.action === 'GRAB_COOKIES') {
    // 从页面获取所有可能的数据
    const result = {
      serviceToken: capturedServiceToken,
      ph: capturedPh,
      userId: capturedUserId,
      localStorage: {
        ph: localStorage.getItem('xiaomichatbot_ph') || '',
        userId: localStorage.getItem('userId') || '',
        slh: localStorage.getItem('xiaomichatbot_slh') || ''
      },
      url: location.href
    };

    // 尝试从document.cookie获取（虽然HttpOnly的拿不到）
    result.documentCookie = document.cookie;

    // 尝试从页面脚本获取
    const scripts = document.querySelectorAll('script');
    for (const script of scripts) {
      const text = script.textContent || '';
      if (!result.ph) {
        const phMatch = text.match(/xiaomichatbot_ph["']?\s*[:=]\s*["']([^"']+)/);
        if (phMatch) result.ph = phMatch[1];
      }
      if (!result.userId) {
        const uidMatch = text.match(/userId["']?\s*[:=]\s*["']?(\d+)/);
        if (uidMatch) result.userId = uidMatch[1];
      }
    }

    // 填充缺失的值
    if (!result.ph && result.localStorage.ph) result.ph = result.localStorage.ph;
    if (!result.userId && result.localStorage.userId) result.userId = result.localStorage.userId;

    // 去除所有值的引号
    result.serviceToken = stripQuotes(result.serviceToken);
    result.ph = stripQuotes(result.ph);
    result.userId = stripQuotes(result.userId);
    if (result.localStorage.ph) result.localStorage.ph = stripQuotes(result.localStorage.ph);
    if (result.localStorage.userId) result.localStorage.userId = stripQuotes(result.localStorage.userId);

    sendResponse(result);
    return true;
  }
});

console.log('🔍 MiMo Cookie内容脚本已注入');
