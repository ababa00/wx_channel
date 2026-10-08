package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"wx_channel/internal/database"
	"wx_channel/internal/utils"
	"wx_channel/internal/websocket"
)

// RadarService 负责定时轮询对标账号的主页，把每条视频的完整数据落库。
// Requirements: Competitor 24-hour Silent Radar
//
// 与原实现的差异：
//   - 不再只取 feed_list 第一页，按 nextMarker 翻页（有上限），减少漏采；
//   - 每条视频的完整字段（互动数、发布时间、位置等）写入 radar_videos 表，
//     而不再塞进 radar_logs.video_list 这种每次轮询就被覆盖的 JSON 字符串；
//   - 下载行为由 radar_auto_download 开关控制，默认关闭，即只采集数据不下载。
type RadarService struct {
	repo         *database.RadarRepository
	videoRepo    *database.RadarVideoRepository
	queueService *QueueService
	hub          *websocket.Hub
	settings     *database.SettingsRepository

	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	wg     sync.WaitGroup

	// scanMu 串行化所有采集动作，保证定时轮询和手动同步不会同时去打微信接口。
	scanMu sync.Mutex
	// scanning 标记当前是否正在采集。手动同步用它判断该拒绝还是排队，
	// 定时轮询则直接跳过本轮——反正手动同步会覆盖同样的目标。
	scanning atomic.Bool

	ticker *time.Ticker
}

// NewRadarService 创建一个新的雷达服务
func NewRadarService(repo *database.RadarRepository, queueService *QueueService, hub *websocket.Hub) *RadarService {
	ctx, cancel := context.WithCancel(context.Background())
	return &RadarService{
		repo:         repo,
		videoRepo:    database.NewRadarVideoRepository(),
		queueService: queueService,
		hub:          hub,
		settings:     database.NewSettingsRepository(),
		ctx:          ctx,
		cancel:       cancel,
	}
}

// Start 启动雷达服务轮询器
func (s *RadarService) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ticker != nil {
		return // 已启动
	}
	if s.ctx == nil || s.ctx.Err() != nil {
		s.ctx, s.cancel = context.WithCancel(context.Background())
	}

	// 默认每分钟检查一次，但实际是否触发取决于每个 target 的 interval_minutes
	s.ticker = time.NewTicker(time.Minute)
	s.wg.Add(1)

	go func() {
		defer s.wg.Done()
		utils.LogInfo("Radar Service (24h静默雷达) 已启动")

		// 启动时立即执行一次检测（延迟10秒，等待WebSocket连接建立）
		time.Sleep(10 * time.Second)
		s.checkTargets()

		for {
			select {
			case <-s.ctx.Done():
				utils.LogInfo("Radar Service 已停止")
				return
			case <-s.ticker.C:
				s.checkTargets()
			}
		}
	}()
}

// Stop 停止雷达服务
func (s *RadarService) Stop() {
	s.mu.Lock()
	if s.ticker == nil {
		s.mu.Unlock()
		return
	}
	cancel := s.cancel
	ticker := s.ticker
	s.ticker = nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if ticker != nil {
		ticker.Stop()
	}
	s.wg.Wait()
}

// checkTargets 遍历并检查所有活动的雷达目标
func (s *RadarService) checkTargets() {
	// 手动同步正在跑就跳过本轮：两者目标重叠，重复打接口没有意义。
	if !s.scanning.CompareAndSwap(false, true) {
		return
	}
	defer s.scanning.Store(false)

	s.scanMu.Lock()
	defer s.scanMu.Unlock()

	targets, err := s.repo.GetActive()
	if err != nil {
		utils.LogError("获取活动雷达目标失败: %v", err)
		return
	}

	if len(targets) == 0 {
		return
	}

	now := time.Now()
	hasClient := s.hub.ClientCount() > 0

	for _, target := range targets {
		// 检查是否到了该检测的时间
		if target.LastCheckTime != nil {
			elapsed := now.Sub(*target.LastCheckTime)
			if elapsed < time.Duration(target.IntervalMinutes)*time.Minute {
				continue // 还没到时间
			}
		}

		if !hasClient {
			// 刻意不推进 last_check_time：微信重连后立刻补采，
			// 否则会白白丢掉一个完整轮询间隔的数据。
			_ = s.repo.AddLog(&database.RadarLog{
				TargetID:     target.ID,
				CheckTime:    now,
				Status:       "error",
				ErrorMessage: "微信客户端未连接或被关闭，请重新注入",
			})
			continue
		}

		// 执行检测
		s.processTarget(target)
	}
}

