// 内容脚本 - 在 MiMo 页面中运行
// 可以在这里添加页面级别的功能，比如自动检测登录状态

console.log('[MiMo Cookie Helper] 已加载');

// 监听来自 popup 的消息
chrome.runtime.onMessage.addListener((request, sender, sendResponse) => {
  if (request.action === 'checkLogin') {
    // 检查页面是否已登录
    const isLoggedIn = document.querySelector('.chat-container') !== null || 
                       document.querySelector('[data-testid="chat-input"]') !== null;
    sendResponse({ isLoggedIn });
  }
  return true;
});