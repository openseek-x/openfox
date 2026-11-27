// 使用工具栏点击事件保留当前标签页的临时授权，侧栏内的勾选决定是否读取网页。
chrome.action.onClicked.addListener((tab) => {
  chrome.sidePanel.open({ windowId: tab.windowId });
});
