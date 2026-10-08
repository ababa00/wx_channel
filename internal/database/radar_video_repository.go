package database

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"wx_channel/internal/utils"
)

// radarVideoColumns is the canonical column list, shared by every read so the
// scan order below can never drift out of sync with the SELECT.
const radarVideoColumns = `
	id, video_id, target_id, username, author_name, author_avatar, nonce_id, title,
	media_type, createtime, cover_url, thumb_url, video_url, decrypt_key,
	file_size, duration, resolution, file_format,
	read_count, like_count, comment_count, fav_count, forward_count,
	ip_region, page_url, is_new, raw, first_seen_at, last_seen_at, created_at, updated_at`

// radarVideoInsertColumns is the write-side column list. It deliberately
// excludes is_new, which is pinned to 1 on insert so the "new video" marker is
// set exactly once and stays sticky afterwards.
var radarVideoInsertColumns = []string{
	"id", "video_id", "target_id", "username", "author_name", "author_avatar", "nonce_id", "title",
	"media_type", "createtime", "cover_url", "thumb_url", "video_url", "decrypt_key",
	"file_size", "duration", "resolution", "file_format",
	"read_count", "like_count", "comment_count", "fav_count", "forward_count",
	"ip_region", "page_url", "raw", "first_seen_at", "last_seen_at", "created_at", "updated_at",
}

// placeholders builds a "?, ?, ..." list of n bind markers, so the INSERT never
// depends on hand-counted question marks staying in sync with the column list.
func placeholders(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "?"
	}
	return strings.Join(parts, ", ")
}

// RadarVideoRepository 处理雷达采集到的视频数据的数据库操作
type RadarVideoRepository struct{}

// NewRadarVideoRepository 创建一个新的雷达视频仓库
func NewRadarVideoRepository() *RadarVideoRepository {
	return &RadarVideoRepository{}
}

