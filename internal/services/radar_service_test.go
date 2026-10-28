package services

import (
	"encoding/json"
	"errors"
	"testing"

	"wx_channel/internal/database"
	"wx_channel/internal/websocket"
)

// 模拟注入端 finderUserPage 返回的一条真实结构的 feed，
// 字段名对齐 internal/assets/inject/utils.js 的 format_feed 取法。
const sampleFeedJSON = `{
  "id": "1491398891234567890",
  "objectNonceId": "nonce-abc",
  "createtime": 1727000000,
  "readCount": 120345,
  "likeCount": 8721,
  "commentCount": 654,
  "favCount": 2310,
  "forwardCount": 477,
  "ipRegionInfo": { "country": "广东", "province": "广东" },
  "contact": {
    "username": "v2_abc123",
    "nickname": "玄机秘境",
    "headUrl": "https://example.com/avatar.jpg"
  },
  "objectDesc": {
    "description": "今天讲一个很多人搞错的概念",
    "mediaType": 4,
    "media": [
      {
        "url": "https://finder.video.qq.com/251/20302/stodownload?encfilekey=abc",
        "urlToken": "&token=tok456",
        "decodeKey": "987654321",
        "coverUrl": "https://example.com/cover.jpg",
        "thumbUrl": "https://example.com/thumb.jpg",
        "fileSize": 104857600,
        "width": 1080,
        "height": 1920,
        "spec": [
          { "fileFormat": "xWT111", "width": 1080, "height": 1920, "durationMs": 63000 }
        ]
      }
    ]
  }
}`

func decodeFeed(t *testing.T) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(sampleFeedJSON), &m); err != nil {
		t.Fatalf("sample feed must be valid JSON: %v", err)
	}
	return m
}

func TestBuildRadarVideoExtractsEveryField(t *testing.T) {
	target := database.RadarTarget{ID: "t1", Username: "v2_abc123", AuthorName: "玄机秘境"}
	v := buildRadarVideo(decodeFeed(t), target)
	if v == nil {
		t.Fatal("buildRadarVideo returned nil for a well-formed feed")
	}

	checks := []struct {
		name string
		got  interface{}
		want interface{}
	}{
		{"VideoID", v.VideoID, "1491398891234567890"},
		{"NonceID", v.NonceID, "nonce-abc"},
		{"CreateTime", v.CreateTime, int64(1727000000)},
		{"Title", v.Title, "今天讲一个很多人搞错的概念"},
		{"AuthorName", v.AuthorName, "玄机秘境"},
		{"AuthorAvatar", v.AuthorAvatar, "https://example.com/avatar.jpg"},
		{"Username", v.Username, "v2_abc123"},
		{"TargetID", v.TargetID, "t1"},
		{"MediaType", v.MediaType, 4},
		{"VideoURL", v.VideoURL, "https://finder.video.qq.com/251/20302/stodownload?encfilekey=abc&token=tok456"},
		{"DecryptKey", v.DecryptKey, "987654321"},
		{"CoverURL", v.CoverURL, "https://example.com/cover.jpg"},
		{"ThumbURL", v.ThumbURL, "https://example.com/thumb.jpg"},
		{"FileSize", v.FileSize, int64(104857600)},
		{"Resolution", v.Resolution, "1080x1920"},
		{"FileFormat", v.FileFormat, "xWT111"},
		{"Duration", v.Duration, int64(63)},
		{"ReadCount", v.ReadCount, int64(120345)},
		{"LikeCount", v.LikeCount, int64(8721)},
		{"CommentCount", v.CommentCount, int64(654)},
		{"FavCount", v.FavCount, int64(2310)},
		{"ForwardCount", v.ForwardCount, int64(477)},
		{"IPRegion", v.IPRegion, "广东"},
	}

	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v (%T), want %v", c.name, c.got, c.got, c.want)
		}
	}

	if v.Raw == "" {
		t.Error("Raw must preserve the original feed JSON for future fields")
	}
}

// 时长优先级：spec[0].durationMs -> videoPlayLen -> videoDuration
func TestExtractDurationPriority(t *testing.T) {
	cases := []struct {
		name string
		media map[string]interface{}
		want int64
	}{
		{
			"spec durationMs 优先",
			map[string]interface{}{
				"spec":         []interface{}{map[string]interface{}{"durationMs": float64(45000)}},
				"videoPlayLen": float64(99),
			},
			45,
		},
		{
			"回退 videoPlayLen",
			map[string]interface{}{"videoPlayLen": float64(30)},
			30,
		},
		{
			"回退 videoDuration",
			map[string]interface{}{"videoDuration": float64(12)},
			12,
		},
		{
			"都没有则为 0",
			map[string]interface{}{},
			0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractDuration(tc.media); got != tc.want {
				t.Errorf("extractDuration = %d, want %d", got, tc.want)
			}
		})
	}
}

// 分辨率优先取宽高，其次 videoResolution
func TestExtractResolutionFallback(t *testing.T) {
	withWH := map[string]interface{}{"width": float64(1920), "height": float64(1080), "videoResolution": "720p"}
	if got := extractResolution(withWH); got != "1920x1080" {
		t.Errorf("got %q, want 1920x1080", got)
	}

	fromSpec := map[string]interface{}{
		"spec": []interface{}{map[string]interface{}{"width": float64(720), "height": float64(1280)}},
	}
	if got := extractResolution(fromSpec); got != "720x1280" {
		t.Errorf("got %q, want 720x1280", got)
	}

	fromString := map[string]interface{}{"videoResolution": "1080p"}
	if got := extractResolution(fromString); got != "1080p" {
		t.Errorf("got %q, want 1080p", got)
	}
}

