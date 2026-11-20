# 内置浏览器页面协议

日期：2026-11-20

模型通过现有 `web` 工具操作应用自己的浏览器窗口。脚本由 Go 生成。模型不能提交任意 JavaScript，也不能操作用户的 Chrome。文件选择框不在协议里。

## 动作

`snapshot`、`open`、`back`、`forward`、`reload`、`move`、`click`、`double_click`、`right_click`、`down`、`up`、`scroll`、`type`、`key`、`wait`。

`read` 与 `snapshot` 相同。有控件编号时用 `ref`。没有 DOM 的区域用 `x`、`y`。编号只对刚刚那次观察有效。

`down` 之后可以 `move` 和 `snapshot`。下一次点击、输入或打开页面会先松开指针，并在观察里写上「已松开」。

## 观察

每次动作结束交回 JSON：网址、标题、正文、最多 80 个控件。控件带 `ref`、`role`、`name`。同时用 `Window.CapturePage` 取当前视口 PNG。

PNG 不写进工具文本。循环把它存成图片文件，挂在这条工具消息上。历史里只保留最新一张页面图，更早的页面图丢掉，文字观察留下。

## 失败

编号不存在、地址不是 http(s)、打开超过 20 秒、`wait` 超过时限，都返回错误和当前观察。`wait` 默认 15 秒，最长 20 秒。
