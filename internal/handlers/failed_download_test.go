package handlers

import (
	"testing"

	"wx_channel/internal/database"
	"wx_channel/internal/services"
)

// 前端在下载启动后轮询 /api/downloads/<videoId> 等结果。如果后端失败时什么都
// 不写，这个 ID 就永远没有记录，前端只能每 2 秒拿 404 空转到超时 —— 实测某个
// 失败任务被打了 283 次 404，全日志 1452 次单条查询里 93.5% 是 404。
//
// 所以失败也必须落一条 status=failed 的记录，让轮询能收尾、用户能看到原因。
func TestRecordFailedDownloadWritesFailedRecord(t *testing.T) {
	cleanup := databaseTestSetupForHandlers(t)
	defer cleanup()

	h := &UploadHandler{downloadService: services.NewDownloadRecordService()}

	req := DownloadVideoRequest{
		VideoID:    "fail-probe-1",
		Title:      "失败的视频",
		Author:     "作者",
		Resolution: "1080x1920",
		LikeCount:  7,
	}
	h.recordFailedDownload(req, "下载失败: 链接已过期")

	rec, err := h.downloadService.GetByID("fail-probe-1")
	if err != nil {
		t.Fatalf("GetByID 报错: %v", err)
	}
	if rec == nil {
		t.Fatal("失败路径没有落记录 —— 前端会一直轮询到 404")
	}
	if rec.Status != database.DownloadStatusFailed {
		t.Errorf("Status = %q, want %q", rec.Status, database.DownloadStatusFailed)
	}
	if rec.ErrorMessage == "" {
		t.Error("ErrorMessage 为空，用户看不到失败原因")
	}
	if rec.Title != "失败的视频" {
		t.Errorf("Title = %q, want %q", rec.Title, "失败的视频")
	}
	if rec.LikeCount != 7 {
		t.Errorf("LikeCount = %d, want 7", rec.LikeCount)
	}
	if rec.FilePath != "" {
		t.Errorf("失败记录不应带文件路径, got %q", rec.FilePath)
	}
}

// videoId 为空时无法被轮询，且空 id 会撞主键，应当直接跳过而不是写脏数据
func TestRecordFailedDownloadSkipsEmptyVideoID(t *testing.T) {
	cleanup := databaseTestSetupForHandlers(t)
	defer cleanup()

	h := &UploadHandler{downloadService: services.NewDownloadRecordService()}

	h.recordFailedDownload(DownloadVideoRequest{VideoID: "", Title: "无 ID"}, "下载失败")

	rec, err := h.downloadService.GetByID("")
	if err != nil {
		t.Fatalf("GetByID 报错: %v", err)
	}
	if rec != nil {
		t.Fatalf("空 videoId 不应落记录, got %+v", rec)
	}
}

// downloadService 为 nil 时不能 panic（部分装配路径可能没注入）
func TestRecordFailedDownloadToleratesNilService(t *testing.T) {
	h := &UploadHandler{}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("downloadService 为 nil 时 panic 了: %v", r)
		}
	}()
	h.recordFailedDownload(DownloadVideoRequest{VideoID: "x", Title: "y"}, "z")
}
