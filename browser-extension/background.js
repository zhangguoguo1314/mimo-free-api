// 去除值两端的引号
function stripQuotes(value) {
  if (!value || typeof value !== 'string') return value;
  value = value.trim();
  if ((value.startsWith('"') && value.endsWith('"')) ||
      (value.startsWith("'") && value.endsWith("'"))) {
    return value.slice(1, -1);
  }
  return value;
}

// 监听请求头，提取Cookie
chrome.webRequest.onBeforeSendHeaders.addListener(
  (details) => {
    const cookies = {};

    // 从请求头中提取Cookie
    for (const header of details.requestHeaders) {
      if (header.name.toLowerCase() === 'cookie') {
        const cookieStr = header.value;

        // 解析各个cookie
        const serviceTokenMatch = cookieStr.match(/serviceToken=([^;]+)/);
        const userIdMatch = cookieStr.match(/userId=([^;]+)/);
        const phMatch = cookieStr.match(/xiaomichatbot_ph=([^;]+)/);

        if (serviceTokenMatch) cookies.serviceToken = stripQuotes(decodeURIComponent(serviceTokenMatch[1]));
        if (userIdMatch) cookies.userId = stripQuotes(userIdMatch[1]);
        if (phMatch) cookies.ph = stripQuotes(decodeURIComponent(phMatch[1]));

        // 保存到storage
        if (cookies.serviceToken && cookies.userId && cookies.ph) {
          chrome.storage.local.set({
            mimoCookies: cookies,
            capturedAt: new Date().toLocaleString('zh-CN')
          });
          console.log('✅ MiMo Cookie已捕获:', cookies);
        }
        break;
      }
    }

    return { requestHeaders: details.requestHeaders };
  },
  {
    urls: [
      "https://aistudio.xiaomimimo.com/*",
      "https://*.xiaomimimo.com/*"
    ]
  },
  ["requestHeaders", "extraHeaders"]
);

console.log('🚀 MiMo Cookie抓取器后台脚本已启动');