// 手动同步的失败原因，都可以直接回给前端展示
var (
	// ErrSyncRunning 表示已经有一次采集在进行中
	ErrSyncRunning = errors.New("已有采集任务正在进行，请稍后再试")
	// ErrNoWeChatClient 表示微信客户端未连接，此时采集必然失败
	ErrNoWeChatClient = errors.New("微信客户端未连接，请先在微信里打开视频号页面并完成注入")
)

// TargetSyncResult 描述单个账号一次采集的结果
type TargetSyncResult struct {
	TargetID string `json:"targetId"`
	Author   string `json:"authorName"`
	// Found 是本次拉到的视频条数
	Found int `json:"found"`
	// NewVideos 是首次发现的条数
	NewVideos int `json:"newVideos"`
	// UpdatedVideos 是已存在、互动数被刷新的条数
	UpdatedVideos int `json:"updatedVideos"`
	// Queued 是加入下载队列的条数（自动下载关闭时恒为 0）
	Queued int    `json:"queued"`
	Error  string `json:"error,omitempty"`
}

// SyncResult 汇总一次手动同步
type SyncResult struct {
	Targets      int                `json:"targets"`
	Found        int                `json:"found"`
	NewVideos    int                `json:"newVideos"`
	Updated      int                `json:"updatedVideos"`
	Queued       int                `json:"queued"`
	DurationMS   int64              `json:"durationMs"`
	Details      []TargetSyncResult `json:"details"`
}

// SyncNow 立即对所有活动目标采集一次，忽略各目标的 interval_minutes。
// 已存在的视频同样会被重新采集，用于刷新点赞等互动数。
// 同步是同步执行的：每个账号翻满配置页数，逐个账号处理完才返回。
func (s *RadarService) SyncNow() (*SyncResult, error) {
	// 先看是否已有采集在跑：有的话说明微信本来就连着，报「未连接」是误导。
	if !s.scanning.CompareAndSwap(false, true) {
		return nil, ErrSyncRunning
	}
	defer s.scanning.Store(false)

	// hub 理论上总由 NewRadarService 注入，这里判空只是防御，
	// 避免构造不当导致整条链路 panic。
	if s.hub == nil || s.hub.ClientCount() == 0 {
		return nil, ErrNoWeChatClient
	}

	s.scanMu.Lock()
	defer s.scanMu.Unlock()

	started := time.Now()

	targets, err := s.repo.GetActive()
	if err != nil {
		return nil, fmt.Errorf("获取活动雷达目标失败: %w", err)
	}

	result := &SyncResult{
		Targets: len(targets),
		Details: make([]TargetSyncResult, 0, len(targets)),
	}

	for _, target := range targets {
		utils.LogInfo("[Radar] 手动同步开始: %s", target.AuthorName)
		detail := s.processTarget(target)
		result.Details = append(result.Details, detail)
		result.Found += detail.Found
		result.NewVideos += detail.NewVideos
		result.Updated += detail.UpdatedVideos
		result.Queued += detail.Queued
	}

	result.DurationMS = time.Since(started).Milliseconds()
	utils.LogInfo("[Radar] 手动同步完成：%d 个账号，拉取 %d 条，新增 %d 条，刷新 %d 条，耗时 %dms",
		result.Targets, result.Found, result.NewVideos, result.Updated, result.DurationMS)

	return result, nil
}

