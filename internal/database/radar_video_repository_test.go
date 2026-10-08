package database

import (
	"testing"
	"time"
)

func mustUpsert(t *testing.T, repo *RadarVideoRepository, v *RadarVideo) bool {
	t.Helper()
	isNew, err := repo.Upsert(v)
	if err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}
	return isNew
}

func newTestVideo(videoID string, target *RadarTarget, title string) *RadarVideo {
	return &RadarVideo{
		VideoID:    videoID,
		TargetID:   target.ID,
		Username:   target.Username,
		AuthorName: target.AuthorName,
		Title:      title,
		CreateTime: time.Now().Unix(),
		VideoURL:   "https://example.com/" + videoID,
		LikeCount:  100,
	}
}

// 迁移 21 必须建出 radar_videos 表
func TestRadarVideosTableCreatedByMigration(t *testing.T) {
	cleanup := setupTestDB(t)
	defer cleanup()

	var name string
	err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='radar_videos'").Scan(&name)
	if err != nil {
		t.Fatalf("radar_videos table missing after migration: %v", err)
	}
}

// 首次写入标记为新，重复采集只刷新互动数且 is_new 保持不变
func TestRadarVideoUpsertKeepsNewFlagSticky(t *testing.T) {
	cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewRadarVideoRepository()
	target := &RadarTarget{ID: "t1", Username: "v2_abc", AuthorName: "博主一"}
	if err := NewRadarRepository().Add(target); err != nil {
		t.Fatalf("failed to seed target: %v", err)
	}

	v := newTestVideo("vid1", target, "第一条")
	if !mustUpsert(t, repo, v) {
		t.Fatal("first Upsert should report isNew=true")
	}

	// 模拟下一轮轮询：互动数上涨，is_new 不应被重置
	v.LikeCount = 250
	v.Title = "第一条（已更新）"
	if mustUpsert(t, repo, v) {
		t.Fatal("second Upsert should report isNew=false")
	}

	got, err := repo.GetByVideoID("vid1")
	if err != nil {
		t.Fatalf("GetByVideoID: %v", err)
	}
	if got == nil {
		t.Fatal("video not found after upsert")
	}
	if got.LikeCount != 250 {
		t.Errorf("LikeCount = %d, want 250 (counters must refresh)", got.LikeCount)
	}
	if got.Title != "第一条（已更新）" {
		t.Errorf("Title = %q, want updated title", got.Title)
	}
	if !got.IsNew {
		t.Error("IsNew must stay true once discovered")
	}
	if got.FirstSeenAt.IsZero() {
		t.Error("FirstSeenAt must be set")
	}
	if got.LastSeenAt.IsZero() {
		t.Error("LastSeenAt must be set")
	}
}

// 同一 video_id 重复采集不应产生重复行
func TestRadarVideoUpsertIsIdempotent(t *testing.T) {
	cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewRadarVideoRepository()
	target := &RadarTarget{ID: "t1", Username: "v2_abc", AuthorName: "博主一"}
	_ = NewRadarRepository().Add(target)

	for i := 0; i < 5; i++ {
		mustUpsert(t, repo, newTestVideo("vid1", target, "重复采集"))
	}

	var total int64
	if err := db.QueryRow("SELECT COUNT(*) FROM radar_videos").Scan(&total); err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 1 {
		t.Errorf("row count = %d, want 1 (upsert must not duplicate)", total)
	}
}

// 过滤：关键字、目标账号、仅新
func TestRadarVideoListFilters(t *testing.T) {
	cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewRadarVideoRepository()
	t1 := &RadarTarget{ID: "t1", Username: "v2_a", AuthorName: "博主甲"}
	t2 := &RadarTarget{ID: "t2", Username: "v2_b", AuthorName: "博主乙"}
	_ = NewRadarRepository().Add(t1)
	_ = NewRadarRepository().Add(t2)

	mustUpsert(t, repo, newTestVideo("a1", t1, "甲的爆款视频"))
	mustUpsert(t, repo, newTestVideo("a2", t1, "甲的日常"))
	mustUpsert(t, repo, newTestVideo("b1", t2, "乙的视频"))

	cases := []struct {
		name   string
		filter RadarVideoFilter
		want   int64
	}{
		{"全部", RadarVideoFilter{}, 3},
		{"按目标过滤", RadarVideoFilter{TargetID: "t1"}, 2},
		{"按账号过滤", RadarVideoFilter{Username: "v2_b"}, 1},
		{"关键字命中标题", RadarVideoFilter{Query: "爆款"}, 1},
		{"关键字未命中", RadarVideoFilter{Query: "不存在的词"}, 0},
		{"组合过滤", RadarVideoFilter{TargetID: "t1", Query: "日常"}, 1},
		{"仅新", RadarVideoFilter{OnlyNew: true}, 3},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.filter
			res, err := repo.List(&f)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if res.Total != tc.want {
				t.Errorf("Total = %d, want %d", res.Total, tc.want)
			}
		})
	}
}

