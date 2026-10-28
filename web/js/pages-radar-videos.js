// ============================================
// 雷达视频数据页签
//
// 展示对标账号主页采集到的每条视频的完整数据（互动数、发布时间、位置等）。
// 与「浏览记录」不同，这里是后台雷达自动轮询累积下来的历史快照，
// 同一视频会被反复刷新互动数，但首次发现时间保持不变。
// ============================================

const radarVideoState = {
    items: [],
    currentPage: 1,
    pageSize: 20,
    totalCount: 0,
    totalPages: 0,
    searchQuery: '',
    targetId: '',
    onlyNew: false,
    // 发布时间区间（YYYY-MM-DD）。后端按 createtime（Unix 秒）过滤，
    // endDay 会补到当天 23:59:59，所以选同一天能查到当天全部视频。
    startDay: '',
    endDay: '',
    // 排序：与后端 radarVideoSortColumns 白名单一一对应。
    // 默认按首次发现倒序，即「最近才采集到的排前面」。
    sortBy: 'first_seen_at',
    sortDesc: true,
    targets: [],
    captureConfig: { autoDownload: false, maxPages: 3 },
    loading: false,
    syncing: false
};

let radarVideoSearchTimer = null;

// 各表头对应的排序列，供点击表头时切换升降序
const RADAR_SORT_COLUMNS = ['like_count', 'createtime', 'first_seen_at'];

// --------------------------------------------
// 数据加载
// --------------------------------------------

async function loadRadarVideos() {
    if (ConnectionManager && ConnectionManager.getStatus() !== 'connected') {
        renderRadarVideosEmpty('请先连接到本地服务');
        return;
    }
    if (radarVideoState.loading) return;
    radarVideoState.loading = true;

    try {
        const params = new URLSearchParams({
            page: String(radarVideoState.currentPage),
            pageSize: String(radarVideoState.pageSize),
            sortBy: radarVideoState.sortBy,
            sortDesc: String(radarVideoState.sortDesc)
        });
        if (radarVideoState.searchQuery) params.set('query', radarVideoState.searchQuery);
        if (radarVideoState.targetId) params.set('targetId', radarVideoState.targetId);
        if (radarVideoState.onlyNew) params.set('onlyNew', 'true');
        if (radarVideoState.startDay) params.set('startDay', radarVideoState.startDay);
        if (radarVideoState.endDay) params.set('endDay', radarVideoState.endDay);

        const [listRes, statsRes] = await Promise.all([
            fetch('/api/v1/radar/videos?' + params.toString()).then(r => r.json()),
            fetch('/api/v1/radar/videos/stats' + (radarVideoState.targetId
                ? '?targetId=' + encodeURIComponent(radarVideoState.targetId) : '')).then(r => r.json())
                .catch(() => null)
        ]);

        if (listRes.code === 0 || listRes.code === 200) {
            const data = listRes.data || {};
            radarVideoState.items = data.items || [];
            radarVideoState.totalCount = data.total || 0;
            radarVideoState.totalPages = data.totalPages || 0;
            renderRadarVideoSortIndicators();
            renderRadarVideosTable();
            renderRadarVideosPagination();
            // 筛选区间摘要要显示命中条数，所以等 totalCount 更新后再渲染
            renderRadarDateRangeSummary();
            renderRadarDateTrigger();
            if (statsRes && (statsRes.code === 0 || statsRes.code === 200)) {
                renderRadarVideoStats(statsRes.data || {});
            }
        } else {
            renderRadarVideosEmpty(listRes.message || '加载视频数据失败');
        }
    } catch (e) {
        console.error('加载雷达视频数据失败:', e);
        renderRadarVideosEmpty('加载失败: ' + e.message);
    } finally {
        radarVideoState.loading = false;
    }
}

async function loadRadarVideoTargets() {
    try {
        const res = await fetch('/api/v1/radar/targets').then(r => r.json());
        if (res.code === 0 || res.code === 200) {
            radarVideoState.targets = res.data || [];
            renderRadarVideoTargetFilter();
        }
    } catch (e) {
        console.error('加载监控目标失败:', e);
    }
}

async function loadRadarCaptureConfig() {
    try {
        const res = await fetch('/api/v1/radar/config').then(r => r.json());
        if (res.code === 0 || res.code === 200) {
            radarVideoState.captureConfig = Object.assign(
                { autoDownload: false, maxPages: 3 }, res.data || {});
            const auto = document.getElementById('radarAutoDownload');
            const pages = document.getElementById('radarMaxPages');
            if (auto) auto.checked = radarVideoState.captureConfig.autoDownload;
            if (pages) pages.value = radarVideoState.captureConfig.maxPages;
        }
    } catch (e) {
        console.error('加载雷达采集配置失败:', e);
    }
}

async function saveRadarCaptureConfig() {
    const auto = document.getElementById('radarAutoDownload');
    const pages = document.getElementById('radarMaxPages');
    if (!auto || !pages) return;

    try {
        const res = await fetch('/api/v1/radar/config', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                autoDownload: auto.checked,
                maxPages: parseInt(pages.value, 10) || 3
            })
        }).then(r => r.json());

        if (res.code === 0 || res.code === 200) {
            showMessage('采集设置已保存', 'success');
        } else {
            showMessage(res.message || '保存失败', 'error');
        }
    } catch (e) {
        showMessage('保存失败: ' + e.message, 'error');
    }
}

// --------------------------------------------
// 渲染
// --------------------------------------------

