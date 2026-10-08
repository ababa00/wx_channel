package handlers

import (
	"net/url"
	"strings"
	"testing"
)

func TestIsOriginalVideoURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{
			name: "legacy original marker",
			raw:  "https://finder.video.qq.com/251/20302/stodownload?encfilekey=abc&token=def&X-snsvideoflag=original",
			want: true,
		},
		{
			name: "specific spec marker",
			raw:  "https://finder.video.qq.com/251/20302/stodownload?encfilekey=abc&token=def&X-snsvideoflag=WT111",
			want: false,
		},
		{
			name: "no video flag",
			raw:  "https://finder.video.qq.com/251/20302/stodownload?encfilekey=abc&token=def",
			want: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := isOriginalVideoURL(tt.raw)
			if got != tt.want {
				t.Fatalf("isOriginalVideoURL(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestNormalizeOriginalVideoURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "removes legacy original query param",
			raw:  "https://finder.video.qq.com/251/20302/stodownload?encfilekey=abc&token=def&X-snsvideoflag=original",
			want: "https://finder.video.qq.com/251/20302/stodownload?encfilekey=abc&token=def",
		},
		{
			name: "keeps specific spec query param",
			raw:  "https://finder.video.qq.com/251/20302/stodownload?encfilekey=abc&token=def&X-snsvideoflag=WT111",
			want: "https://finder.video.qq.com/251/20302/stodownload?encfilekey=abc&token=def&X-snsvideoflag=WT111",
		},
		{
			name: "keeps url without spec flag",
			raw:  "https://finder.video.qq.com/251/20302/stodownload?encfilekey=abc&token=def",
			want: "https://finder.video.qq.com/251/20302/stodownload?encfilekey=abc&token=def",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := normalizeOriginalVideoURL(tt.raw)
			if got != tt.want {
				t.Fatalf("normalizeOriginalVideoURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestNormalizeDownloadURLPreservesOriginalSignedParameters(t *testing.T) {
	t.Parallel()

	raw := "https://finder.video.qq.com/251/20302/stodownload?encfilekey=abc&hy=SH&idx=1&m=compressed&uzid=7a1ac&token=def&basedata=base&sign=sig&web=1&extg=10f0000&svrbypass=bypass&svrnonce=123"
	got, mode := NormalizeDownloadURL(raw, "")
	if mode != downloadVideoModeOriginal {
		t.Fatalf("NormalizeDownloadURL mode = %q, want %q", mode, downloadVideoModeOriginal)
	}
	if got != raw {
		t.Fatalf("NormalizeDownloadURL changed signed URL without legacy marker: got %q, want %q", got, raw)
	}

	marked := raw + "&X-snsvideoflag=original"
	got, mode = NormalizeDownloadURL(marked, "")
	if mode != downloadVideoModeOriginal {
		t.Fatalf("NormalizeDownloadURL marked mode = %q, want %q", mode, downloadVideoModeOriginal)
	}
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse normalized URL: %v", err)
	}
	query := parsed.Query()
	for _, key := range []string{"encfilekey", "hy", "idx", "m", "uzid", "token", "basedata", "sign", "web", "extg", "svrbypass", "svrnonce"} {
		if query.Get(key) == "" {
			t.Fatalf("normalized URL lost signed query parameter %q: %q", key, got)
		}
	}
	if marker := query.Get("X-snsvideoflag"); marker != "" {
		t.Fatalf("normalized URL retained legacy marker: %q", marker)
	}
}

func TestDownloadVideoModeFromRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		req  DownloadVideoRequest
		want downloadVideoMode
	}{
		{
			name: "legacy original marker uses original mode",
			req: DownloadVideoRequest{
				VideoURL: "https://finder.video.qq.com/251/20302/stodownload?encfilekey=abc&token=def&X-snsvideoflag=original",
			},
			want: downloadVideoModeOriginal,
		},
		{
			name: "specific file format uses specific mode",
			req: DownloadVideoRequest{
				VideoURL:   "https://finder.video.qq.com/251/20302/stodownload?encfilekey=abc&token=def&X-snsvideoflag=WT111",
				FileFormat: "WT111",
			},
			want: downloadVideoModeSpecific,
		},
		{
			name: "file format alone keeps specific mode",
			req: DownloadVideoRequest{
				VideoURL:   "https://finder.video.qq.com/251/20302/stodownload?encfilekey=abc&token=def",
				FileFormat: "WT111",
			},
			want: downloadVideoModeSpecific,
		},
		{
			name: "missing spec uses original mode",
			req: DownloadVideoRequest{
				VideoURL: "https://finder.video.qq.com/251/20302/stodownload?encfilekey=abc&token=def",
			},
			want: downloadVideoModeOriginal,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := downloadModeFromRequest(tt.req)
			if got != tt.want {
				t.Fatalf("downloadModeFromRequest(%+v) = %q, want %q", tt.req, got, tt.want)
			}
		})
	}
}

func TestDownloadConnectionCountFromMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		base        int
		mode        downloadVideoMode
		wantConnect int
	}{
		{
			name:        "original mode forces single connection",
			base:        8,
			mode:        downloadVideoModeOriginal,
			wantConnect: 1,
		},
		{
			name:        "specific mode preserves configured connections",
			base:        8,
			mode:        downloadVideoModeSpecific,
			wantConnect: 8,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := downloadConnectionCountFromMode(tt.base, tt.mode)
			if got != tt.wantConnect {
				t.Fatalf("downloadConnectionCountFromMode(%d, %q) = %d, want %d", tt.base, tt.mode, got, tt.wantConnect)
			}
		})
	}
}

// describeSizeDeviation 只描述偏差、不阻断下载。
// 回归背景：它的前身 validateOriginalDownloadSize 会直接返回 error，
// 而 feed 标注的 media.fileSize 是作者上传的原始大小、CDN 给的是微信转码画质，
// 两者差距经常超过 20%，于是每一次下载都判失败（实测 completed 记录为 0，
// 下载目录里一个视频文件都没有）。
func TestDescribeSizeDeviation(t *testing.T) {
	t.Parallel()

	const megabyte = int64(1024 * 1024)
	tests := []struct {
		name         string
		mode         downloadVideoMode
		expectedSize int64
		actualSize   int64
		wantNote     bool
	}{
		{
			name:         "notes the reported 362 MB to 39 MB shrink",
			mode:         downloadVideoModeOriginal,
			expectedSize: 362 * megabyte,
			actualSize:   39 * megabyte,
			wantNote:     true,
		},
		{
			name:         "notes the reported 4.26 MB to 0.25 MB shrink",
			mode:         downloadVideoModeOriginal,
			expectedSize: 4*megabyte + 26*megabyte/100,
			actualSize:   25 * megabyte / 100,
			wantNote:     true,
		},
		{
			// 真实抓到的这条：feed 3.45 MB，CDN 给 0.74 MB，解密后完全可播放
			name:         "notes the real-world 21.5% rendition we measured",
			mode:         downloadVideoModeOriginal,
			expectedSize: 3614995,
			actualSize:   775859,
			wantNote:     true,
		},
		{
			name:         "silent when the hint is close",
			mode:         downloadVideoModeOriginal,
			expectedSize: 362 * megabyte,
			actualSize:   300 * megabyte,
		},
		{
			name:         "silent when the file is larger than the hint",
			mode:         downloadVideoModeOriginal,
			expectedSize: 10 * megabyte,
			actualSize:   39 * megabyte,
		},
		{
			name:         "silent for a specific rendition",
			mode:         downloadVideoModeSpecific,
			expectedSize: 362 * megabyte,
			actualSize:   39 * megabyte,
		},
		{
			name:         "silent without a source-size hint",
			mode:         downloadVideoModeOriginal,
			expectedSize: 0,
			actualSize:   39 * megabyte,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			note := describeSizeDeviation(tt.mode, tt.expectedSize, tt.actualSize)
			if got := note != ""; got != tt.wantNote {
				t.Fatalf("describeSizeDeviation() note = %q, wantNote %v", note, tt.wantNote)
			}
			// 关键回归：任何情况下都不得再出现阻断下载的错误
			if note != "" && strings.Contains(note, "低码率流") {
				t.Errorf("提示文案不应再宣称下载失败: %q", note)
			}
		})
	}
}
