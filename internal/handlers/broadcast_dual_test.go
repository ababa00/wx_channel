package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// 测试用的 WS 信封，字段与 handlers.WebSocketHub 广播的一致
type testHubEnvelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// newTestHub 起一个独立的 WebSocketHub 并跑主循环。
//
// 刻意不用 GetWebSocketHub() 单例：那个单例会额外启动一个每 5 秒查数据库的
// 统计广播 goroutine，而同包其他用例的 cleanup 会 database.Close() 把全局 DB
// 置空，两者相撞就是 nil 指针 panic。NewWebSocketHub() 不起后台 goroutine，
// 隔离干净。
func newTestHub(t *testing.T) *WebSocketHub {
	t.Helper()
	hub := NewWebSocketHub()
	go hub.Run()
	return hub
}

// newTestHubClient 起一个真实 HTTP 服务器挂 /ws，再拨一个 WS 连接，
// 等它完成注册后返回。用真连接而不是 mock：注册是异步的（走 hub.register
// channel），只有真连一遍才能确认客户端真的进了 clients 集合。
func newTestHubClient(t *testing.T, hub *WebSocketHub) *websocket.Conn {
	t.Helper()

	handler := &WebSocketHandler{hub: hub}
	srv := httptest.NewServer(http.HandlerFunc(handler.HandleWebSocket))
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{srv.URL}},
	})
	if err != nil {
		t.Fatalf("拨号失败: %v", err)
	}

	// 等注册完成：注册走 channel，是异步的
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if hub.ClientCount() > 0 {
			return conn
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("客户端未完成注册（ClientCount 仍为 0）")
	return nil
}

// readHubMessage 读一条消息，带超时
func readHubMessage(t *testing.T, conn *websocket.Conn) testHubEnvelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("读取广播失败: %v", err)
	}
	var msg testHubEnvelope
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("解包广播失败: %v, raw=%s", err, data)
	}
	return msg
}

// 项目里有两套互不相通的 WebSocket Hub，这是「下载完成后必须刷新页面才显示
// 播放按钮」的根因：
//
//   - handlers.WebSocketHub（GetWebSocketHub() 单例）—— /ws 端点注册的控制台
//     页面客户端连在这里，原本只收到队列和统计广播。
//   - websocket.Hub（app.WSHub）—— SunnyNet 注入的微信页面客户端注册在这里，
//     下载事件的广播原本只发到这里。
//
// 所以 download_complete 发给了微信页面，用户盯着的控制台页面收不到。
// broadcastDownloadEvent 必须同时投递到两边。

// 关键回归：控制台页面所在的 hub 必须收到下载完成事件。
// 把 broadcastDownloadEvent 里的第二个投递删掉，本用例就会失败。
func TestBroadcastDownloadEventReachesConsoleHub(t *testing.T) {
	hub := newTestHub(t)
	restore := consoleHubProvider
	consoleHubProvider = func() *WebSocketHub { return hub }
	defer func() { consoleHubProvider = restore }()

	conn := newTestHubClient(t, hub)
	defer conn.Close(websocket.StatusNormalClosure, "")

	h := &UploadHandler{} // wsHub 为 nil：模拟微信页面那套 hub 缺席
	h.broadcastDownloadEvent("download_complete", map[string]interface{}{
		"videoId": "probe-video-1",
		"title":   "探针视频",
		"path":    `C:\x\a.mp4`,
	})

	msg := readHubMessage(t, conn)

	// 信封必须是 {type:"cmd", data:{action,payload}} —— 前端 handleCommand 依赖它
	if msg.Type != "cmd" {
		t.Fatalf("type = %q, want \"cmd\"", msg.Type)
	}
	var cmd struct {
		Action  string                 `json:"action"`
		Payload map[string]interface{} `json:"payload"`
	}
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		t.Fatalf("data 不是内联对象（前端会解不开）: %v, raw=%s", err, msg.Data)
	}
	if cmd.Action != "download_complete" {
		t.Fatalf("action = %q, want download_complete", cmd.Action)
	}
	if cmd.Payload["videoId"] != "probe-video-1" {
		t.Fatalf("payload.videoId = %v, want probe-video-1", cmd.Payload["videoId"])
	}
}

// wsHub 为 nil 时不能 panic（部分装配路径可能没注入）
func TestBroadcastDownloadEventToleratesNilWSHub(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("wsHub 为 nil 时 panic 了: %v", r)
		}
	}()
	h := &UploadHandler{}
	h.broadcastDownloadEvent("download_progress", map[string]interface{}{"videoId": "x"})
}

// 所有下载事件类型都要走双投递，不能只改 download_complete
func TestBroadcastDownloadEventCoversAllActions(t *testing.T) {
	hub := newTestHub(t)
	restore := consoleHubProvider
	consoleHubProvider = func() *WebSocketHub { return hub }
	defer func() { consoleHubProvider = restore }()

	conn := newTestHubClient(t, hub)
	defer conn.Close(websocket.StatusNormalClosure, "")

	h := &UploadHandler{}
	actions := []string{"download_complete", "download_failed", "download_progress"}

	for _, action := range actions {
		h.broadcastDownloadEvent(action, map[string]interface{}{"videoId": "v-" + action})
		msg := readHubMessage(t, conn)
		var cmd struct {
			Action string `json:"action"`
		}
		if err := json.Unmarshal(msg.Data, &cmd); err != nil {
			t.Fatalf("%s: 解包失败: %v", action, err)
		}
		if cmd.Action != action {
			t.Fatalf("action = %q, want %q", cmd.Action, action)
		}
	}
}