function renderRadarVideoStats(stats) {
    const set = (id, value) => {
        const el = document.getElementById(id);
        if (el) el.textContent = value;
    };
    set('radarStatTotal', formatNumber(stats.totalVideos || 0));
    set('radarStatNew', formatNumber(stats.newVideos || 0));
    set('radarStatAuthors', formatNumber(stats.totalAuthors || 0));
    set('radarStatPlays', formatNumber(stats.totalPlays || 0));
    set('radarStatLikes', formatNumber(stats.totalLikes || 0));
    set('radarStatUpdated', stats.updatedAt ? formatRadarTime(stats.updatedAt) : '—');
}

function renderRadarVideoTargetFilter() {
    const sel = document.getElementById('radarVideoTargetFilter');
    if (!sel) return;
    const current = radarVideoState.targetId;
    let html = '<option value="">全部监控账号</option>';
    for (const t of radarVideoState.targets) {
        const name = escapeHtml(t.author_name || t.username);
        html += `<option value="${escapeHtml(t.id)}"${t.id === current ? ' selected' : ''}>${name}</option>`;
    }
    sel.innerHTML = html;
}

function renderRadarVideosEmpty(message) {
    const tbody = document.getElementById('radarVideosTableBody');
    if (!tbody) return;
    tbody.innerHTML = `
        <tr>
            <td colspan="8">
                <div class="table-empty-state">
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                        <path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/>
                        <circle cx="12" cy="12" r="3"/>
                    </svg>
                    <p>${escapeHtml(message)}</p>
                </div>
            </td>
        </tr>`;
}

// --------------------------------------------
// 排序
// --------------------------------------------

// applyRadarSort 应用排序并回到第一页，改排序后停留在旧页码会看到空页
function applyRadarSort(sortBy, sortDesc) {
    radarVideoState.sortBy = sortBy;
    radarVideoState.sortDesc = sortDesc;
    radarVideoState.currentPage = 1;
    const select = document.getElementById('radarVideoSort');
    if (select) {
        const want = `${sortBy}:${sortDesc ? 'desc' : 'asc'}`;
        // 表头点击产生的组合如果不在下拉选项里，就补一条，免得下拉显示的选择和实际排序对不上
        if (![...select.options].some(o => o.value === want)) {
            const opt = document.createElement('option');
            opt.value = want;
            opt.textContent = `${RADAR_SORT_LABELS[sortBy] || sortBy} ${sortDesc ? '降序' : '升序'}`;
            select.appendChild(opt);
        }
        select.value = want;
    }
    loadRadarVideos();
}

const RADAR_SORT_LABELS = {
    like_count: '点赞数',
    fav_count: '收藏数',
    forward_count: '转发数',
    comment_count: '评论数',
    read_count: '播放数',
    createtime: '发布时间',
    first_seen_at: '首次发现',
    duration: '时长',
    title: '标题'
};

// 排序下拉：value 形如 like_count:desc
function sortRadarVideos(value) {
    const [sortBy, dir] = value.split(':');
    applyRadarSort(sortBy, dir !== 'asc');
}

// 点击表头：在同一列上切换升降序，切换到别的列则默认降序（从多到少）
function sortRadarVideosByColumn(column) {
    if (!RADAR_SORT_COLUMNS.includes(column)) return;
    if (radarVideoState.sortBy === column) {
        applyRadarSort(column, !radarVideoState.sortDesc);
    } else {
        applyRadarSort(column, true);
    }
}

// 在可排序表头上标出当前排序列与方向
function renderRadarVideoSortIndicators() {
    const arrows = { radarThLike: 'like_count', radarThCreatetime: 'createtime', radarThFirstSeen: 'first_seen_at' };
    for (const [id, column] of Object.entries(arrows)) {
        const th = document.getElementById(id);
        if (!th) continue;
        const active = radarVideoState.sortBy === column;
        th.classList.toggle('sort-active', active);
        th.dataset.sortArrow = active ? (radarVideoState.sortDesc ? 'desc' : 'asc') : '';
    }
}

// --------------------------------------------
// 一键同步
// --------------------------------------------

// syncRadarVideosNow 立刻触发一次采集，忽略各账号的轮询间隔。
// 已存在的视频同样会被重新拉取，点赞等互动数随之刷新——
// 这一点从「最后更新」列和同步结果里的「刷新 N 条」都能看出来。
async function syncRadarVideosNow() {
    if (radarVideoState.syncing) return;
    radarVideoState.syncing = true;
    setRadarSyncButtonState(true);

    try {
        const res = await fetch('/api/v1/radar/sync', { method: 'POST' });
        const body = await res.json().catch(() => null);

        if (body && (body.code === 0 || body.code === 200)) {
            const d = body.data || {};
            const secs = ((d.durationMs || 0) / 1000).toFixed(1);
            showMessage(`同步完成：${d.targets || 0} 个账号，拉取 ${d.found || 0} 条，新增 ${d.newVideos || 0} 条，刷新 ${d.updatedVideos || 0} 条，耗时 ${secs}s`, 'success');
            showRadarSyncDetails(d);
            radarVideoState.currentPage = 1;
            await loadRadarVideos();
        } else {
            showMessage((body && body.message) || '同步失败', 'error');
            showRadarSyncDetails(null, (body && body.message) || '同步失败');
        }
    } catch (e) {
        console.error('同步失败:', e);
        showMessage('同步失败: ' + e.message, 'error');
    } finally {
        radarVideoState.syncing = false;
        setRadarSyncButtonState(false);
    }
}

function setRadarSyncButtonState(busy) {
    const btn = document.getElementById('radarSyncNowBtn');
    const text = document.getElementById('radarSyncNowText');
    if (btn) {
        btn.disabled = busy;
        btn.style.opacity = busy ? '0.6' : '';
        btn.style.cursor = busy ? 'progress' : '';
    }
    if (text) text.textContent = busy ? '同步中...' : '立即同步';
}

