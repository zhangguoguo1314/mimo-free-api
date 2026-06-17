// 点击扩展图标时执行
chrome.action.onClicked.addListener(async (tab) => {
  // 确保在正确的域名
  if (!tab.url.includes('xiaomimimo.com')) {
    chrome.scripting.executeScript({
      target: { tabId: tab.id },
      func: () => alert('请在 MiMo 网站 (aistudio.xiaomimimo.com) 上使用此扩展')
    });
    return;
  }

  try {
    // 向content script发送消息获取cookie
    const response = await chrome.tabs.sendMessage(tab.id, { action: 'GRAB_COOKIES' });

    if (response) {
      // 保存到storage
      const cookies = {
        serviceToken: response.serviceToken || '',
        ph: response.ph || response.localStorage?.ph || '',
        userId: response.userId || response.localStorage?.userId || ''
      };

      await chrome.storage.local.set({
        mimoCookies: cookies,
        capturedAt: new Date().toLocaleString('zh-CN'),
        rawResponse: response
      });

      // 在页面显示提示
      chrome.scripting.executeScript({
        target: { tabId: tab.id },
        func: (data) => {
          const div = document.createElement('div');
          div.style.cssText = `
            position: fixed;
            top: 20px;
            right: 20px;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            color: white;
            padding: 16px 24px;
            border-radius: 12px;
            box-shadow: 0 4px 20px rgba(0,0,0,0.3);
            z-index: 999999;
            font-family: -apple-system, BlinkMacSystemFont, sans-serif;
            font-size: 14px;
            max-width: 320px;
            line-height: 1.5;
          `;

          const hasData = data.ph && data.userId;
          div.innerHTML = `
            <div style="font-weight: bold; margin-bottom: 8px; font-size: 16px;">
              ${hasData ? '✅ 抓取成功！' : '⚠️ 部分数据缺失'}
            </div>
            <div style="margin-bottom: 4px;"><strong>UserID:</strong> ${data.userId || '未找到'}</div>
            <div style="margin-bottom: 4px;"><strong>PH:</strong> ${data.ph ? data.ph.substring(0, 25) + '...' : '未找到'}</div>
            <div style="margin-bottom: 4px;"><strong>Token:</strong> ${data.serviceToken ? '已获取 ✓' : '未找到 ✗'}</div>
            <div style="margin-top: 10px; padding-top: 10px; border-top: 1px solid rgba(255,255,255,0.3); font-size: 12px;">
              ${hasData ? '点击扩展图标打开弹窗，复制完整配置' : '请先发送一条消息，然后再点击抓取'}
            </div>
          `;

          document.body.appendChild(div);

          // 添加动画
          div.animate([
            { transform: 'translateX(100%)', opacity: 0 },
            { transform: 'translateX(0)', opacity: 1 }
          ], {
            duration: 300,
            easing: 'ease-out'
          });

          setTimeout(() => {
            div.animate([
              { transform: 'translateX(0)', opacity: 1 },
              { transform: 'translateX(100%)', opacity: 0 }
            ], {
              duration: 300,
              easing: 'ease-in'
            }).onfinish = () => div.remove();
          }, 4000);
        },
        args: [cookies]
      });
    }
  } catch (error) {
    // Content script可能没有注入，尝试注入
    try {
      await chrome.scripting.executeScript({
        target: { tabId: tab.id },
        files: ['content.js']
      });

      // 等待一下再发送消息
      setTimeout(async () => {
        try {
          const response = await chrome.tabs.sendMessage(tab.id, { action: 'GRAB_COOKIES' });
          if (response) {
            const cookies = {
              serviceToken: response.serviceToken || '',
              ph: response.ph || response.localStorage?.ph || '',
              userId: response.userId || response.localStorage?.userId || ''
            };

            await chrome.storage.local.set({
              mimoCookies: cookies,
              capturedAt: new Date().toLocaleString('zh-CN')
            });

            // 刷新弹窗
            chrome.action.openPopup();
          }
        } catch (e) {
          console.error('Failed to get cookies:', e);
        }
      }, 500);
    } catch (e) {
      console.error('Failed to inject content script:', e);
    }
  }
});

// 监听来自content script的消息（自动捕获）
chrome.runtime.onMessage.addListener((request, sender, sendResponse) => {
  if (request.type === 'COOKIES_CAPTURED') {
    chrome.storage.local.set({
      mimoCookies: request.data,
      capturedAt: new Date().toLocaleString('zh-CN')
    });
  }
});

console.log('🚀 MiMo Cookie抓取器后台脚本已启动');
