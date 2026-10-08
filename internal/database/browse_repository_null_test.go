package database

import (
	"database/sql"
	"testing"
)

// 回归：browse_history 的部分文本列是后期 ALTER 加的、没有 DEFAULT，
// 任何漏字段的 INSERT 都会留下 NULL。读取时扫进 string 会报
//
//	sql: Scan error on column index 15, name "page_url":
//	     converting NULL to string is unsupported
//
// 整个浏览记录页因此 500。读取查询必须对所有可空文本列做 COALESCE。
func TestListBrowseRecordsToleratesNullTextColumns(t *testing.T) {
	// 用包内的公共 helper 建独立库，不依赖测试执行顺序
	cleanup := setupTestDB(t)
	defer cleanup()

	// 造一条 page_url 为 NULL 的记录，走的是漏字段的 INSERT 路径
	if _, err := db.Exec(`DELETE FROM browse_history WHERE id = 'zz_null_probe'`); err != nil {
		t.Fatalf("清理探针记录失败: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO browse_history (id, title, author, browse_time)
		VALUES ('zz_null_probe', '探针', '探针作者', CURRENT_TIMESTAMP)
	`); err != nil {
		t.Fatalf("插入探针记录失败: %v", err)
	}
	defer db.Exec(`DELETE FROM browse_history WHERE id = 'zz_null_probe'`)

	// 确认这行确实带着 NULL，否则本用例等于没测
	var nulls int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM browse_history WHERE id = 'zz_null_probe' AND page_url IS NULL`,
	).Scan(&nulls); err != nil {
		t.Fatalf("探针查询失败: %v", err)
	}
	if nulls != 1 {
		t.Skip("page_url 有默认值，未能构造 NULL，跳过")
	}

	repo := NewBrowseHistoryRepository()

	// 列表查询：以前这里直接 500
	result, err := repo.List(&PaginationParams{Page: 1, PageSize: 100})
	if err != nil {
		t.Fatalf("List() 因 NULL 返回错误（回归）: %v", err)
	}

	found := false
	for _, rec := range result.Items {
		if rec.ID == "zz_null_probe" {
			found = true
			if rec.PageURL != "" {
				t.Errorf("PageURL = %q, want 空串", rec.PageURL)
			}
			if rec.VideoURL != "" || rec.CoverURL != "" || rec.DecryptKey != "" || rec.AuthorID != "" {
				t.Errorf("可空文本列未归零: %+v", rec)
			}
		}
	}
	if !found {
		t.Fatalf("探针记录未出现在列表中（分页参数可能不对）")
	}

	// 单条查询：同一份 SELECT，同样的坑
	if _, err := repo.GetByID("zz_null_probe"); err != nil {
		t.Fatalf("GetByID() 因 NULL 返回错误（回归）: %v", err)
	}
}

var _ = sql.ErrNoRows
