package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"wx_channel/internal/database"
	"wx_channel/internal/response"
)

// 雷达视频数据页签的 API。
// 与 radar.go 里的目标管理分开，因为两者读写的是完全不同的表。

// GetRadarVideos 分页查询雷达采集到的视频数据
func (h *RadarServiceAPI) GetRadarVideos(w http.ResponseWriter, r *http.Request) {
	// videoRepo 由构造函数注入，但这里仍要判空：一旦某个装配路径漏掉赋值，
	// 下面 .List() 就会空指针 panic，曾在 /api/v1/radar/videos 上真实发生 4 次。
	if h == nil || h.videoRepo == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, 503, "雷达数据服务未就绪")
		return
	}

	q := r.URL.Query()

	filter := &database.RadarVideoFilter{}
	filter.Page, _ = strconv.Atoi(q.Get("page"))
	filter.PageSize, _ = strconv.Atoi(q.Get("pageSize"))
	filter.SortBy = q.Get("sortBy")
	// 默认按发现时间倒序：最新的采集结果排在最前
	filter.SortDesc = q.Get("sortDesc") != "false"
	filter.TargetID = strings.TrimSpace(q.Get("targetId"))
	filter.Username = strings.TrimSpace(q.Get("username"))
	filter.Query = strings.TrimSpace(q.Get("query"))
	filter.OnlyNew = q.Get("onlyNew") == "true"

	// parseDayParam 对空参数返回 (nil, nil)，所以必须判空后才能解引用
	if v, err := parseDayParam(q.Get("startDay")); err == nil && v != nil {
		filter.StartDay = v
	}
	if v, err := parseDayParam(q.Get("endDay")); err == nil && v != nil {
		// endDay 含当天整天，补到 23:59:59
		end := v.Add(24*time.Hour - time.Second)
		filter.EndDay = &end
	}

	result, err := h.videoRepo.List(filter)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "获取视频数据失败")
		return
	}
	response.Success(w, result)
}

// parseDayParam 解析 YYYY-MM-DD，按本地时区起点
func parseDayParam(s string) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	t, err := time.ParseInLocation("2006-01-02", s, time.Local)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// GetRadarVideoStats 汇总卡片数据
func (h *RadarServiceAPI) GetRadarVideoStats(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.videoRepo == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, 503, "雷达数据服务未就绪")
		return
	}

	stats, err := h.videoRepo.Stats(strings.TrimSpace(r.URL.Query().Get("targetId")))
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "获取统计数据失败")
		return
	}
	response.Success(w, stats)
}

// DeleteRadarVideos 清空采集到的视频数据（targetId 非空时只清该监控对象）
func (h *RadarServiceAPI) DeleteRadarVideos(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.videoRepo == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, 503, "雷达数据服务未就绪")
		return
	}

	targetID := strings.TrimSpace(r.URL.Query().Get("targetId"))

	if targetID != "" {
		if err := h.videoRepo.DeleteByTarget(targetID); err != nil {
			response.Error(w, http.StatusInternalServerError, "删除视频数据失败")
			return
		}
	} else if err := h.videoRepo.DeleteAll(); err != nil {
		response.Error(w, http.StatusInternalServerError, "删除视频数据失败")
		return
	}

	response.Success(w, nil)
}

// RadarCaptureConfig 雷达采集开关，供控制台页签使用
type RadarCaptureConfig struct {
	AutoDownload bool `json:"autoDownload"`
	MaxPages     int  `json:"maxPages"`
}

// GetRadarCaptureConfig 读取采集开关
func (h *RadarServiceAPI) GetRadarCaptureConfig(w http.ResponseWriter, r *http.Request) {
	settings := database.NewSettingsRepository()

	autoDownload, _ := settings.GetBool(database.SettingKeyRadarAutoDownload, database.DefaultRadarAutoDownload)
	maxPages, _ := settings.GetInt(database.SettingKeyRadarMaxPages, database.DefaultRadarMaxPages)
	if maxPages < 1 {
		maxPages = database.DefaultRadarMaxPages
	}

	response.Success(w, RadarCaptureConfig{
		AutoDownload: autoDownload,
		MaxPages:     maxPages,
	})
}

// UpdateRadarCaptureConfig 保存采集开关
func (h *RadarServiceAPI) UpdateRadarCaptureConfig(w http.ResponseWriter, r *http.Request) {
	var req RadarCaptureConfig
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, "请求参数解析失败")
		return
	}
	if req.MaxPages < 1 {
		req.MaxPages = 1
	}
	if req.MaxPages > database.MaxRadarMaxPages {
		req.MaxPages = database.MaxRadarMaxPages
	}

	settings := database.NewSettingsRepository()
	if err := settings.SetBool(database.SettingKeyRadarAutoDownload, req.AutoDownload); err != nil {
		response.Error(w, http.StatusInternalServerError, "保存采集设置失败")
		return
	}
	if err := settings.SetInt(database.SettingKeyRadarMaxPages, req.MaxPages); err != nil {
		response.Error(w, http.StatusInternalServerError, "保存采集设置失败")
		return
	}

	response.Success(w, req)
}
