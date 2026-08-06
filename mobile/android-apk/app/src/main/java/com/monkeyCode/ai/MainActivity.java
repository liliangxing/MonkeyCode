package com.monkeyCode.ai;

import android.app.Activity;
import android.content.ContentValues;
import android.content.pm.PackageManager;
import android.graphics.Color;
import android.graphics.drawable.GradientDrawable;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.os.Environment;
import android.provider.MediaStore;
import android.util.TypedValue;
import android.view.Gravity;
import android.view.View;
import android.view.Window;
import android.view.WindowManager;
import android.webkit.CookieManager;
import android.webkit.JavascriptInterface;
import android.webkit.WebChromeClient;
import android.webkit.WebResourceError;
import android.webkit.WebResourceRequest;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.FrameLayout;
import android.widget.LinearLayout;
import android.widget.TextView;
import android.widget.Toast;

import org.json.JSONObject;

import java.io.File;
import java.io.FileOutputStream;
import java.io.OutputStream;
import java.nio.charset.StandardCharsets;
import java.text.SimpleDateFormat;
import java.util.Date;
import java.util.Iterator;
import java.util.Locale;

public class MainActivity extends Activity {

    private WebView webView;
    private View errorView;
    private View fabButton;
    private boolean isExporting = false;

    private static final String DEFAULT_SERVER_URL = "https://monkeycode-ai.com/console/";

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);

        requestWindowFeature(Window.FEATURE_NO_TITLE);
        getWindow().setFlags(
            WindowManager.LayoutParams.FLAG_HARDWARE_ACCELERATED,
            WindowManager.LayoutParams.FLAG_HARDWARE_ACCELERATED
        );

        FrameLayout root = new FrameLayout(this);
        root.setLayoutParams(new FrameLayout.LayoutParams(
            FrameLayout.LayoutParams.MATCH_PARENT,
            FrameLayout.LayoutParams.MATCH_PARENT
        ));

        webView = new WebView(this);
        webView.setLayoutParams(new FrameLayout.LayoutParams(
            FrameLayout.LayoutParams.MATCH_PARENT,
            FrameLayout.LayoutParams.MATCH_PARENT
        ));
        root.addView(webView);

        errorView = createErrorView();
        errorView.setVisibility(View.GONE);
        root.addView(errorView);

        fabButton = createFabButton();
        root.addView(fabButton);

        setContentView(root);

        configureWebView();
        loadApp();
    }

    private void configureWebView() {
        WebSettings settings = webView.getSettings();
        settings.setJavaScriptEnabled(true);
        settings.setDomStorageEnabled(true);
        settings.setDatabaseEnabled(true);
        settings.setAllowFileAccess(true);
        settings.setAllowContentAccess(true);
        settings.setLoadWithOverviewMode(true);
        settings.setUseWideViewPort(true);
        settings.setSupportZoom(false);
        settings.setBuiltInZoomControls(false);
        settings.setCacheMode(WebSettings.LOAD_DEFAULT);
        settings.setMixedContentMode(WebSettings.MIXED_CONTENT_ALWAYS_ALLOW);
        settings.setJavaScriptCanOpenWindowsAutomatically(true);
        settings.setMediaPlaybackRequiresUserGesture(false);

        WebView.setWebContentsDebuggingEnabled(true);

        CookieManager.getInstance().setAcceptCookie(true);
        CookieManager.getInstance().setAcceptThirdPartyCookies(webView, true);

        webView.addJavascriptInterface(new MonkeyCodeBridge(), "MonkeyCodeBridge");

        webView.setWebViewClient(new WebViewClient() {
            @Override
            public void onPageFinished(WebView view, String url) {
                super.onPageFinished(view, url);
                view.evaluateJavascript(buildCaptureScript(), null);
            }

            @Override
            public void onReceivedError(WebView view, WebResourceRequest request, WebResourceError error) {
                super.onReceivedError(view, request, error);
                if (request.isForMainFrame()) {
                    errorView.setVisibility(View.VISIBLE);
                }
            }
        });

        webView.setWebChromeClient(new WebChromeClient());
    }

    private void loadApp() {
        webView.loadUrl(DEFAULT_SERVER_URL);
    }

    // ==================== FAB Button ====================

    private View createFabButton() {
        TextView btn = new TextView(this);
        int size = (int) TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_DIP, 48, getResources().getDisplayMetrics());
        int marginSide = (int) TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_DIP, 16, getResources().getDisplayMetrics());
        // Move up 160dp to clear the input box + panel buttons row
        int marginBottom = (int) TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_DIP, 160, getResources().getDisplayMetrics());

        FrameLayout.LayoutParams params = new FrameLayout.LayoutParams(size, size);
        params.gravity = Gravity.BOTTOM | Gravity.END;
        params.setMargins(0, 0, marginSide, marginBottom);
        btn.setLayoutParams(params);

        GradientDrawable bg = new GradientDrawable();
        bg.setShape(GradientDrawable.RECTANGLE);
        bg.setCornerRadius(size / 2f);
        bg.setColor(0xE6404040);
        btn.setBackground(bg);

        btn.setText("导出");
        btn.setTextColor(Color.WHITE);
        btn.setTextSize(TypedValue.COMPLEX_UNIT_DIP, 12);
        btn.setGravity(Gravity.CENTER);
        btn.setClickable(true);
        btn.setFocusable(true);

        btn.setOnClickListener(v -> onExportClick());

        btn.setOnLongClickListener(v -> {
            showToast("导出当前会话的用户消息到 Markdown 文件");
            return true;
        });

        return btn;
    }

    private void onExportClick() {
        if (isExporting) {
            showToast("正在导出中，请稍候...");
            return;
        }
        isExporting = true;
        showToast("正在获取会话消息...");
        webView.evaluateJavascript(buildExportScript(), null);
    }

    // ==================== Capture Script (injected on page load) ====================

    private String buildCaptureScript() {
        return String.join("\n",
            "(function() {",
            "  if (window._mcCaptureInstalled) return;",
            "  window._mcCaptureInstalled = true;",
            "  window._mcUserMessages = [];",
            "  window._mcConvName = '';",
            "",
            "  // ===== Patch Worker to intercept decoded WebSocket messages =====",
            "  var OrigWorker = window.Worker;",
            "  window.Worker = function(url, options) {",
            "    var worker = arguments.length > 1 ? new OrigWorker(url, options) : new OrigWorker(url);",
            "    worker.addEventListener('message', function(event) {",
            "      try {",
            "        if (event.data && event.data.type === 'messages' && event.data.data && event.data.data.messages) {",
            "          var msgs = event.data.data.messages;",
            "          for (var i = 0; i < msgs.length; i++) {",
            "            var msg = msgs[i];",
            "            if (msg.type === 'user-input' && msg.data) {",
            "              var content = typeof msg.data === 'string' ? msg.data : String(msg.data);",
            "              if (content && content.length > 0) {",
            "                window._mcUserMessages.push({",
            "                  time: msg.timestamp || 0,",
            "                  content: content",
            "                });",
            "                if (!window._mcConvName) window._mcConvName = content;",
            "              }",
            "            }",
            "          }",
            "        }",
            "      } catch(e) {}",
            "    });",
            "    return worker;",
            "  };",
            "  window.Worker.prototype = OrigWorker.prototype;",
            "  if (OrigWorker.prototype && OrigWorker.prototype.constructor) {",
            "    window.Worker.prototype.constructor = window.Worker;",
            "  }",
            "",
            "  // ===== Patch fetch for model switching API capture =====",
            "  var origFetch = window.fetch;",
            "  window.fetch = async function() {",
            "    var args = Array.prototype.slice.call(arguments);",
            "    var resource = args[0];",
            "    var config = args[1] || {};",
            "    var url = typeof resource === 'string' ? resource : (resource && resource.url) || '';",
            "    var method = ((config && config.method) || 'GET').toUpperCase();",
            "    if (url.indexOf('/api/v1/users/models/') >= 0 && method === 'PUT') {",
            "      var bodyStr = '';",
            "      if (config && config.body) {",
            "        bodyStr = typeof config.body === 'string' ? config.body : JSON.stringify(config.body);",
            "      }",
            "      var headers = {};",
            "      if (config && config.headers) {",
            "        if (config.headers instanceof Headers) {",
            "          config.headers.forEach(function(v, k) { headers[k] = v; });",
            "        } else if (typeof config.headers === 'object') {",
            "          for (var k in config.headers) { if (config.headers.hasOwnProperty(k)) headers[k] = config.headers[k]; }",
            "        }",
            "      }",
            "      var response = await origFetch.apply(this, args);",
            "      var responseText = '';",
            "      try { var clone = response.clone(); responseText = await clone.text(); } catch(e) {}",
            "      var modelName = '';",
            "      try {",
            "        var match = url.match(/\\/models\\/([^\\/\\?]+)/);",
            "        if (match) modelName = match[1];",
            "        var respJson = JSON.parse(responseText);",
            "        if (respJson.data) {",
            "          if (respJson.data.model) modelName = respJson.data.model;",
            "          if (respJson.data.remark) modelName = respJson.data.remark;",
            "        }",
            "      } catch(e) {}",
            "      try {",
            "        window.MonkeyCodeBridge.captureCurl(JSON.stringify({",
            "          url: url, method: method, body: bodyStr,",
            "          headers: JSON.stringify(headers), cookies: document.cookie,",
            "          responseStatus: response.status, responseBody: responseText,",
            "          modelName: modelName",
            "        }));",
            "      } catch(e) {}",
            "      return response;",
            "    }",
            "    return origFetch.apply(this, args);",
            "  };",
            "",
            "  // ===== Patch XHR for model switching =====",
            "  var origXhrOpen = XMLHttpRequest.prototype.open;",
            "  var origXhrSend = XMLHttpRequest.prototype.send;",
            "  var origXhrSetHeader = XMLHttpRequest.prototype.setRequestHeader;",
            "  XMLHttpRequest.prototype.setRequestHeader = function(name, value) {",
            "    if (!this._mcHeaders) this._mcHeaders = {};",
            "    this._mcHeaders[name] = value;",
            "    return origXhrSetHeader.apply(this, [name, value]);",
            "  };",
            "  XMLHttpRequest.prototype.open = function(method, url) {",
            "    this._mcMethod = method; this._mcUrl = url;",
            "    return origXhrOpen.apply(this, Array.prototype.slice.call(arguments));",
            "  };",
            "  XMLHttpRequest.prototype.send = function(body) {",
            "    var self = this;",
            "    if (self._mcMethod && self._mcMethod.toUpperCase() === 'PUT' &&",
            "        self._mcUrl && self._mcUrl.indexOf('/api/v1/users/models/') >= 0) {",
            "      self.addEventListener('load', function() {",
            "        var modelName = '';",
            "        try {",
            "          var match = self._mcUrl.match(/\\/models\\/([^\\/\\?]+)/);",
            "          if (match) modelName = match[1];",
            "          var respJson = JSON.parse(self.responseText);",
            "          if (respJson.data && respJson.data.model) modelName = respJson.data.model;",
            "        } catch(e) {}",
            "        try {",
            "          window.MonkeyCodeBridge.captureCurl(JSON.stringify({",
            "            url: self._mcUrl, method: self._mcMethod.toUpperCase(),",
            "            body: body ? String(body) : '',",
            "            headers: JSON.stringify(self._mcHeaders || {}),",
            "            cookies: document.cookie,",
            "            responseStatus: self.status, responseBody: self.responseText || '',",
            "            modelName: modelName",
            "          }));",
            "        } catch(e) {}",
            "      });",
            "    }",
            "    return origXhrSend.apply(this, [body]);",
            "  };",
            "",
            "  console.log('[MonkeyCode] Worker + API capture installed');",
            "})();"
        );
    }

    // ==================== Export Script (on button click) ====================

    private String buildExportScript() {
        return String.join("\n",
            "(async function() {",
            "  function sanitizeFilename(name) {",
            "    return name.replace(/[^\\w\\u4e00-\\u9fa5\\-_]/g, '_').replace(/_+/g, '_').substring(0, 50).replace(/^_|_$/g, '') || 'untitled';",
            "  }",
            // formatTime: returns formatted string, or empty string if invalid
            "  function formatTime(ts) {",
            "    if (typeof ts !== 'number' || !isFinite(ts) || ts <= 0) return '';",
            "    var d = new Date(ts);",
            "    if (isNaN(d.getTime())) return '';",
            "    var pad = function(n) { return String(n).padStart(2, '0'); };",
            "    return d.getFullYear() + '-' + pad(d.getMonth()+1) + '-' + pad(d.getDate()) + ' ' + pad(d.getHours()) + ':' + pad(d.getMinutes()) + ':' + pad(d.getSeconds());",
            "  }",
            // isValidTime: checks if timestamp is a valid number that produces a valid Date
            "  function isValidTime(ts) {",
            "    if (typeof ts !== 'number' || !isFinite(ts) || ts <= 0) return false;",
            "    var d = new Date(ts);",
            "    return !isNaN(d.getTime());",
            "  }",
            "",
            "  var taskId = '';",
            "  var pathParts = location.pathname.split('/');",
            "  for (var i = 0; i < pathParts.length; i++) {",
            "    if (pathParts[i] === 'task' && i + 1 < pathParts.length) { taskId = pathParts[i + 1]; break; }",
            "  }",
            "  if (!taskId) {",
            "    var match = location.href.match(/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})/i);",
            "    if (match) taskId = match[1];",
            "  }",
            "",
            "  window.MonkeyCodeBridge.onExportProgress('正在提取消息...');",
            "  var userMessages = [];",
            "  var conversationName = '';",
            "",
            "  // ===== Method 1: React Fiber traversal (PRIMARY) =====",
            "  try {",
            "    var rootFiber = null;",
            "    var allEls = document.querySelectorAll('[id^=\"message-\"], .user-message-markdown, [class*=\"overflow-y-auto\"]');",
            "    for (var ei = 0; ei < allEls.length; ei++) {",
            "      var keys = Object.keys(allEls[ei]);",
            "      for (var ki = 0; ki < keys.length; ki++) {",
            "        if (keys[ki].indexOf('__reactFiber') === 0 || keys[ki].indexOf('__reactInternal') === 0) {",
            "          rootFiber = allEls[ei][keys[ki]];",
            "          break;",
            "        }",
            "      }",
            "      if (rootFiber) break;",
            "    }",
            "    if (!rootFiber) {",
            "      var allDivs = document.querySelectorAll('div');",
            "      for (var di = 0; di < allDivs.length && di < 200; di++) {",
            "        var dkeys = Object.keys(allDivs[di]);",
            "        for (var dki = 0; dki < dkeys.length; dki++) {",
            "          if (dkeys[dki].indexOf('__reactFiber') === 0 || dkeys[dki].indexOf('__reactInternal') === 0) {",
            "            rootFiber = allDivs[di][dkeys[dki]];",
            "            break;",
            "          }",
            "        }",
            "        if (rootFiber) break;",
            "      }",
            "    }",
            "    if (rootFiber) {",
            "      while (rootFiber && rootFiber.return) rootFiber = rootFiber.return;",
            "      var queue = [rootFiber];",
            "      var visited = new Set();",
            "      var found = false;",
            "      while (queue.length > 0 && !found) {",
            "        var f = queue.shift();",
            "        if (!f || visited.has(f)) continue;",
            "        visited.add(f);",
            "        if (f.memoizedProps) {",
            "          var p = f.memoizedProps;",
            "          if (p.messages && Array.isArray(p.messages) && p.messages.length > 0) {",
            "            var fm = p.messages[0];",
            "            if (fm && fm.type && fm.time !== undefined && fm.data !== undefined) {",
            "              for (var mi = 0; mi < p.messages.length; mi++) {",
            "                var m = p.messages[mi];",
            "                if (m.type === 'user_input' && m.data && m.data.content) {",
            "                  userMessages.push({ time: m.time || 0, content: m.data.content });",
            "                  if (!conversationName) conversationName = m.data.content;",
            "                }",
            "              }",
            "              found = true;",
            "            }",
            "          }",
            "          if (!found && p.taskManager && p.taskManager.state && p.taskManager.state.messages) {",
            "            var tmMsgs = p.taskManager.state.messages;",
            "            for (var ti = 0; ti < tmMsgs.length; ti++) {",
            "              var tm = tmMsgs[ti];",
            "              if (tm.type === 'user_input' && tm.data && tm.data.content) {",
            "                userMessages.push({ time: tm.time || 0, content: tm.data.content });",
            "                if (!conversationName) conversationName = tm.data.content;",
            "              }",
            "            }",
            "            found = true;",
            "          }",
            "        }",
            "        if (!found && f.memoizedState) {",
            "          var hook = f.memoizedState;",
            "          while (hook) {",
            "            if (hook.memoizedState && typeof hook.memoizedState === 'object' &&",
            "                hook.memoizedState.current && hook.memoizedState.current.state &&",
            "                Array.isArray(hook.memoizedState.current.state.messages)) {",
            "              var refMsgs = hook.memoizedState.current.state.messages;",
            "              for (var ri = 0; ri < refMsgs.length; ri++) {",
            "                var rm = refMsgs[ri];",
            "                if (rm.type === 'user_input' && rm.data && rm.data.content) {",
            "                  userMessages.push({ time: rm.time || 0, content: rm.data.content });",
            "                  if (!conversationName) conversationName = rm.data.content;",
            "                }",
            "              }",
            "              found = true;",
            "              break;",
            "            }",
            "            hook = hook.next;",
            "          }",
            "        }",
            "        if (f.child) queue.push(f.child);",
            "        if (f.sibling) queue.push(f.sibling);",
            "      }",
            "      console.log('[Export] React fiber found messages:', userMessages.length);",
            "    } else {",
            "      console.log('[Export] No React fiber found');",
            "    }",
            "  } catch(e) { console.error('[Export] React fiber error:', e); }",
            "",
            "  // ===== Method 2: Worker-captured messages (SECONDARY) =====",
            "  if (window._mcUserMessages && window._mcUserMessages.length > 0) {",
            "    for (var wi = 0; wi < window._mcUserMessages.length; wi++) {",
            "      userMessages.push({",
            "        time: window._mcUserMessages[wi].time,",
            "        content: window._mcUserMessages[wi].content",
            "      });",
            "    }",
            "    if (!conversationName) conversationName = window._mcConvName || '';",
            "    console.log('[Export] Worker-captured messages:', window._mcUserMessages.length);",
            "  }",
            "",
            "  // ===== Method 3: REST API for task name (TERTIARY) =====",
            "  if (taskId) {",
            "    try {",
            "      var resp = await fetch('/api/v1/users/tasks/' + taskId, { credentials: 'include' });",
            "      var json = await resp.json();",
            "      if (json.data) {",
            "        if (json.data.summary) conversationName = json.data.summary;",
            "        else if (json.data.content && !conversationName) conversationName = json.data.content;",
            "        if (json.data.content && userMessages.length === 0) {",
            "          var ts = json.data.created_at ? json.data.created_at * 1000 : 0;",
            "          userMessages.push({ time: ts, content: json.data.content });",
            "        }",
            "      }",
            "    } catch(e) { console.error('[Export] API error:', e); }",
            "  }",
            "",
            "  // ===== Deduplicate, sort, export =====",
            "  var seen = {};",
            "  userMessages = userMessages.filter(function(m) {",
            "    var key = m.content.substring(0, 100);",
            "    if (seen[key]) return false;",
            "    seen[key] = true;",
            "    return true;",
            "  });",
            "  console.log('[Export] Total unique messages:', userMessages.length);",
            "  if (userMessages.length === 0) {",
            "    window.MonkeyCodeBridge.onExportProgress('未找到用户消息，请确保在任务会话页面使用');",
            "    return;",
            "  }",
            "  // Sort by time; messages with invalid time go to the end",
            "  userMessages.sort(function(a, b) {",
            "    var aValid = isValidTime(a.time);",
            "    var bValid = isValidTime(b.time);",
            "    if (!aValid && !bValid) return 0;",
            "    if (!aValid) return 1;",
            "    if (!bValid) return -1;",
            "    return a.time - b.time;",
            "  });",
            "  var filename = sanitizeFilename(conversationName || (taskId || 'untitled'));",
            "  var md = '# \\u4f1a\\u8bdd\\u7528\\u6237\\u6d88\\u606f\\u5bfc\\u51fa\\n\\n';",
            "  md += '> \\u5bfc\\u51fa\\u65f6\\u95f4: ' + (formatTime(Date.now()) || '\\u672a\\u77e5') + '\\n';",
            "  if (taskId) md += '> \\u4efb\\u52a1ID: ' + taskId + '\\n';",
            "  md += '> \\u7528\\u6237\\u6d88\\u606f\\u6570: ' + userMessages.length + '\\n\\n';",
            "  md += '---\\n\\n';",
            "  for (var i = 0; i < userMessages.length; i++) {",
            "    var msg = userMessages[i];",
            "    var timeStr = formatTime(msg.time);",
            "    // If time is valid, show it; otherwise show message number",
            "    if (timeStr) {",
            "      md += '## ' + timeStr + '\\n\\n';",
            "    } else {",
            "      md += '## \\u6d88\\u606f ' + (i + 1) + '\\n\\n';",
            "    }",
            "    md += msg.content + '\\n\\n---\\n\\n';",
            "  }",
            "  window.MonkeyCodeBridge.exportMd(md, filename);",
            "})();"
        );
    }

    // ==================== File Writing ====================

    private void saveToDownloads(String content, String filename) {
        try {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                ContentValues values = new ContentValues();
                values.put(MediaStore.Downloads.DISPLAY_NAME, filename);
                values.put(MediaStore.Downloads.MIME_TYPE, "application/octet-stream");
                values.put(MediaStore.Downloads.RELATIVE_PATH,
                    Environment.DIRECTORY_DOWNLOADS + "/MonkeyCode");
                values.put(MediaStore.Downloads.IS_PENDING, 1);

                Uri uri = getContentResolver().insert(
                    MediaStore.Downloads.EXTERNAL_CONTENT_URI, values);
                if (uri != null) {
                    OutputStream os = getContentResolver().openOutputStream(uri);
                    os.write(content.getBytes(StandardCharsets.UTF_8));
                    os.close();

                    values.clear();
                    values.put(MediaStore.Downloads.IS_PENDING, 0);
                    getContentResolver().update(uri, values, null, null);
                }
            } else {
                if (checkSelfPermission("android.permission.WRITE_EXTERNAL_STORAGE")
                        != PackageManager.PERMISSION_GRANTED) {
                    showToast("需要存储权限，请在设置中授权");
                    return;
                }
                File dir = new File(
                    Environment.getExternalStoragePublicDirectory(Environment.DIRECTORY_DOWNLOADS),
                    "MonkeyCode");
                if (!dir.exists()) dir.mkdirs();
                File file = new File(dir, filename);
                FileOutputStream fos = new FileOutputStream(file);
                fos.write(content.getBytes(StandardCharsets.UTF_8));
                fos.close();
            }

            showToast("已保存到 Download/MonkeyCode/" + filename);
        } catch (Exception e) {
            showToast("保存失败: " + e.getMessage());
        }
    }

    private String formatCurlCommand(JSONObject data) {
        StringBuilder sb = new StringBuilder();
        String now = new SimpleDateFormat("yyyy-MM-dd HH:mm:ss", Locale.getDefault()).format(new Date());

        String modelName = data.optString("modelName", "unknown");
        String url = data.optString("url", "");
        String method = data.optString("method", "PUT");
        String body = data.optString("body", "");
        int respStatus = data.optInt("responseStatus", 0);
        String respBody = data.optString("responseBody", "");

        sb.append("#!/bin/bash\n");
        sb.append("# MonkeyCode 模型切换 API 捕获\n");
        sb.append("# 捕获时间: ").append(now).append("\n");
        sb.append("# 模型: ").append(modelName).append("\n");
        sb.append("# 状态码: ").append(respStatus).append("\n\n");

        sb.append("curl -X ").append(method);
        sb.append(" \\\n  '").append(url).append("'");

        sb.append(" \\\n  -H 'Content-Type: application/json'");

        try {
            String headersStr = data.optString("headers", "{}");
            JSONObject headers = new JSONObject(headersStr);
            Iterator<String> keys = headers.keys();
            while (keys.hasNext()) {
                String key = keys.next();
                if (key.toLowerCase().equals("content-type")) continue;
                String value = headers.getString(key);
                sb.append(" \\\n  -H '").append(key).append(": ").append(value).append("'");
            }
        } catch (Exception e) {}

        String cookies = CookieManager.getInstance().getCookie(url);
        if (cookies != null && !cookies.isEmpty()) {
            sb.append(" \\\n  -H 'Cookie: ").append(cookies).append("'");
        }

        if (!body.isEmpty()) {
            String escapedBody = body.replace("'", "'\\''");
            sb.append(" \\\n  -d '").append(escapedBody).append("'");
        }

        sb.append("\n\n");
        sb.append("# === 响应 ===\n");
        sb.append("# HTTP ").append(respStatus).append("\n");

        if (respBody.length() > 2000) {
            respBody = respBody.substring(0, 2000) + "...(truncated)";
        }
        String[] respLines = respBody.split("\n");
        for (String line : respLines) {
            sb.append("# ").append(line).append("\n");
        }

        return sb.toString();
    }

    private String sanitizeFilename(String name) {
        if (name == null || name.isEmpty()) return "unknown";
        String sanitized = name.replaceAll("[^\\w\\u4e00-\\u9fa5\\-_]", "_")
            .replaceAll("_+", "_")
            .trim();
        if (sanitized.length() > 50) sanitized = sanitized.substring(0, 50);
        sanitized = sanitized.replaceAll("^_|_$", "");
        if (sanitized.isEmpty()) sanitized = "unknown";
        return sanitized;
    }

    // ==================== Toast ====================

    private void showToast(final String message) {
        runOnUiThread(new Runnable() {
            @Override
            public void run() {
                Toast.makeText(MainActivity.this, message, Toast.LENGTH_SHORT).show();
            }
        });
    }

    // ==================== Error View ====================

    private View createErrorView() {
        LinearLayout layout = new LinearLayout(this);
        layout.setOrientation(LinearLayout.VERTICAL);
        layout.setGravity(android.view.Gravity.CENTER);
        layout.setPadding(32, 32, 32, 32);

        TextView title = new TextView(this);
        title.setText("无法连接到服务器");
        title.setTextSize(18);
        title.setGravity(android.view.Gravity.CENTER);
        title.setPadding(0, 0, 0, 16);

        TextView subtitle = new TextView(this);
        subtitle.setText("请检查网络连接后重试\n\nMonkeyCode AI");
        subtitle.setTextSize(14);
        subtitle.setGravity(android.view.Gravity.CENTER);

        layout.addView(title);
        layout.addView(subtitle);
        return layout;
    }

    // ==================== JS Bridge ====================

    private class MonkeyCodeBridge {

        @JavascriptInterface
        public void exportMd(String md, String filename) {
            final String safeName = sanitizeFilename(filename) + ".md";
            final String content = md;
            new Thread(new Runnable() {
                @Override
                public void run() {
                    saveToDownloads(content, safeName);
                    isExporting = false;
                }
            }).start();
        }

        @JavascriptInterface
        public void captureCurl(String jsonData) {
            try {
                final JSONObject data = new JSONObject(jsonData);
                final String modelName = sanitizeFilename(data.optString("modelName", "unknown"));
                final String dateStr = new SimpleDateFormat("yyyyMMdd_HHmm", Locale.getDefault()).format(new Date());
                // Use .sh extension - it's a shell script, prevents Android from adding .txt
                final String filename = modelName + "_" + dateStr + ".sh";

                new Thread(new Runnable() {
                    @Override
                    public void run() {
                        String curl = formatCurlCommand(data);
                        saveToDownloads(curl, filename);
                    }
                }).start();
            } catch (Exception e) {
                showToast("捕获失败: " + e.getMessage());
            }
        }

        @JavascriptInterface
        public void onExportProgress(String message) {
            showToast(message);
            if (message.contains("未找到") || message.contains("失败") || message.contains("请在") || message.contains("确保")) {
                isExporting = false;
            }
        }
    }

    // ==================== Lifecycle ====================

    @Override
    public void onBackPressed() {
        if (webView.canGoBack()) {
            webView.goBack();
        } else {
            super.onBackPressed();
        }
    }

    @Override
    protected void onPause() {
        super.onPause();
        webView.onPause();
    }

    @Override
    protected void onResume() {
        super.onResume();
        webView.onResume();
    }

    @Override
    protected void onDestroy() {
        webView.destroy();
        super.onDestroy();
    }
}