// 把逐个账号的结果摊开显示，只在有异常账号时才出现
function showRadarSyncDetails(result, failureMessage) {
    let box = document.getElementById('radarSyncDetails');
    if (!box) {
        const anchor = document.getElementById('radarSyncNowBtn');
        if (!anchor || !anchor.parentElement) return;
        box = document.createElement('div');
        box.id = 'radarSyncDetails';
        box.className = 'alert alert-info';
        box.style.cssText = 'margin-top:16px;display:none';
        anchor.parentElement.parentElement.appendChild(box);
    }

    if (failureMessage) {
        box.style.display = 'flex';
        box.innerHTML = `<span>${escapeHtml(failureMessage)}</span>`;
        return;
    }
    if (!result) {
        box.style.display = 'none';
        return;
    }

    const details = result.details || [];
    const bad = details.filter(d => d.error);
    if (!bad.length) {
        box.style.display = 'none';
        return;
    }

    box.style.display = 'flex';
    box.innerHTML = `<span>以下账号采集不完整：${bad.map(d =>
        `${escapeHtml(d.authorName || d.targetId)}（${escapeHtml(d.error)}）`).join('；')}</span>`;
}

function renderRadarVideosTable() {
    const tbody = document.getElementById('radarVideosTableBody');
    if (!tbody) return;

    if (!radarVideoState.items.length) {
        const hasFilter = radarVideoState.searchQuery || radarVideoState.targetId || radarVideoState.onlyNew;
        renderRadarVideosEmpty(hasFilter ? '没有符合条件的视频' : '暂无采集数据。请先在「对标雷达」添加监控账号，并保持微信客户端连接。');
        return;
    }

    // 已下载完成的 videoId → 本地文件路径。决定操作列渲染「下载」还是「播放」。
    // 用缓存，不在这里请求接口：重绘很频繁（翻页/排序/筛选都会触发）。
    ensureRadarDownloadedMap();

    let html = '';
    for (const v of radarVideoState.items) {
        const cover = v.thumbUrl || v.coverUrl || '';
        const thumbnail = cover
            ? `<img class="table-thumbnail" src="${escapeHtml(cover)}" alt="" onerror="this.style.display='none';this.nextElementSibling.style.display='flex'"><div class="table-thumbnail-placeholder" style="display:none"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polygon points="23 7 16 12 23 17 23 7"/><rect x="1" y="5" width="15" height="14" rx="2" ry="2"/></svg></div>`
            : `<div class="table-thumbnail-placeholder"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polygon points="23 7 16 12 23 17 23 7"/><rect x="1" y="5" width="15" height="14" rx="2" ry="2"/></svg></div>`;

        // 视频规格摘要
        const meta = [];
        if (v.duration) meta.push(escapeHtml(formatDuration(v.duration)));
        if (v.resolution) meta.push(escapeHtml(v.resolution));
        if (v.fileSize) meta.push(escapeHtml(formatBytes(v.fileSize)));
        if (v.ipRegion) meta.push(escapeHtml(v.ipRegion));

        // 互动数据徽章，0 也显示，方便对比
        const statBadges = [];
        statBadges.push(`<span class="stat-badge" title="播放量"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polygon points="5 3 19 12 5 21 5 3"/></svg>${formatNumber(v.readCount || 0)}</span>`);
        statBadges.push(`<span class="stat-badge stat-like" title="点赞"><svg viewBox="0 0 24 24" fill="currentColor"><path d="M14 9V5a3 3 0 0 0-3-3l-4 9v11h11.28a2 2 0 0 0 2-1.7l1.38-9a2 2 0 0 0-2-2.3zM7 22H4a2 2 0 1 1 0-4h3"/></svg>${formatNumber(v.likeCount || 0)}</span>`);
        statBadges.push(`<span class="stat-badge stat-comment" title="评论"><svg viewBox="0 0 24 24" fill="currentColor"><path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/></svg>${formatNumber(v.commentCount || 0)}</span>`);
        statBadges.push(`<span class="stat-badge stat-fav" title="收藏"><svg viewBox="0 0 24 24" fill="currentColor"><polygon points="12 2 15.09 8.26 22 9.27 17 14.14 18.18 21.02 12 17.77 5.82 21.02 7 14.14 2 9.27 8.91 8.26 12 2"/></svg>${formatNumber(v.favCount || 0)}</span>`);
        statBadges.push(`<span class="stat-badge stat-forward" title="转发"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="17 1 21 5 17 9"/><path d="M3 11V9a4 4 0 0 1 4-4h14"/><polyline points="7 23 3 19 7 15"/><path d="M21 13v2a4 4 0 0 1-4 4H3"/></svg>${formatNumber(v.forwardCount || 0)}</span>`);

        html += `
            <tr data-id="${escapeHtml(v.id)}">
                <td>${v.isNew ? '<span class="stat-badge stat-like" title="本次监控新发现">NEW</span>' : ''}</td>
                <td>${thumbnail}</td>
                <td>
                    <div class="video-info-cell">
                        <div class="table-title" title="${escapeHtml(v.title || '无标题')}">${escapeHtml(v.title || '无标题')}</div>
                        <div class="table-meta">
                            <span class="meta-item">${escapeHtml(v.authorName || '')}</span>
                            ${meta.length ? `<span class="meta-item">${meta.join(' · ')}</span>` : ''}
                        </div>
                    </div>
                </td>
                <td><div style="display:flex;flex-wrap:wrap;gap:6px;">${statBadges.join('')}</div></td>
                <td>${v.createTime ? escapeHtml(formatRadarTime(v.createTime * 1000)) : '—'}</td>
                <td>${escapeHtml(formatRadarTime(v.firstSeenAt))}</td>
                <td title="最近一次采集到这条视频的时间">${escapeHtml(formatRadarTime(v.lastSeenAt))}</td>
                <td>
                    ${radarDownloadedIds.has(v.videoId)
                        ? `<button class="btn btn-secondary" style="padding:4px 10px;font-size:12px;"
                                title="播放已下载的文件"
                                onclick="playRadarVideo('${escapeHtml(v.videoId || '')}')">▶ 播放</button>`
                        : `<button class="btn btn-secondary" style="padding:4px 10px;font-size:12px;"
                                onclick="downloadRadarVideo('${escapeHtml(v.videoId || '')}')">下载</button>`}
                </td>
            </tr>`;
    }
    tbody.innerHTML = html;
}

