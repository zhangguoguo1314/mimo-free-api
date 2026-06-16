// Background Service Worker
// 用于拦截网络请求并提取 Cookie

let capturedCookies = {};

// 监听请求发送前的事件
chrome.webRequest.onBeforeSendHeaders.addListener(
  (details) => {
    // 只处理 MiMo 的 chat 请求
    if (details.url.includes('xiaomimimo.com') && details.url.includes('chat')) {
      console.log('[Background] 拦截到请求:', details.url);
      
      // 查找 Cookie 请求头
      const cookieHeader = details.requestHeaders.find(
        h => h.name.toLowerCase() === 'cookie'
      );
      
      if (cookieHeader && cookieHeader.value) {
        console.log('[Background] 找到 Cookie:', cookieHeader.value.substring(0, 100) + '...');
        
        // 解析 Cookie
        const cookies = parseCookieString(cookieHeader.value);
        
        // 保存到存储
        if (cookies.serviceToken || cookies.userId || cookies.ph) {
          capturedCookies = { ...capturedCookies, ...cookies };
          chrome.storage.local.set({ capturedCookies });
          console.log('[Background] 已保存 Cookie:', capturedCookies);
        }
      }
    }
  },
  {
    urls: ["https://*.xiaomimimo.com/*"]
  },
  ["requestHeaders"]
);

// 解析 Cookie 字符串
function parseCookieString(cookieStr) {
  const result = {};
  const pairs = cookieStr.split(';');
  
  for (let pair of pairs) {
    const [name, ...valueParts] = pair.trim().split('=');
    const value = valueParts.join('=');
    
    if (name === 'serviceToken') {
      result.service_token = value;
    }
    if (name === 'userId') {
      result.user_id = value;
    }
    if (name === 'xiaomichatbot_ph') {
      result.ph = value;
    }
  }
  
  return result;
}

// 监听来自 popup 的消息
chrome.runtime.onMessage.addListener((request, sender, sendResponse) => {
  if (request.action === 'getCapturedCookies') {
    sendResponse({ cookies: capturedCookies });
  }
  if (request.action === 'clearCapturedCookies') {
    capturedCookies = {};
    chrome.storage.local.remove('capturedCookies');
    sendResponse({ success: true });
  }
  return true;
});

console.log('[Background] Service Worker 已启动');