// 缺 id 的条目必须被丢弃，否则无法去重
func TestBuildRadarVideoRejectsMissingID(t *testing.T) {
	target := database.RadarTarget{ID: "t1", Username: "u", AuthorName: "a"}
	if got := buildRadarVideo(map[string]interface{}{"createtime": float64(1)}, target); got != nil {
		t.Error("feed without id must be rejected")
	}
}

// 没有 title 时回退到可识别的占位标题，而不是空串
func TestBuildRadarVideoFallsBackToTitle(t *testing.T) {
	target := database.RadarTarget{ID: "t1", Username: "u", AuthorName: "a"}
	v := buildRadarVideo(map[string]interface{}{"id": "vid-1"}, target)
	if v == nil {
		t.Fatal("expected a record")
	}
	if v.Title == "" {
		t.Error("Title must never be empty")
	}
}

// 翻页游标：兼容多个字段名，且游标不推进时必须停止
func TestFeedListNextMarkerCandidates(t *testing.T) {
	cases := []struct {
		json string
		want string
	}{
		{`{"data":{"lastBuffer":"m1"}}`, "m1"},
		{`{"data":{"lastBuff":"m2"}}`, "m2"},
		{`{"data":{"nextMarker":"m3"}}`, "m3"},
		{`{"data":{"buffer":"m4"}}`, "m4"},
		{`{"data":{}}`, ""},
		{`{"data":{"hasMore":true}}`, ""},
	}
	for _, tc := range cases {
		var r feedListResponse
		if err := json.Unmarshal([]byte(tc.json), &r); err != nil {
			t.Fatalf("bad test json: %v", err)
		}
		if got := r.nextMarker(); got != tc.want {
			t.Errorf("nextMarker(%s) = %q, want %q", tc.json, got, tc.want)
		}
	}
}

// objectList / object 两种字段名都要能取到
func TestFeedItemsFallsBackToObject(t *testing.T) {
	var withList feedListResponse
	_ = json.Unmarshal([]byte(`{"data":{"objectList":[{"id":"a"},{"id":"b"}]}}`), &withList)
	if len(withList.feedItems()) != 2 {
		t.Errorf("objectList: got %d items, want 2", len(withList.feedItems()))
	}

	var withObject feedListResponse
	_ = json.Unmarshal([]byte(`{"data":{"object":[{"id":"c"}]}}`), &withObject)
	if len(withObject.feedItems()) != 1 {
		t.Errorf("object fallback: got %d items, want 1", len(withObject.feedItems()))
	}
}

// 回归：HasMore 曾经是值类型 bool，微信响应里没有 hasMore 字段时它保持 false，
// 于是第一页无条件退出，游标再正确也翻不了页。现在必须是「明确 false 才停」。
func TestHasMoreAbsentDoesNotStopPagination(t *testing.T) {
	cases := []struct {
		name string
		json string
		want bool
	}{
		{"字段缺失时不阻拦", `{"data":{"objectList":[{"id":"a"}]}}`, true},
		{"明确 true", `{"data":{"objectList":[],"hasMore":true}}`, true},
		{"明确 false", `{"data":{"objectList":[],"hasMore":false}}`, false},
		{"null 视为不阻拦", `{"data":{"objectList":[],"hasMore":null}}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var r feedListResponse
			if err := json.Unmarshal([]byte(tc.json), &r); err != nil {
				t.Fatalf("bad json: %v", err)
			}
			if got := r.hasMore(); got != tc.want {
				t.Errorf("hasMore() = %v, want %v", got, tc.want)
			}
		})
	}
}

// SyncNow 在已有采集进行时必须立刻拒绝，而不是排队等完。
// 手动同步是给用户点的，等十几秒才返回会让人以为按钮坏了。
func TestSyncNowRejectsWhenAlreadyScanning(t *testing.T) {
	svc := &RadarService{hub: websocket.NewHub()}
	// 模拟一次采集正在进行
	svc.scanning.Store(true)

	if _, err := svc.SyncNow(); !errors.Is(err, ErrSyncRunning) {
		t.Errorf("SyncNow while scanning = %v, want ErrSyncRunning", err)
	}
}

// 没有微信客户端时必须明确报错，而不是带着空结果返回成功
func TestSyncNowRequiresWeChatClient(t *testing.T) {
	svc := &RadarService{hub: websocket.NewHub()}
	// 新建的 hub 没有任何连接，ClientCount() 为 0
	if _, err := svc.SyncNow(); !errors.Is(err, ErrNoWeChatClient) {
		t.Errorf("SyncNow without client = %v, want ErrNoWeChatClient", err)
	}
}

// scanning 标记必须能被正确置位与释放，否则手动同步会永久失效
func TestScanningFlagIsReleasedAfterSync(t *testing.T) {
	svc := &RadarService{}
	if !svc.scanning.CompareAndSwap(false, true) {
		t.Fatal("first CAS should win")
	}
	if svc.scanning.CompareAndSwap(false, true) {
		t.Error("second CAS should fail while held")
	}
	svc.scanning.Store(false)
	if !svc.scanning.CompareAndSwap(false, true) {
		t.Error("CAS should succeed again after release")
	}
}

// 检查页数、每页条数和真实字段名必须能取到，否则翻页失效时无从排查
func TestFeedDataKeysListsRealFieldNames(t *testing.T) {
	raw := []byte(`{"data":{"BaseResponse":{"Ret":0},"objectList":[],"lastBuffer":"abc"}}`)
	keys := feedDataKeys(raw)
	want := []string{"BaseResponse", "lastBuffer", "objectList"}
	if len(keys) != len(want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Errorf("keys[%d] = %q, want %q (must be sorted)", i, keys[i], want[i])
		}
	}
}
