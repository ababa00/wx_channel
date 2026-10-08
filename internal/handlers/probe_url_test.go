package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 浏览记录里存的是上次抓取时的签名链接快照，带 svrnonce 时间戳。
// 链接过期后服务端会拒绝，如果让 Gopeed 盲重试 3 次只是浪费时间，
// 应该先用一次 1 字节 Range 探活，探到 4xx 就立刻给出可执行的提示。
func TestIsExpiredVideoURLStatus(t *testing.T) {
	t.Parallel()

	expired := []int{http.StatusForbidden, http.StatusGone, http.StatusNotFound}
	alive := []int{http.StatusOK, http.StatusPartialContent,
		http.StatusRequestedRangeNotSatisfiable, http.StatusUnauthorized}

	for _, s := range expired {
		if !isExpiredVideoURLStatus(s) {
			t.Errorf("isExpiredVideoURLStatus(%d) = false, want true", s)
		}
	}
	for _, s := range alive {
		if isExpiredVideoURLStatus(s) {
			t.Errorf("isExpiredVideoURLStatus(%d) = true, want false", s)
		}
	}
}

func TestProbeVideoURL(t *testing.T) {
	t.Parallel()

	t.Run("探到 403 判定为过期", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("Range"); got != "bytes=0-0" {
				t.Errorf("Range = %q, want bytes=0-0（探活必须只取 1 字节）", got)
			}
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()

		status, err := probeVideoURL(context.Background(), srv.URL)
		if err != nil {
			t.Fatalf("probeVideoURL() 报错: %v", err)
		}
		if status != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", status)
		}
		if !isExpiredVideoURLStatus(status) {
			t.Fatal("403 应被判为过期")
		}
	})

	t.Run("探到 206 判定为可用", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Range", "bytes 0-0/12345")
			w.WriteHeader(http.StatusPartialContent)
		}))
		defer srv.Close()

		status, err := probeVideoURL(context.Background(), srv.URL)
		if err != nil {
			t.Fatalf("probeVideoURL() 报错: %v", err)
		}
		if isExpiredVideoURLStatus(status) {
			t.Fatalf("206 不应被判为过期")
		}
	})

	t.Run("网络异常时返回 error，调用方据此放行而不阻断", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		url := srv.URL
		srv.Close() // 立刻关掉，制造连接失败

		if _, err := probeVideoURL(context.Background(), url); err == nil {
			t.Fatal("连接失败时应返回 error，调用方据此不阻断下载")
		}
	})

	t.Run("空 URL 直接返回 error", func(t *testing.T) {
		t.Parallel()
		if _, err := probeVideoURL(context.Background(), "  "); err == nil {
			t.Fatal("空 URL 应返回 error")
		}
	})
}