function renderRadarVideosPagination() {
    const container = document.getElementById('radarVideosPagination');
    if (!container) return;

    if (radarVideoState.totalPages <= 1) {
        container.innerHTML = radarVideoState.totalCount > 0
            ? `<span class="pagination-info">共 ${radarVideoState.totalCount} 条</span>`
            : '';
        return;
    }

    let html = `<button ${radarVideoState.currentPage === 1 ? 'disabled' : ''} onclick="goToRadarVideosPage(${radarVideoState.currentPage - 1})">上一页</button>`;

    const maxVisible = 5;
    let start = Math.max(1, radarVideoState.currentPage - Math.floor(maxVisible / 2));
    let end = Math.min(radarVideoState.totalPages, start + maxVisible - 1);
    if (end - start < maxVisible - 1) start = Math.max(1, end - maxVisible + 1);

    if (start > 1) {
        html += `<button onclick="goToRadarVideosPage(1)">1</button>`;
        if (start > 2) html += `<span class="pagination-info">...</span>`;
    }
    for (let i = start; i <= end; i++) {
        html += `<button class="${i === radarVideoState.currentPage ? 'active' : ''}" onclick="goToRadarVideosPage(${i})">${i}</button>`;
    }
    if (end < radarVideoState.totalPages) {
        if (end < radarVideoState.totalPages - 1) html += `<span class="pagination-info">...</span>`;
        html += `<button onclick="goToRadarVideosPage(${radarVideoState.totalPages})">${radarVideoState.totalPages}</button>`;
    }
    html += `<button ${radarVideoState.currentPage === radarVideoState.totalPages ? 'disabled' : ''} onclick="goToRadarVideosPage(${radarVideoState.currentPage + 1})">下一页</button>`;
    html += `<span class="pagination-info">共 ${radarVideoState.totalCount} 条</span>`;

    container.innerHTML = html;
}

// --------------------------------------------
// 交互
// --------------------------------------------

function goToRadarVideosPage(page) {
    if (page < 1 || page > radarVideoState.totalPages) return;
    radarVideoState.currentPage = page;
    loadRadarVideos();
}

function changeRadarVideosPageSize(value) {
    radarVideoState.pageSize = parseInt(value, 10) || 20;
    radarVideoState.currentPage = 1;
    loadRadarVideos();
}

function handleRadarVideoSearch(value) {
    clearTimeout(radarVideoSearchTimer);
    radarVideoSearchTimer = setTimeout(() => {
        radarVideoState.searchQuery = value.trim();
        radarVideoState.currentPage = 1;
        loadRadarVideos();
    }, 300);
}

function filterRadarVideosByTarget(value) {
    radarVideoState.targetId = value;
    radarVideoState.currentPage = 1;
    loadRadarVideos();
}

function toggleRadarOnlyNew(checked) {
    radarVideoState.onlyNew = checked;
    radarVideoState.currentPage = 1;
    loadRadarVideos();
}

// ============================================
// 发布时间区间筛选
//
// 交互模仿 Element UI 的日期区间选择器：
//   点击触发器 -> 弹出双月面板 -> 点起点 -> 点终点 -> 确定
// 第一次点击定起点，此时鼠标在面板上移动会实时预览区间，
// 第二次点击定终点并自动交换（若终点早于起点）。
// ============================================

// 面板状态与「已应用」的筛选值是分开的：取消时不会污染已应用的筛选。
const radarDatePickerState = {
    open: false,
    // 左月视图的 1 号
    viewMonth: new Date(),
    tempStart: '',
    tempEnd: '',
    // 已定起点、正在等终点
    selecting: false,
    // 悬停日期，用于预览区间
    hoverDate: ''
};

const RADAR_DATE_WEEKDAYS = ['一', '二', '三', '四', '五', '六', '日'];

// 把 Date 格式化成 YYYY-MM-DD（本地时区，不用 toISOString 以免时区偏移）
function formatDateInput(d) {
    const p = n => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

// 由 YYYY-MM-DD 还原成本地零点的 Date
function parseDateInput(s) {
    if (!s) return null;
    const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(s);
    if (!m) return null;
    return new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]));
}

function radarDateTodayIso() {
    return formatDateInput(new Date());
}

// 生成某月的 6x7 网格（周一起始）
function radarDateBuildMonth(year, month) {
    const first = new Date(year, month, 1);
    // getDay(): 0=周日。转成周一起始的偏移
    const offset = (first.getDay() + 6) % 7;
    const gridStart = new Date(year, month, 1 - offset);

    const todayIso = radarDateTodayIso();
    const cells = [];
    for (let i = 0; i < 42; i++) {
        const d = new Date(gridStart.getFullYear(), gridStart.getMonth(), gridStart.getDate() + i);
        const iso = formatDateInput(d);
        cells.push({
            iso,
            day: d.getDate(),
            otherMonth: d.getMonth() !== month,
            today: iso === todayIso
        });
    }
    return cells;
}