func (r *RadarVideoRepository) scanVideo(scanner interface{ Scan(...interface{}) error }) (*RadarVideo, error) {
	var v RadarVideo
	var isNew int
	var firstSeen, lastSeen sql.NullTime

	err := scanner.Scan(
		&v.ID, &v.VideoID, &v.TargetID, &v.Username, &v.AuthorName, &v.AuthorAvatar, &v.NonceID, &v.Title,
		&v.MediaType, &v.CreateTime, &v.CoverURL, &v.ThumbURL, &v.VideoURL, &v.DecryptKey,
		&v.FileSize, &v.Duration, &v.Resolution, &v.FileFormat,
		&v.ReadCount, &v.LikeCount, &v.CommentCount, &v.FavCount, &v.ForwardCount,
		&v.IPRegion, &v.PageURL, &isNew, &v.Raw, &firstSeen, &lastSeen, &v.CreatedAt, &v.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	v.IsNew = isNew != 0
	if firstSeen.Valid {
		v.FirstSeenAt = firstSeen.Time
	}
	if lastSeen.Valid {
		v.LastSeenAt = lastSeen.Time
	}
	return &v, nil
}

// Upsert 写入或更新一条雷达视频。
// 首次写入时 is_new 置 1 并固定下来（sticky），之后的轮询只刷新互动数与
// last_seen_at，这样「仅看新视频」筛选不会因为重复采集而逐渐清空。
// 返回值 isNew 表示本次调用是否为该视频的首次发现。
func (r *RadarVideoRepository) Upsert(v *RadarVideo) (isNew bool, err error) {
	if v.VideoID == "" {
		return false, fmt.Errorf("radar video: empty video_id")
	}

	tx, err := db.Begin()
	if err != nil {
		return false, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	var existingID string
	err = tx.QueryRow("SELECT id FROM radar_videos WHERE video_id = ?", v.VideoID).Scan(&existingID)
	if err != nil && err != sql.ErrNoRows {
		return false, fmt.Errorf("failed to look up radar video: %w", err)
	}
	// isNew 是命名返回值，默认为 false；只有确认没查到旧行才是首次发现。
	isNew = err == sql.ErrNoRows

	now := time.Now().Format(time.RFC3339)

	if isNew {
		if v.ID == "" {
			v.ID = utils.RandomString(16)
		}
		cols := append(append([]string{}, radarVideoInsertColumns...), "is_new")
		query := fmt.Sprintf("INSERT INTO radar_videos (%s) VALUES (%s, 1)",
			strings.Join(cols, ", "), placeholders(len(radarVideoInsertColumns)))
		_, err = tx.Exec(query,
			v.ID, v.VideoID, v.TargetID, v.Username, v.AuthorName, v.AuthorAvatar, v.NonceID, v.Title,
			v.MediaType, v.CreateTime, v.CoverURL, v.ThumbURL, v.VideoURL, v.DecryptKey,
			v.FileSize, v.Duration, v.Resolution, v.FileFormat,
			v.ReadCount, v.LikeCount, v.CommentCount, v.FavCount, v.ForwardCount,
			v.IPRegion, v.PageURL, v.Raw, now, now, now, now,
		)
	} else {
		// is_new is intentionally left untouched on update.
		query := `
			UPDATE radar_videos SET
				target_id = ?, username = ?, author_name = ?, author_avatar = ?,
				nonce_id = ?, title = ?, media_type = ?, createtime = ?,
				cover_url = ?, thumb_url = ?, video_url = ?, decrypt_key = ?,
				file_size = ?, duration = ?, resolution = ?, file_format = ?,
				read_count = ?, like_count = ?, comment_count = ?, fav_count = ?, forward_count = ?,
				ip_region = ?, page_url = ?, raw = ?, last_seen_at = ?, updated_at = ?
			WHERE id = ?`
		_, err = tx.Exec(query,
			v.TargetID, v.Username, v.AuthorName, v.AuthorAvatar,
			v.NonceID, v.Title, v.MediaType, v.CreateTime,
			v.CoverURL, v.ThumbURL, v.VideoURL, v.DecryptKey,
			v.FileSize, v.Duration, v.Resolution, v.FileFormat,
			v.ReadCount, v.LikeCount, v.CommentCount, v.FavCount, v.ForwardCount,
			v.IPRegion, v.PageURL, v.Raw, now, now, existingID,
		)
	}

	if err != nil {
		return false, fmt.Errorf("failed to upsert radar video %s: %w", v.VideoID, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("failed to commit radar video upsert: %w", err)
	}
	return isNew, nil
}

// GetByVideoID 按视频 ID 查询单条记录
func (r *RadarVideoRepository) GetByVideoID(videoID string) (*RadarVideo, error) {
	query := `SELECT ` + radarVideoColumns + ` FROM radar_videos WHERE video_id = ?`
	row := db.QueryRow(query, videoID)
	v, err := r.scanVideo(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get radar video: %w", err)
	}
	return v, nil
}

// radarVideoSortColumns 白名单，杜绝 ORDER BY 注入
var radarVideoSortColumns = map[string]bool{
	"createtime": true, "first_seen_at": true, "like_count": true,
	"comment_count": true, "fav_count": true, "forward_count": true,
	"read_count": true, "duration": true, "title": true,
}

// List 分页查询雷达视频，支持按监控对象、关键字和时间范围过滤
func (r *RadarVideoRepository) List(filter *RadarVideoFilter) (*PagedResult[RadarVideo], error) {
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.PageSize < 1 {
		filter.PageSize = 20
	}
	if filter.PageSize > 200 {
		filter.PageSize = 200
	}
	if !radarVideoSortColumns[filter.SortBy] {
		filter.SortBy = "first_seen_at"
	}

	var where []string
	var args []interface{}

	if filter.TargetID != "" {
		where = append(where, "target_id = ?")
		args = append(args, filter.TargetID)
	}
	if filter.Username != "" {
		where = append(where, "username = ?")
		args = append(args, filter.Username)
	}
	if filter.OnlyNew {
		where = append(where, "is_new = 1")
	}
	if filter.Query != "" {
		where = append(where, "(title LIKE ? OR author_name LIKE ?)")
		like := "%" + filter.Query + "%"
		args = append(args, like, like)
	}
	if filter.StartDay != nil {
		where = append(where, "createtime >= ?")
		args = append(args, filter.StartDay.Unix())
	}
	if filter.EndDay != nil {
		where = append(where, "createtime <= ?")
		args = append(args, filter.EndDay.Unix())
	}

	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}

	var total int64
	if err := db.QueryRow("SELECT COUNT(*) FROM radar_videos"+clause, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("failed to count radar videos: %w", err)
	}

	sortOrder := "DESC"
	if !filter.SortDesc {
		sortOrder = "ASC"
	}
	offset := (filter.Page - 1) * filter.PageSize

	query := fmt.Sprintf(
		"SELECT %s FROM radar_videos%s ORDER BY %s %s LIMIT ? OFFSET ?",
		radarVideoColumns, clause, filter.SortBy, sortOrder)

	rows, err := db.Query(query, append(args, filter.PageSize, offset)...)
	if err != nil {
		return nil, fmt.Errorf("failed to list radar videos: %w", err)
	}
	defer rows.Close()

	items := []RadarVideo{}
	for rows.Next() {
		v, err := r.scanVideo(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan radar video: %w", err)
		}
		items = append(items, *v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate radar videos: %w", err)
	}

	return NewPagedResult(items, total, filter.Page, filter.PageSize), nil
}

// RadarVideoStats 视频数据页签顶部的汇总卡片
type RadarVideoStats struct {
	TotalVideos  int64 `json:"totalVideos"`
	NewVideos    int64 `json:"newVideos"`
	TotalAuthors int64 `json:"totalAuthors"`
	TotalPlays   int64 `json:"totalPlays"`
	TotalLikes   int64 `json:"totalLikes"`
	UpdatedAt    string `json:"updatedAt"`
}

// Stats 汇总雷达采集到的视频数据
func (r *RadarVideoRepository) Stats(targetID string) (*RadarVideoStats, error) {
	clause := ""
	var args []interface{}
	if targetID != "" {
		clause = " WHERE target_id = ?"
		args = append(args, targetID)
	}

	var s RadarVideoStats
	err := db.QueryRow(`
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN is_new = 1 THEN 1 ELSE 0 END), 0),
		       COUNT(DISTINCT username),
		       COALESCE(SUM(read_count), 0),
		       COALESCE(SUM(like_count), 0),
		       COALESCE(MAX(last_seen_at), '')
		FROM radar_videos`+clause, args...).
		Scan(&s.TotalVideos, &s.NewVideos, &s.TotalAuthors, &s.TotalPlays, &s.TotalLikes, &s.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to summarize radar videos: %w", err)
	}
	return &s, nil
}

// DeleteByTarget 删除某个监控目标下的全部视频数据
func (r *RadarVideoRepository) DeleteByTarget(targetID string) error {
	if _, err := db.Exec("DELETE FROM radar_videos WHERE target_id = ?", targetID); err != nil {
		return fmt.Errorf("failed to delete radar videos by target: %w", err)
	}
	return nil
}

// DeleteAll 清空全部视频数据
func (r *RadarVideoRepository) DeleteAll() error {
	if _, err := db.Exec("DELETE FROM radar_videos"); err != nil {
		return fmt.Errorf("failed to delete all radar videos: %w", err)
	}
	return nil
}