// radarCaptureOptions 读取雷达采集开关
func (s *RadarService) radarCaptureOptions() (autoDownload bool, maxPages int) {
	autoDownload = database.DefaultRadarAutoDownload
	maxPages = database.DefaultRadarMaxPages

	if s.settings == nil {
		return
	}
	if v, err := s.settings.GetBool(database.SettingKeyRadarAutoDownload, database.DefaultRadarAutoDownload); err == nil {
		autoDownload = v
	}
	if v, err := s.settings.GetInt(database.SettingKeyRadarMaxPages, database.DefaultRadarMaxPages); err == nil && v > 0 {
		if v > database.MaxRadarMaxPages {
			v = database.MaxRadarMaxPages
		}
		maxPages = v
	}
	return
}

// feedListResponse 与注入端 finderUserPage 的返回结构对应。
// Go 侧拿到的是 { "data": { ... } } 这一层。
type feedListResponse struct {
	Data struct {
		BaseResponse struct {
			Ret int `json:"Ret"`
		} `json:"BaseResponse"`
		ObjectList []map[string]interface{} `json:"objectList"`
		Object     []map[string]interface{} `json:"object"`

		// 翻页游标。微信在不同接口/版本上用过不同的名字，这里全部兼容。
		LastBuffer string `json:"lastBuffer"`
		LastBuff   string `json:"lastBuff"`
		NextMarker string `json:"nextMarker"`
		Buffer     string `json:"buffer"`

		// 必须是指针：用值类型时，响应里没有 hasMore 字段就会保持 false，
		// 于是"本页取完"和"微信明说没有更多"被混为一谈，第一页就退出。
		// nil 表示微信压根没返回这个字段，此时只以游标为准。
		HasMore *bool `json:"hasMore"`
	} `json:"data"`
}

