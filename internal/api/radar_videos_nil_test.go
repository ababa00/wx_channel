package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// 装配路径一旦漏掉 videoRepo，.List() / .Stats() / .Delete*() 就会空指针 panic。
// 历史上 /api/v1/radar/videos 上真实 panic 过 4 次（见日志
// "Panic recovered: invalid memory address or nil pointer dereference"）。
// 这里锁住「缺依赖时返回 503 而不是崩进程」。
func TestRadarVideoHandlersTolerateMissingRepo(t *testing.T) {
	h := &RadarServiceAPI{} // 故意不注入 videoRepo

	cases := []struct {
		name    string
		path    string
		handler func(http.ResponseWriter, *http.Request)
	}{
		{"列表", "/api/v1/radar/videos", h.GetRadarVideos},
		{"统计", "/api/v1/radar/videos/stats", h.GetRadarVideoStats},
		{"删除", "/api/v1/radar/videos", h.DeleteRadarVideos},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)

			// 关键：不能 panic。recover 一下让测试能报出失败而不是整个包崩掉
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("缺 videoRepo 时 panic 了: %v", r)
				}
			}()

			tc.handler(rec, req)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503", rec.Code)
			}
		})
	}
}