// 当前应当高亮的区间（含预览态）
function radarDateActiveRange() {
    const s = radarDatePickerState;
    if (!s.tempStart) return { start: '', end: '' };
    if (s.tempEnd) {
        return s.tempStart <= s.tempEnd
            ? { start: s.tempStart, end: s.tempEnd }
            : { start: s.tempEnd, end: s.tempStart };
    }
    // 已定起点、正在选终点：用悬停位置预览
    if (s.selecting && s.hoverDate) {
        return s.tempStart <= s.hoverDate
            ? { start: s.tempStart, end: s.hoverDate }
            : { start: s.hoverDate, end: s.tempStart };
    }
    return { start: s.tempStart, end: '' };
}

function radarDateCellClass(cell, range) {
    const cls = ['radar-date-cell'];
    if (cell.otherMonth) cls.push('is-other-month');
    if (cell.today) cls.push('is-today');

    // 非本月日期不参与区间高亮。
    // 它们只是邻月的视觉参照；如果照样画色带，同一个区间会在两个月的网格里
    // 各出现半截，和真正可选的日期在视觉上打架。
    if (cell.otherMonth) return cls.join(' ');

    const { start, end } = range;
    if (start && end) {
        if (cell.iso === start) cls.push('range-start');
        else if (cell.iso === end) cls.push('range-end');
        else if (cell.iso > start && cell.iso < end) cls.push('in-range');
    } else if (start && cell.iso === start) {
        cls.push('range-start', 'range-end');
    }
    return cls.join(' ');
}

// 只重建网格结构。调用时机：打开面板、翻月。
//
// 注意：悬停预览绝不能走这里。旧实现把 mouseenter 也接到本函数上，
// 每次进入一个格子就重建全部 84 个按钮 —— 正在按下的那个按钮被销毁，
// mousedown 之后 mouseup/click 落不到它身上，点击就"卡住"了；
// 而且 click 会落到 body 上，被外部点击监听当成「点了面板外面」而关闭面板。
// 悬停改走 paintRadarDateRange()，只改 class 不动 DOM。
function renderRadarDatePicker() {
    const container = document.getElementById('radarDateMonths');
    if (!container) return;

    const base = radarDatePickerState.viewMonth;
    let html = '';

    for (let m = 0; m < 2; m++) {
        const monthDate = new Date(base.getFullYear(), base.getMonth() + m, 1);
        const year = monthDate.getFullYear();
        const month = monthDate.getMonth();
        const cells = radarDateBuildMonth(year, month);

        html += '<div class="radar-date-month">';
        html += `<div class="radar-date-month-title">${year} 年 ${month + 1} 月</div>`;
        html += '<div class="radar-date-weekdays">'
            + RADAR_DATE_WEEKDAYS.map(w => `<span>${w}</span>`).join('')
            + '</div>';
        html += '<div class="radar-date-grid">';
        for (const c of cells) {
            // 只写静态属性；区间高亮由 paintRadarDateRange() 负责
            html += `<button type="button" class="radar-date-cell"
                data-iso="${c.iso}"
                data-other="${c.otherMonth ? '1' : ''}"
                data-today="${c.today ? '1' : ''}">${c.day}</button>`;
        }
        html += '</div></div>';
    }

    container.innerHTML = html;
    paintRadarDateRange();
}

// 只更新格子的高亮 class，不碰 DOM 结构。
// 悬停、点击都走这里 —— 按钮节点保持稳定，点击事件不会被吞掉。
function paintRadarDateRange() {
    const container = document.getElementById('radarDateMonths');
    if (!container) return;

    const range = radarDateActiveRange();
    const cells = container.querySelectorAll('.radar-date-cell');

    for (const btn of cells) {
        const iso = btn.dataset.iso;
        const cls = radarDateCellClass({
            iso,
            otherMonth: btn.dataset.other === '1',
            today: btn.dataset.today === '1'
        }, range);
        if (btn.className !== cls) btn.className = cls;
    }

    renderRadarDatePickerHint();
}

// 用事件委托处理网格交互：DOM 重建后不需要重新绑事件
function bindRadarDateGridEvents() {
    const container = document.getElementById('radarDateMonths');
    if (!container || container.dataset.bound === '1') return;
    container.dataset.bound = '1';

    container.addEventListener('click', function (event) {
        const btn = event.target.closest('.radar-date-cell');
        if (!btn || !container.contains(btn)) return;
        // 阻止冒泡到 document，否则会被外部点击监听当成点了面板外
        event.stopPropagation();
        // 非本月日期不可选
        if (btn.classList.contains('is-other-month')) return;
        radarDatePickDay(btn.dataset.iso);
    });

    container.addEventListener('mouseover', function (event) {
        const btn = event.target.closest('.radar-date-cell');
        if (!btn || !container.contains(btn)) return;
        // 非本月日期不参与悬停预览
        if (btn.classList.contains('is-other-month')) return;
        radarDateHoverDay(btn.dataset.iso);
    });

    // 鼠标移出整个网格时清掉预览，避免残留半截高亮
    container.addEventListener('mouseleave', function () {
        const s = radarDatePickerState;
        if (!s.selecting || !s.hoverDate) return;
        s.hoverDate = '';
        paintRadarDateRange();
    });
}

function renderRadarDatePickerHint() {
    const el = document.getElementById('radarDatePickerHint');
    if (!el) return;
    const s = radarDatePickerState;
    if (!s.tempStart) {
        el.textContent = '请选择开始日期';
    } else if (s.selecting) {
        el.textContent = '请选择结束日期';
    } else {
        const r = radarDateActiveRange();
        el.textContent = `${r.start} 至 ${r.end}`;
    }
}