// nextMarker 提取下一页游标，没有则返回空串
func (r *feedListResponse) nextMarker() string {
	for _, candidate := range []string{r.Data.LastBuffer, r.Data.LastBuff, r.Data.NextMarker, r.Data.Buffer} {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

// hasMore 明确返回 false 才算没有更多；字段缺失时交给游标判断
func (r *feedListResponse) hasMore() bool {
	return r.Data.HasMore == nil || *r.Data.HasMore
}

// feedItems 返回对象列表，兼容 objectList / object 两种字段名
func (r *feedListResponse) feedItems() []map[string]interface{} {
	if len(r.Data.ObjectList) > 0 {
		return r.Data.ObjectList
	}
	return r.Data.Object
}

// feedDataKeys 取出 data 对象的全部顶层字段名。
// 翻页游标的名字没有文档可查，只能在停止翻页时把真实字段名打进日志，
// 否则就只能一个一个猜。
func feedDataKeys(raw []byte) []string {
	var probe struct {
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil
	}
	keys := make([]string, 0, len(probe.Data))
	for k := range probe.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// feedFetchResult 记录一次翻页拉取的结果，用于日志诊断。
type feedFetchResult struct {
	Feeds []map[string]interface{}
	// Pages 是实际成功翻的页数，PageSizes 是每一页返回的条数。
	// 微信不暴露页大小参数，只能实测，所以这里把它记下来。
	Pages     int
	PageSizes []int
	// StopReason 非空说明为什么提前停止翻页。
	StopReason string
	// DataKeys 是响应 data 对象的顶层字段名，用来定位翻页游标的真实名字。
	DataKeys []string
	// StoppedEarly 非空表示翻页中途出错，已采到的数据仍然有效。
	StoppedEarly error
}

// fetchFeeds 逐页拉取账号主页视频，最多 maxPages 页。
// 单页失败不会丢弃已经取到的数据：返回已采集部分并把错误一并交回调用方记录。
func (s *RadarService) fetchFeeds(username string, maxPages int) (*feedFetchResult, error) {
	result := &feedFetchResult{}
	marker := ""
	seenMarkers := map[string]bool{}

	for page := 1; page <= maxPages; page++ {
		body := websocket.FeedListBody{
			Username:   username,
			NextMarker: marker,
		}

		data, err := s.hub.CallAPI("key:channels:feed_list", body, 30*time.Second)
		if err != nil {
			result.StoppedEarly = err
			result.StopReason = "调用失败"
			return result, err
		}

		var parsed feedListResponse
		if err := json.Unmarshal(data, &parsed); err != nil {
			result.StoppedEarly = fmt.Errorf("解析返回数据失败: %w", err)
			result.StopReason = "响应不是合法 JSON"
			return result, result.StoppedEarly
		}

		if parsed.Data.BaseResponse.Ret != 0 {
			result.StoppedEarly = fmt.Errorf("微信接口返回失败，状态码: %d (可能是请求过于频繁或账号异常)", parsed.Data.BaseResponse.Ret)
			result.StopReason = fmt.Sprintf("微信返回 Ret=%d", parsed.Data.BaseResponse.Ret)
			return result, result.StoppedEarly
		}

		items := parsed.feedItems()
		result.Feeds = append(result.Feeds, items...)
		result.Pages = page
		result.PageSizes = append(result.PageSizes, len(items))
		if result.DataKeys == nil {
			result.DataKeys = feedDataKeys(data)
		}

		if len(items) == 0 {
			result.StopReason = "本页返回 0 条"
			break
		}
		if !parsed.hasMore() {
			result.StopReason = "微信明确返回 hasMore=false"
			break
		}

		next := parsed.nextMarker()
		if next == "" {
			// 最常见的情形：游标字段名没匹配上。用真实字段名把这行日志补全，
			// 下一次就能直接看出该认哪个字段，不用再猜。
			result.StopReason = "未取到翻页游标（候选字段名均无值）"
			break
		}
		if seenMarkers[next] {
			result.StopReason = "游标未推进，服务端不再翻页"
			break
		}
		seenMarkers[next] = true
		marker = next
	}

	return result, nil
}

// ---------- 字段解析辅助 ----------

func str(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func num(m map[string]interface{}, key string) int64 {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return n
		}
	}
	return 0
}

func sub(m map[string]interface{}, key string) map[string]interface{} {
	if m == nil {
		return nil
	}
	if v, ok := m[key].(map[string]interface{}); ok {
		return v
	}
	return nil
}

func subList(m map[string]interface{}, key string) []interface{} {
	if m == nil {
		return nil
	}
	if v, ok := m[key].([]interface{}); ok {
		return v
	}
	return nil
}

// extractResolution 按 handlers 包浏览记录里的同一优先级解析分辨率：
// media 宽高 → spec 宽高 → videoResolution 字符串
func extractResolution(media map[string]interface{}) string {
	if w, h := num(media, "width"), num(media, "height"); w > 0 && h > 0 {
		return fmt.Sprintf("%dx%d", w, h)
	}
	for _, item := range subList(media, "spec") {
		specItem, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if w, h := num(specItem, "width"), num(specItem, "height"); w > 0 && h > 0 {
			return fmt.Sprintf("%dx%d", w, h)
		}
	}
	return str(media, "videoResolution")
}

// extractFileFormat 取 spec 里第一个非空的 fileFormat（xWT111 之类）
func extractFileFormat(media map[string]interface{}) string {
	for _, item := range subList(media, "spec") {
		if specItem, ok := item.(map[string]interface{}); ok {
			if f := str(specItem, "fileFormat"); f != "" {
				return f
			}
		}
	}
	return ""
}

// extractDuration 统一换算成秒。优先 spec[0].durationMs，其次 videoPlayLen*1000，
// 最后退回 videoDuration（部分版本本身就是秒）。
func extractDuration(media map[string]interface{}) int64 {
	if spec := subList(media, "spec"); len(spec) > 0 {
		if specItem, ok := spec[0].(map[string]interface{}); ok {
			if ms := num(specItem, "durationMs"); ms > 0 {
				return ms / 1000
			}
		}
	}
	if s := num(media, "videoPlayLen"); s > 0 {
		return s
	}
	return num(media, "videoDuration")
}

// extractIPRegion 发布位置，例如 "广东"
func extractIPRegion(feed map[string]interface{}) string {
	info := sub(feed, "ipRegionInfo")
	if info == nil {
		return ""
	}
	if country := str(info, "country"); country != "" {
		return country
	}
	return str(info, "province")
}

// buildRadarVideo 把一条 feed 对象转成入库结构
func buildRadarVideo(feed map[string]interface{}, target database.RadarTarget) *database.RadarVideo {
	videoID := str(feed, "id")
	if videoID == "" {
		return nil
	}

	contact := sub(feed, "contact")
	authorName := str(contact, "nickname")
	if authorName == "" {
		authorName = target.AuthorName
	}

	v := &database.RadarVideo{
		VideoID:      videoID,
		TargetID:     target.ID,
		Username:     target.Username,
		AuthorName:   authorName,
		AuthorAvatar: str(contact, "headUrl"),
		NonceID:      str(feed, "objectNonceId"),
		Title:        str(sub(feed, "objectDesc"), "description"),
		CreateTime:   num(feed, "createtime"),
		// 互动数据直接取自 feed 顶层，与注入端 format_feed 的取法一致
		ReadCount:    num(feed, "readCount"),
		LikeCount:    num(feed, "likeCount"),
		CommentCount: num(feed, "commentCount"),
		FavCount:     num(feed, "favCount"),
		ForwardCount: num(feed, "forwardCount"),
		IPRegion:     extractIPRegion(feed),
		PageURL:      str(feed, "url"),
	}

	desc := sub(feed, "objectDesc")
	if desc != nil {
		v.MediaType = int(num(desc, "mediaType"))
		// 遍历媒体列表，优先取真正带下载地址的视频媒体。
		// 上游 v5.7.11 修复: 不再硬取第一条，并统一用 JoinURLToken 处理 token。
		if mediaList := subList(desc, "media"); len(mediaList) > 0 {
			for _, mediaItem := range mediaList {
				media, ok := mediaItem.(map[string]interface{})
				if !ok {
					continue
				}
				rawURL := str(media, "url")
				urlToken := str(media, "urlToken")
				if urlToken == "" {
					urlToken = str(media, "url_token")
				}
				candidateURL := utils.JoinURLToken(rawURL, urlToken)
				if v.VideoURL == "" && candidateURL != "" {
					v.VideoURL = candidateURL
				}
				if v.VideoURL == "" {
					continue
				}
				v.DecryptKey = str(media, "decodeKey")
				v.CoverURL = str(media, "coverUrl")
				v.ThumbURL = str(media, "thumbUrl")
				v.FileSize = num(media, "fileSize")
				v.Duration = extractDuration(media)
				v.Resolution = extractResolution(media)
				v.FileFormat = extractFileFormat(media)
				// 已取到有效媒体，停止遍历（上游 v5.7.11 行为）
				break
			}
		}
	}

	if v.Title == "" {
		v.Title = fmt.Sprintf("RadarV_%s", videoID)
	}
	if v.ThumbURL == "" {
		v.ThumbURL = v.CoverURL
	}
	if v.CoverURL == "" {
		v.CoverURL = v.ThumbURL
	}

	// 保留原始 feed，后续新增字段时无需重新采集
	if raw, err := json.Marshal(feed); err == nil {
		v.Raw = string(raw)
	}

	return v
}

// processTarget 处理单个雷达监控目标的拉取与入库逻辑
func (s *RadarService) processTarget(target database.RadarTarget) TargetSyncResult {
	result := TargetSyncResult{TargetID: target.ID, Author: target.AuthorName}
	utils.LogInfo("[Radar] 开始检测账号: %s (%s)", target.AuthorName, target.Username)

	now := time.Now()
	if err := s.repo.UpdateLastCheckTime(target.ID, now); err != nil {
		utils.LogError("[Radar] 更新检测时间失败 [%s]: %v", target.ID, err)
	}

	radarLog := &database.RadarLog{
		TargetID:  target.ID,
		CheckTime: now,
		Status:    "success",
	}

	autoDownload, maxPages := s.radarCaptureOptions()

	// 1. 逐页拉取账号主页视频
	pageResult, err := s.fetchFeeds(target.Username, maxPages)
	feeds := pageResult.Feeds
	count := len(feeds)

	if err != nil {
		radarLog.Status = "error"
		if strings.Contains(err.Error(), "no available client") {
			radarLog.ErrorMessage = "微信客户端未连接或已退出"
			utils.LogWarn("[Radar] 检测失败 [%s]: %s", target.AuthorName, radarLog.ErrorMessage)
		} else {
			radarLog.ErrorMessage = err.Error()
			utils.LogError("[Radar] 获取视频列表失败 [%s]: %v", target.AuthorName, err)
		}
		// 即使中途失败，已经取到的页也照常入库，不让已采数据白费
		if count == 0 {
			result.Error = err.Error()
			_ = s.repo.AddLog(radarLog)
			return result
		}
		utils.LogWarn("[Radar] 账号 [%s] 翻页中断，已采集 %d 条: %v", target.AuthorName, count, err)
	}

	radarLog.FoundVideos = count
	result.Found = count

	if count == 0 {
		if err != nil {
			result.Error = err.Error()
		}
		utils.LogInfo("[Radar] 账号 [%s] 暂无视频数据", target.AuthorName)
		_ = s.repo.AddLog(radarLog)
		return result
	}

	// 2. 逐条解析并入库
	newVideoCount := 0
	updatedCount := 0
	queuedCount := 0
	var videoSummaries []database.RadarVideoSummary

	for _, feed := range feeds {
		v := buildRadarVideo(feed, target)
		if v == nil {
			continue
		}

		isNew, err := s.videoRepo.Upsert(v)
		if err != nil {
			utils.LogError("[Radar] 写入视频数据失败 [%s]-[%s]: %v", target.AuthorName, v.VideoID, err)
			continue
		}
		if isNew {
			newVideoCount++
			utils.LogInfo("[Radar] 发现新视频 [%s]: %s", target.AuthorName, v.Title)
		} else {
			// 已存在的视频同样刷一遍互动数，这是「同步」按钮的主要价值所在
			updatedCount++
		}

		videoSummaries = append(videoSummaries, database.RadarVideoSummary{
			VideoID: v.VideoID,
			Title:   v.Title,
			IsNew:   isNew,
		})

		// 只有显式打开自动下载时才入队，否则雷达纯采集、不落视频文件
		if autoDownload && isNew && v.VideoURL != "" {
			if _, err := s.queueService.AddToQueue([]VideoInfo{{
				VideoID:    v.VideoID,
				Title:      v.Title,
				Author:     v.AuthorName,
				VideoURL:   v.VideoURL,
				CoverURL:   v.CoverURL,
				Size:       v.FileSize,
				DecryptKey: v.DecryptKey,
				Duration:   v.Duration,
				Resolution: v.Resolution,
			}}); err != nil {
				utils.LogError("[Radar] 添加视频到下载队列失败 [%s]-[%s]: %v", target.AuthorName, v.Title, err)
			} else {
				queuedCount++
			}
		}
	}

	radarLog.NewVideos = newVideoCount
	result.NewVideos = newVideoCount
	result.UpdatedVideos = updatedCount
	result.Queued = queuedCount
	if err != nil {
		result.Error = err.Error()
	}

	if len(videoSummaries) > 0 {
		if b, err := json.Marshal(videoSummaries); err == nil {
			radarLog.VideoList = string(b)
		}
	}

	_ = s.repo.AddLog(radarLog)

	// 把页数、每页条数和真实字段名打出来：页大小微信不暴露，只能实测；
	// 翻页游标的字段名也没有文档，只能从响应里认。
	utils.LogInfo("[Radar] 账号 [%s] 翻 %d/%d 页，每页条数 %v，共 %d 条；新增 %d 条，刷新 %d 条，入队 %d 条（自动下载=%v）",
		target.AuthorName, pageResult.Pages, maxPages, pageResult.PageSizes, count,
		newVideoCount, updatedCount, queuedCount, autoDownload)

	if pageResult.Pages < maxPages {
		utils.LogWarn("[Radar] 账号 [%s] 提前停止翻页：%s", target.AuthorName, pageResult.StopReason)
		utils.LogWarn("[Radar] 账号 [%s] feed_list 响应的 data 字段：%v", target.AuthorName, pageResult.DataKeys)
	}

	return result
}