// 分页切片必须互不重叠且总数一致
func TestRadarVideoListPagination(t *testing.T) {
	cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewRadarVideoRepository()
	target := &RadarTarget{ID: "t1", Username: "v2_abc", AuthorName: "博主一"}
	_ = NewRadarRepository().Add(target)

	for i := 0; i < 7; i++ {
		id := string(rune('a'+i)) + "-vid"
		mustUpsert(t, repo, newTestVideo(id, target, "视频"+id))
	}

	seen := map[string]bool{}
	for page := 1; page <= 3; page++ {
		f := RadarVideoFilter{}
		f.Page = page
		f.PageSize = 3
		res, err := repo.List(&f)
		if err != nil {
			t.Fatalf("List page %d: %v", page, err)
		}
		if res.Total != 7 {
			t.Errorf("Total = %d, want 7", res.Total)
		}
		if res.TotalPages != 3 {
			t.Errorf("TotalPages = %d, want 3", res.TotalPages)
		}
		for _, item := range res.Items {
			if seen[item.VideoID] {
				t.Errorf("duplicate %s across pages", item.VideoID)
			}
			seen[item.VideoID] = true
		}
	}
	if len(seen) != 7 {
		t.Errorf("saw %d distinct rows across pages, want 7", len(seen))
	}
}

// ORDER BY 只接受白名单列，注入式排序必须被纠正
func TestRadarVideoListRejectsUnknownSortColumn(t *testing.T) {
	cleanup := setupTestDB(t)
	defer cleanup()

	f := RadarVideoFilter{}
	f.SortBy = "createtime; DROP TABLE radar_videos--"
	res, err := NewRadarVideoRepository().List(&f)
	if err != nil {
		t.Fatalf("List should not error on hostile sort column: %v", err)
	}
	if res == nil {
		t.Fatal("expected result")
	}
	// 表必须还在
	var total int64
	if err := db.QueryRow("SELECT COUNT(*) FROM radar_videos").Scan(&total); err != nil {
		t.Fatalf("radar_videos table was damaged: %v", err)
	}
}

// 汇总统计
func TestRadarVideoStats(t *testing.T) {
	cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewRadarVideoRepository()
	t1 := &RadarTarget{ID: "t1", Username: "v2_a", AuthorName: "博主甲"}
	t2 := &RadarTarget{ID: "t2", Username: "v2_b", AuthorName: "博主乙"}
	_ = NewRadarRepository().Add(t1)
	_ = NewRadarRepository().Add(t2)

	a := newTestVideo("a1", t1, "甲一")
	a.ReadCount, a.LikeCount = 1000, 50
	mustUpsert(t, repo, a)

	b := newTestVideo("b1", t2, "乙一")
	b.ReadCount, b.LikeCount = 500, 20
	mustUpsert(t, repo, b)

	stats, err := repo.Stats("")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.TotalVideos != 2 {
		t.Errorf("TotalVideos = %d, want 2", stats.TotalVideos)
	}
	if stats.TotalAuthors != 2 {
		t.Errorf("TotalAuthors = %d, want 2", stats.TotalAuthors)
	}
	if stats.TotalPlays != 1500 {
		t.Errorf("TotalPlays = %d, want 1500", stats.TotalPlays)
	}
	if stats.TotalLikes != 70 {
		t.Errorf("TotalLikes = %d, want 70", stats.TotalLikes)
	}

	scoped, err := repo.Stats("t1")
	if err != nil {
		t.Fatalf("Stats scoped: %v", err)
	}
	if scoped.TotalVideos != 1 || scoped.TotalPlays != 1000 {
		t.Errorf("scoped stats wrong: %+v", scoped)
	}
}

// 空 video_id 必须被拒绝，否则会写出一堆无法检索的脏行
func TestRadarVideoUpsertRejectsEmptyVideoID(t *testing.T) {
	cleanup := setupTestDB(t)
	defer cleanup()

	if _, err := NewRadarVideoRepository().Upsert(&RadarVideo{}); err == nil {
		t.Fatal("expected error for empty video_id")
	}
}