// 更新触发器上的文字
function renderRadarDateTrigger() {
    const startEl = document.getElementById('radarDateStartText');
    const endEl = document.getElementById('radarDateEndText');
    const trigger = document.getElementById('radarDateTrigger');
    const { startDay, endDay } = radarVideoState;

    if (startEl) {
        startEl.textContent = startDay || '开始日期';
        startEl.classList.toggle('is-placeholder', !startDay);
    }
    if (endEl) {
        endEl.textContent = endDay || '结束日期';
        endEl.classList.toggle('is-placeholder', !endDay);
    }
    if (trigger) {
        trigger.classList.toggle('has-value', !!(startDay || endDay));
        trigger.classList.toggle('is-active', radarDatePickerState.open);
    }
}

function toggleRadarDatePicker(event) {
    if (event) event.stopPropagation();
    if (radarDatePickerState.open) {
        closeRadarDatePicker();
        return;
    }
    openRadarDatePicker();
}

function openRadarDatePicker() {
    const s = radarDatePickerState;
    s.open = true;

    // 打开时把已应用的区间带进临时态，方便微调
    s.tempStart = radarVideoState.startDay || '';
    s.tempEnd = radarVideoState.endDay || '';
    s.selecting = false;
    s.hoverDate = '';

    // 视图定位到起点所在月，没有就定位到当月
    const anchor = parseDateInput(s.tempStart) || new Date();
    s.viewMonth = new Date(anchor.getFullYear(), anchor.getMonth(), 1);

    const panel = document.getElementById('radarDatePicker');
    if (panel) panel.style.display = 'block';

    renderRadarDatePicker();
    bindRadarDateGridEvents();
    renderRadarDateTrigger();
}

function closeRadarDatePicker() {
    radarDatePickerState.open = false;
    const panel = document.getElementById('radarDatePicker');
    if (panel) panel.style.display = 'none';
    renderRadarDateTrigger();
}

function cancelRadarDatePicker() {
    const s = radarDatePickerState;
    // 丢弃临时选择，回到已应用的筛选
    s.tempStart = radarVideoState.startDay || '';
    s.tempEnd = radarVideoState.endDay || '';
    s.selecting = false;
    s.hoverDate = '';
    closeRadarDatePicker();
}

function radarDateShiftMonth(delta) {
    const s = radarDatePickerState;
    s.viewMonth = new Date(s.viewMonth.getFullYear(), s.viewMonth.getMonth() + delta, 1);
    renderRadarDatePicker();
}

// 点日期：第一次定起点，第二次定终点
function radarDatePickDay(iso) {
    const s = radarDatePickerState;

    if (!s.selecting) {
        s.tempStart = iso;
        s.tempEnd = '';
        s.selecting = true;
        s.hoverDate = '';
    } else {
        // 终点早于起点时交换，避免出现反向区间
        if (iso < s.tempStart) {
            s.tempEnd = s.tempStart;
            s.tempStart = iso;
        } else {
            s.tempEnd = iso;
        }
        s.selecting = false;
        s.hoverDate = '';
    }

    // 只重绘高亮，不重建 DOM —— 否则按钮被替换，后续点击会丢
    paintRadarDateRange();
}

function radarDateHoverDay(iso) {
    const s = radarDatePickerState;
    // 只在「已定起点、等终点」时预览
    if (!s.selecting) return;
    if (s.hoverDate === iso) return;
    s.hoverDate = iso;
    // 关键：悬停只改 class。旧实现走 renderRadarDatePicker()，
    // 会在鼠标按下期间重建按钮，导致 click 永远不触发。
    paintRadarDateRange();
}

// 确定：把临时区间应用到筛选并查询
function confirmRadarDateRange() {
    const s = radarDatePickerState;

    // 只选了起点没选终点：按单日处理，而不是丢掉选择
    let start = s.tempStart;
    let end = s.tempEnd;
    if (start && !end) end = start;

    radarVideoState.startDay = start || '';
    radarVideoState.endDay = end || '';
    radarVideoState.currentPage = 1;

    s.selecting = false;
    s.hoverDate = '';
    closeRadarDatePicker();

    renderRadarDateTrigger();
    renderRadarDateRangeSummary();
    loadRadarVideos();
}

// 清除：立即生效，不需要点确定
function clearRadarDateRange(event) {
    if (event) event.stopPropagation();

    radarVideoState.startDay = '';
    radarVideoState.endDay = '';
    radarVideoState.currentPage = 1;

    const s = radarDatePickerState;
    s.tempStart = '';
    s.tempEnd = '';
    s.selecting = false;
    s.hoverDate = '';

    renderRadarDateTrigger();
    renderRadarDateRangeSummary();
    loadRadarVideos();

    if (s.open) renderRadarDatePicker();
}

// days > 0 表示「近 N 天」（含今天），days === 0 表示「仅今天」
//
// 快捷按钮是「一步到位」的：点完立即生效并收起面板。
// 不要在这里渲染面板 —— 那会让快捷选择和日历面板同时出现在屏幕上，
// 用户还得再点一次「确定」才能关掉，等于白点。
function setRadarDatePreset(days) {
    const today = new Date();
    const start = new Date();
    if (days > 0) {
        start.setDate(start.getDate() - (days - 1));
    }

    const startIso = formatDateInput(start);
    const endIso = formatDateInput(today);

    radarVideoState.startDay = startIso;
    radarVideoState.endDay = endIso;
    radarVideoState.currentPage = 1;

    // 同步面板内部状态，下次打开面板时能定位到这段区间
    const s = radarDatePickerState;
    s.tempStart = startIso;
    s.tempEnd = endIso;
    s.selecting = false;
    s.hoverDate = '';
    s.viewMonth = new Date(start.getFullYear(), start.getMonth(), 1);

    closeRadarDatePicker();
    renderRadarDateTrigger();
    renderRadarDateRangeSummary();
    loadRadarVideos();
}

function onRadarDateTriggerKeydown(event) {
    if (event.key === 'Enter' || event.key === ' ' || event.key === 'Spacebar') {
        event.preventDefault();
        openRadarDatePicker();
    } else if (event.key === 'Escape') {
        cancelRadarDatePicker();
    }
}

