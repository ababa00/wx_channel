package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"wx_channel/internal/database"
)

// 这些处理器会打到真实的 sqlite 连接上，先建一个临时库。
// 注意不能用 t.TempDir()：它会在测试结束时自动删除目录，
// 而 database.Initialize 持有连接不放，删除会因文件占用而失败。
// database.Initialize 内部有 once 语义，整个测试包共用第一个库即可。
func setupRadarAPITestDB(t *testing.T) func() {
	t.Helper()
	dir, err := os.MkdirTemp("", "wx_channel_radar_api_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	if err := database.Initialize(&database.Config{DBPath: filepath.Join(dir, "test.db")}); err != nil {
		os.RemoveAll(dir)
		t.Fatalf("failed to init test database: %v", err)
	}
	return func() { _ = os.RemoveAll(dir) }
}

// 回归：startDay / endDay 缺省时 parseDayParam 返回 (nil, nil)，
// 处理器曾经直接对 nil *time.Time 调用 Add() 导致整个接口 500。
// 这里覆盖缺省、单侧、两侧与非法输入四种情况。
func TestGetRadarVideosToleratesMissingDateParams(t *testing.T) {
	cleanup := setupRadarAPITestDB(t)
	defer cleanup()

	h := NewRadarServiceAPI()
	h.RegisterRoutes(http.NewServeMux()) // 确保路由注册本身不 panic

	paths := []string{
		"/api/v1/radar/videos",
		"/api/v1/radar/videos?page=1&pageSize=20",
		"/api/v1/radar/videos?onlyNew=true",
		"/api/v1/radar/videos?startDay=2026-01-01",
		"/api/v1/radar/videos?endDay=2026-12-31",
		"/api/v1/radar/videos?startDay=2026-01-01&endDay=2026-12-31",
		"/api/v1/radar/videos?startDay=not-a-date",
		"/api/v1/radar/videos?page=abc&pageSize=xyz",
		"/api/v1/radar/videos?sortBy=like_count&sortDesc=false",
		"/api/v1/radar/videos?query=%E7%8E%8B%E4%B8%96%E5%9B%BD&username=v2_abc",
	}

	for _, path := range paths {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()

		h.GetRadarVideos(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("%s -> status %d, want 200 (body: %s)", path, rec.Code, rec.Body.String())
			continue
		}

		var body struct {
			Code int `json:"code"`
			Data struct {
				Items []interface{} `json:"items"`
				Total int64         `json:"total"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Errorf("%s -> invalid JSON: %v", path, err)
		}
	}
}

// 列表接口必须在真实数据下吐出前端要用的全部字段。
// 前端渲染失败最常见的原因就是字段名对不上，这里逐个断言。
func TestGetRadarVideosSerializesEveryFieldThePageReads(t *testing.T) {
	cleanup := setupRadarAPITestDB(t)
	defer cleanup()

	target := &database.RadarTarget{ID: "t1", Username: "v2_abc", AuthorName: "玄机秘境"}
	if err := database.NewRadarRepository().Add(target); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	published := time.Now().Add(-48 * time.Hour).Unix()
	_, err := database.NewRadarVideoRepository().Upsert(&database.RadarVideo{
		VideoID:      "1491398891234567890",
		TargetID:     target.ID,
		Username:     target.Username,
		AuthorName:   target.AuthorName,
		AuthorAvatar: "https://example.com/avatar.jpg",
		NonceID:      "nonce-abc",
		Title:        "今天讲一个很多人搞错的概念",
		MediaType:    4,
		CreateTime:   published,
		CoverURL:     "https://example.com/cover.jpg",
		ThumbURL:     "https://example.com/thumb.jpg",
		VideoURL:     "https://finder.video.qq.com/251/20302/stodownload?encfilekey=abc&token=tok456",
		DecryptKey:   "987654321",
		FileSize:     104857600,
		Duration:     63,
		Resolution:   "1080x1920",
		FileFormat:   "xWT111",
		ReadCount:    120345,
		LikeCount:    8721,
		CommentCount: 654,
		FavCount:     2310,
		ForwardCount: 477,
		IPRegion:     "广东",
		PageURL:      "https://channels.weixin.qq.com/x",
	})
	if err != nil {
		t.Fatalf("seed video: %v", err)
	}

	h := NewRadarServiceAPI()
	rec := httptest.NewRecorder()
	// 按 targetId 取数，避免依赖测试执行顺序（同包共用一个数据库）
	h.GetRadarVideos(rec, httptest.NewRequest(http.MethodGet, "/api/v1/radar/videos?targetId=t1", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Code int `json:"code"`
		Data struct {
			Items []map[string]interface{} `json:"items"`
			Total int64                    `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.Total != 1 {
		t.Fatalf("Total = %d, want 1", body.Data.Total)
	}
	if len(body.Data.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(body.Data.Items))
	}

	item := body.Data.Items[0]
	want := map[string]interface{}{
		"videoId":      "1491398891234567890",
		"targetId":     "t1",
		"username":     "v2_abc",
		"authorName":   "玄机秘境",
		"authorAvatar": "https://example.com/avatar.jpg",
		"nonceId":      "nonce-abc",
		"title":        "今天讲一个很多人搞错的概念",
		"mediaType":    float64(4),
		"createTime":   float64(published),
		"coverUrl":     "https://example.com/cover.jpg",
		"thumbUrl":     "https://example.com/thumb.jpg",
		"videoUrl":     "https://finder.video.qq.com/251/20302/stodownload?encfilekey=abc&token=tok456",
		"decryptKey":   "987654321",
		"fileSize":     float64(104857600),
		"duration":     float64(63),
		"resolution":   "1080x1920",
		"fileFormat":   "xWT111",
		"readCount":    float64(120345),
		"likeCount":    float64(8721),
		"commentCount": float64(654),
		"favCount":     float64(2310),
		"forwardCount": float64(477),
		"ipRegion":     "广东",
		"pageUrl":      "https://channels.weixin.qq.com/x",
		"isNew":        true,
	}
	for key, expected := range want {
		got, ok := item[key]
		if !ok {
			t.Errorf("JSON missing field %q", key)
			continue
		}
		if got != expected {
			t.Errorf("%s = %v (%T), want %v", key, got, got, expected)
		}
	}

	// firstSeenAt 必须是非空字符串，否则前端「首次发现」列会显示 —
	if s, ok := item["firstSeenAt"].(string); !ok || s == "" {
		t.Errorf("firstSeenAt = %v, want non-empty string", item["firstSeenAt"])
	}

	// raw 不应出现在响应里，避免每行都带上整份 feed JSON
	if _, leaked := item["raw"]; leaked {
		t.Error("raw must not be serialized (tagged json:\"-\")")
	}
}

// 汇总接口在有数据时必须给出正确的卡片数值。
// 注意 database.Initialize 是 once 语义，整个包共用一个库，所以这里给数据打上
// 独立的 targetId 并按它取数，避免受其它测试残留数据影响。
func TestGetRadarVideoStatsReflectsStoredData(t *testing.T) {
	cleanup := setupRadarAPITestDB(t)
	defer cleanup()

	repo := database.NewRadarVideoRepository()
	_, _ = repo.Upsert(&database.RadarVideo{
		VideoID: "s1", TargetID: "stats-scope", Username: "v2_a", AuthorName: "甲",
		ReadCount: 1000, LikeCount: 50,
	})
	_, _ = repo.Upsert(&database.RadarVideo{
		VideoID: "s2", TargetID: "stats-scope", Username: "v2_b", AuthorName: "乙",
		ReadCount: 500, LikeCount: 20,
	})

	h := NewRadarServiceAPI()
	rec := httptest.NewRecorder()
	h.GetRadarVideoStats(rec, httptest.NewRequest(http.MethodGet, "/api/v1/radar/videos/stats?targetId=stats-scope", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Data database.RadarVideoStats `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.TotalVideos != 2 {
		t.Errorf("TotalVideos = %d, want 2", body.Data.TotalVideos)
	}
	if body.Data.TotalAuthors != 2 {
		t.Errorf("TotalAuthors = %d, want 2", body.Data.TotalAuthors)
	}
	if body.Data.TotalPlays != 1500 {
		t.Errorf("TotalPlays = %d, want 1500", body.Data.TotalPlays)
	}
	if body.Data.TotalLikes != 70 {
		t.Errorf("TotalLikes = %d, want 70", body.Data.TotalLikes)
	}
}

// 排序参数必须真的生效，且只能落在白名单列上
func TestGetRadarVideosSortsByLikeCount(t *testing.T) {
	cleanup := setupRadarAPITestDB(t)
	defer cleanup()

	repo := database.NewRadarVideoRepository()
	for i, likes := range []int64{5, 900, 60} {
		_, err := repo.Upsert(&database.RadarVideo{
			VideoID:    fmt.Sprintf("sort-%d", i),
			TargetID:   "sort-scope",
			Username:   "v2_sort",
			AuthorName: "排序测试",
			Title:      fmt.Sprintf("视频%d", i),
			LikeCount:  likes,
		})
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	h := NewRadarServiceAPI()
	fetch := func(sortBy string, desc bool) []int64 {
		q := fmt.Sprintf("/api/v1/radar/videos?targetId=sort-scope&sortBy=%s&sortDesc=%v&pageSize=50", sortBy, desc)
		rec := httptest.NewRecorder()
		h.GetRadarVideos(rec, httptest.NewRequest(http.MethodGet, q, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d for %s", rec.Code, q)
		}
		var body struct {
			Data struct {
				Items []struct {
					LikeCount int64 `json:"likeCount"`
				} `json:"items"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		out := make([]int64, 0, len(body.Data.Items))
		for _, it := range body.Data.Items {
			out = append(out, it.LikeCount)
		}
		return out
	}

	if got := fetch("like_count", true); !reflect.DeepEqual(got, []int64{900, 60, 5}) {
		t.Errorf("like_count desc = %v, want [900 60 5]", got)
	}
	if got := fetch("like_count", false); !reflect.DeepEqual(got, []int64{5, 60, 900}) {
		t.Errorf("like_count asc = %v, want [5 60 900]", got)
	}
	if got := fetch("fav_count", true); len(got) != 3 {
		t.Errorf("fav_count desc returned %d rows, want 3", len(got))
	}
}

// 同步端点：方法受限、服务未注入时明确报错
func TestSyncRadarVideosRejectsGetAndUnreadyService(t *testing.T) {
	cleanup := setupRadarAPITestDB(t)
	defer cleanup()

	h := NewRadarServiceAPI()

	rec := httptest.NewRecorder()
	h.SyncRadarVideos(rec, httptest.NewRequest(http.MethodGet, "/api/v1/radar/sync", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET -> status %d, want 405", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.SyncRadarVideos(rec, httptest.NewRequest(http.MethodPost, "/api/v1/radar/sync", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("POST without service -> status %d, want 503", rec.Code)
	}
}

func TestGetRadarCaptureConfigRoundTrip(t *testing.T) {
	cleanup := setupRadarAPITestDB(t)
	defer cleanup()

	h := NewRadarServiceAPI()

	rec := httptest.NewRecorder()
	h.GetRadarCaptureConfig(rec, httptest.NewRequest(http.MethodGet, "/api/v1/radar/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET config -> %d", rec.Code)
	}

	var get struct {
		Code int                `json:"code"`
		Data RadarCaptureConfig `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &get); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if get.Data.MaxPages < 1 {
		t.Errorf("MaxPages = %d, want >= 1", get.Data.MaxPages)
	}
}

// 非法页数必须被夹紧，而不是照单全收
func TestUpdateRadarCaptureConfigClampsMaxPages(t *testing.T) {
	cleanup := setupRadarAPITestDB(t)
	defer cleanup()

	h := NewRadarServiceAPI()

	cases := []struct {
		in  RadarCaptureConfig
		out RadarCaptureConfig
	}{
		{RadarCaptureConfig{MaxPages: 0}, RadarCaptureConfig{MaxPages: 1}},
		{RadarCaptureConfig{MaxPages: -5}, RadarCaptureConfig{MaxPages: 1}},
		{RadarCaptureConfig{MaxPages: 9999}, RadarCaptureConfig{MaxPages: 20}},
	}

	for _, tc := range cases {
		body, _ := json.Marshal(tc.in)
		rec := httptest.NewRecorder()
		h.UpdateRadarCaptureConfig(rec, httptest.NewRequest(http.MethodPut, "/api/v1/radar/config", bytes.NewReader(body)))

		if rec.Code != http.StatusOK {
			t.Fatalf("PUT config -> %d", rec.Code)
		}
		var got struct {
			Data RadarCaptureConfig `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.Data.MaxPages != tc.out.MaxPages {
			t.Errorf("MaxPages in=%d out=%d, want %d", tc.in.MaxPages, got.Data.MaxPages, tc.out.MaxPages)
		}
	}
}
