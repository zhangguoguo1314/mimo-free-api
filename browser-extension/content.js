// 内容脚本 - 在 MiMo 页面中运行
// 负责读取页面 Cookie 并提供给 popup

console.log('[MiMo Cookie Helper] 内容脚本已加载');

// 读取 document.cookie
function readDocumentCookie() {
  const cookies = document.cookie;
  console.log('[MiMo Cookie Helper] document.cookie:', cookies);
  
  const result = {};
  
  if (cookies) {
    const pairs = cookies.split(';');
    for (let pair of pairs) {
      const [name, ...valueParts] = pair.trim().split('=');
      const value = valueParts.join('=');
      
      if (name === 'serviceToken') {
        result.service_token = value;
        console.log('[MiMo Cookie Helper] 找到 serviceToken');
      }
      if (name === 'userId') {
        result.user_id = value;
        console.log('[MiMo Cookie Helper] 找到 userId:', value);
      }
      if (name === 'xiaomichatbot_ph') {
        result.ph = value;
        console.log('[MiMo Cookie Helper] 找到 ph');
      }
    }
  }
  
  return result;
}

// 监听来自 popup 的消息
chrome.runtime.onMessage.addListener((request, sender, sendResponse) => {
  console.log('[MiMo Cookie Helper] 收到消息:', request);
  
  if (request.action === 'getCookies') {
    const cookies = readDocumentCookie();
    console.log('[MiMo Cookie Helper] 返回 Cookie:', cookies);
    sendResponse({ cookies });
  }
  
  return true;
});

// 页面加载完成后也打印一次
window.addEventListener('load', () => {
  console.log('[MiMo Cookie Helper] 页面加载完成');
  readDocumentCookie();
});