// 点击面板外部关闭。脚本只加载一次，所以这里只绑一次。
(function bindRadarDatePickerOutsideClick() {
    if (typeof document === 'undefined') return;
    document.addEventListener('click', function (event) {
        if (!radarDatePickerState.open) return;
        const picker = document.getElementById('radarDatePicker');
        const trigger = document.getElementById('radarDateTrigger');
        if (picker && picker.contains(event.target)) return;
        if (trigger && trigger.contains(event.target)) return;
        closeRadarDatePicker();
    });
    document.addEventListener('keydown', function (event) {
        if (event.key === 'Escape' && radarDatePickerState.open) {
            cancelRadarDatePicker();
        }
    });
})();

// 区间摘要：显示命中了多少条，让人知道筛选生效了
function renderRadarDateRangeSummary() {
    const el = document.getElementById('radarDateRangeSummary');
    if (!el) return;

    const { startDay, endDay } = radarVideoState;
    if (!startDay && !endDay) {
        el.textContent = '';
        return;
    }
    const range = startDay && endDay
        ? (startDay === endDay ? startDay : `${startDay} ~ ${endDay}`)
        : (startDay ? `${startDay} 起` : `至 ${endDay}`);
    el.textContent = `已筛选 ${range}，共 ${radarVideoState.totalCount} 条`;
}

// ============================================
// 下载
// ============================================

// 已下载完成的视频：videoId -> { filePath, title }
// 下载记录里 status=completed 且有 filePath 的，操作列显示「播放」而不是「下载」。
// 用同步 Map 而不是每次渲染去查接口：渲染是同步的，拿不到异步结果。
const radarDownloadedIds = new Map();
let radarDownloadedLoaded = false;

// 缓存下载记录映射。只在首次进入页面、以及后端广播下载完成/失败时刷新，
// 不在每次表格重绘时刷新 —— 翻页、改排序、改筛选都会重绘，每次都打一次
// 500 条的大查询是纯浪费。
let radarDownloadedInFlight = false;
function refreshRadarDownloadedMap() {
    if (typeof ApiClient === 'undefined' || !ApiClient.getDownloadRecords) return;
    if (radarDownloadedInFlight) return;
    radarDownloadedInFlight = true;

    ApiClient.getDownloadRecords({ page: 1, pageSize: 500 }).then(res => {
        // core.js 的 request() 已经返回解析后的 JSON 本体，形如
        // { success, data: { items, total } }，data 直接挂在 res 上
        const data = (res && res.data) || {};
        const items = Array.isArray(data) ? data : (data.items || []);

        radarDownloadedIds.clear();
        for (const r of items) {
            // 只认「已完成 + 有文件路径」的记录，失败记录不该给播放按钮
            if (r.status === 'completed' && r.filePath) {
                radarDownloadedIds.set(r.videoId || r.id, {
                    filePath: r.filePath,
                    title: r.title || ''
                });
            }
        }
        radarDownloadedLoaded = true;
        // 数据到位后重绘一次，让按钮切到播放态
        renderRadarVideosTable();
    }).catch(() => {
        // 接口不可用时按钮保持「下载」，不阻断页面
    }).finally(() => {
        radarDownloadedInFlight = false;
    });
}

// 确保映射已加载。首次调用会真正请求，之后直接返回，
// 直到后端广播下载完成时把它置为失效。
function ensureRadarDownloadedMap() {
    if (radarDownloadedLoaded) return;
    refreshRadarDownloadedMap();
}

// 后端广播 download_complete 时调用：把这一条直接写进映射并重绘。
// 不走接口往返，所以按钮是「立刻」变成播放键，不需要刷新页面。
function markRadarVideoDownloaded(payload) {
    if (!payload || !payload.videoId) return;

    const entry = {
        filePath: payload.path || '',
        title: payload.title || ''
    };
    radarDownloadedIds.set(payload.videoId, entry);
    // 落盘路径要等下载记录写入完成，稍后再拉一次以拿到权威 filePath
    radarDownloadedLoaded = false;
    setTimeout(() => {
        if (currentPage === 'radar-videos') refreshRadarDownloadedMap();
    }, 800);

    if (currentPage === 'radar-videos') renderRadarVideosTable();
}

// playRadarVideo 播放已下载到本地的文件
function playRadarVideo(videoId) {
    const entry = radarDownloadedIds.get(videoId);
    if (!entry) {
        showMessage('找不到已下载的文件，请先下载', 'error');
        return;
    }
    if (!entry.filePath) {
        showMessage('文件路径不可用', 'error');
        return;
    }
    if (typeof openVideoPlayer !== 'function') {
        showMessage('播放器未加载，请刷新页面', 'error');
        return;
    }
    // 第三个参数 isLocalFile=true 会让播放器走后端 /api/video/stream 读本地文件
    openVideoPlayer(entry.filePath, entry.title || '视频播放', true);
}

// 下载进度跟踪改为事件驱动，不再轮询。
//
// 旧实现每 2 秒打三个请求（单条 + 列表 + 队列），一个失败的 ID 能空转到
// 30 分钟超时（实测被打了 283 次 404）。本质问题是用「反复问」去等一个
// 服务端早已知道结果的事件 —— 后端落盘时会广播 download_complete /
// download_failed，直接监听即可，请求数归零。
//
// 这里只保留「在等哪些下载」的集合，用于在页面上给出一次汇总提示。
const radarDownloadWatch = new Map();

function trackRadarDownload(videoId) {
    if (!videoId) return;
    radarDownloadWatch.set(videoId, Date.now());
}

// 后端广播落盘完成：更新按钮、清掉等待项、给一次汇总提示
function onRadarDownloadSettled(payload, ok) {
    if (!payload) return;
    const id = payload.videoId;
    if (id && radarDownloadWatch.has(id)) {
        radarDownloadWatch.delete(id);
    }

    if (ok && id) {
        markRadarVideoDownloaded(payload);
    }

    if (currentPage === 'radar-videos') {
        showMessage(
            ok ? `下载完成：${payload.title || ''}` : `下载失败：${payload.title || ''}`,
            ok ? 'success' : 'error'
        );
    }
}

// 一次性注册监听。脚本只加载一次，所以不需要防重复。
if (typeof WebSocketClient !== 'undefined' && WebSocketClient.onDownloadComplete) {
    WebSocketClient.onDownloadComplete((payload) => onRadarDownloadSettled(payload, true));
    WebSocketClient.onDownloadFailed((payload) => onRadarDownloadSettled(payload, false));
}

// downloadRadarVideo 从「视频数据」页直接下载一条雷达视频
async function downloadRadarVideo(videoId) {
    const v = radarVideoState.items.find(x => x.videoId === videoId);
    if (!v) {
        showMessage('未找到该视频，列表可能已刷新', 'error');
        return;
    }
    if (!v.videoUrl) {
        showMessage('这条视频没有可用的下载地址，请先「立即同步」重新采集', 'error');
        return;
    }

    try {
        await ApiClient.startVideoDownload({
            videoId: v.videoId,
            videoUrl: v.videoUrl,
            key: v.decryptKey || '',
            title: v.title || '',
            author: v.authorName || '',
            expectedSize: v.fileSize || 0,
            duration: v.duration || 0,
            resolution: v.resolution || '',
            fileFormat: '',
            forceSave: true
        });
        showMessage('已开始下载，完成后可在「下载记录」查看', 'success');
        trackRadarDownload(v.videoId);
    } catch (e) {
        showMessage('下载启动失败: ' + e.message, 'error');
    }
}

// 时间格式化：后端 createTime 是 Unix 秒，firstSeenAt 是 RFC3339 字符串
function formatRadarTime(value) {
    if (!value) return '—';
    const d = (typeof value === 'number')
        ? new Date(value)
        : new Date(String(value).replace(' ', 'T'));
    if (isNaN(d.getTime())) return '—';
    const p = n => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

// 导出当前筛选结果为 CSV，方便丢进表格里做对标分析
async function exportRadarVideosCSV() {
    showMessage('正在导出...', 'info');
    try {
        const params = new URLSearchParams({ page: '1', pageSize: '200', sortBy: 'first_seen_at', sortDesc: 'true' });
        if (radarVideoState.searchQuery) params.set('query', radarVideoState.searchQuery);
        if (radarVideoState.targetId) params.set('targetId', radarVideoState.targetId);
        if (radarVideoState.onlyNew) params.set('onlyNew', 'true');
        if (radarVideoState.startDay) params.set('startDay', radarVideoState.startDay);
        if (radarVideoState.endDay) params.set('endDay', radarVideoState.endDay);

        const res = await fetch('/api/v1/radar/videos?' + params.toString()).then(r => r.json());
        if (res.code !== 0 && res.code !== 200) throw new Error(res.message || '导出失败');

        const items = (res.data && res.data.items) || [];
        if (!items.length) {
            showMessage('没有可导出的数据', 'warning');
            return;
        }

        const header = ['作者', '视频ID', '标题', '发布时间', '首次发现', '播放', '点赞', '评论', '收藏', '转发', '时长(秒)', '大小(字节)', '分辨率', '位置', '视频链接'];
        const lines = [header.join(',')];
        for (const v of items) {
            lines.push([
                v.authorName, v.videoId, v.title,
                v.createTime ? new Date(v.createTime * 1000).toLocaleString('zh-CN') : '',
                formatRadarTime(v.firstSeenAt),
                v.readCount, v.likeCount, v.commentCount, v.favCount, v.forwardCount,
                v.duration, v.fileSize, v.resolution, v.ipRegion, v.videoUrl
            ].map(csvEscape).join(','));
        }

        // BOM 头，否则 Excel 打开中文会乱码
        const blob = new Blob(['﻿' + lines.join('\n')], { type: 'text/csv;charset=utf-8' });
        const a = document.createElement('a');
        a.href = URL.createObjectURL(blob);
        a.download = `雷达视频数据_${new Date().toISOString().slice(0, 10)}.csv`;
        a.click();
        URL.revokeObjectURL(a.href);
        showMessage(`已导出 ${items.length} 条`, 'success');
    } catch (e) {
        showMessage('导出失败: ' + e.message, 'error');
    }
}

function csvEscape(v) {
    const s = v === undefined || v === null ? '' : String(v);
    return /[",\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s;
}

async function clearRadarVideos() {
    if (!confirm('确定要清空全部采集到的视频数据吗？此操作不可恢复。')) return;
    try {
        const params = radarVideoState.targetId ? '?targetId=' + encodeURIComponent(radarVideoState.targetId) : '';
        const res = await fetch('/api/v1/radar/videos' + params, { method: 'DELETE' }).then(r => r.json());
        if (res.code === 0 || res.code === 200) {
            showMessage('已清空', 'success');
            loadRadarVideos();
        } else {
            showMessage(res.message || '清空失败', 'error');
        }
    } catch (e) {
        showMessage('清空失败: ' + e.message, 'error');
    }
}

// --------------------------------------------
// 初始化
// --------------------------------------------

document.addEventListener('DOMContentLoaded', () => {
    document.querySelectorAll('.nav-item').forEach(item => {
        item.addEventListener('click', () => {
            if (item.dataset.page === 'radar-videos') {
                loadRadarVideoTargets();
                loadRadarCaptureConfig();
                loadRadarVideos();
            }
        });
    });
});
