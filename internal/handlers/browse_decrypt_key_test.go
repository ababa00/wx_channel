package handlers

import (
	"testing"

	"wx_channel/internal/database"
)

// 回归：浏览记录更新时必须让 decrypt_key 与 video_url 保持配套。
//
// 微信每次抓取都会同时换掉签名 URL 和 decodeKey。旧实现在记录已存在时
// 沿用旧密钥，却把 video_url 覆盖成新值，于是 URL 与密钥错配，
// 下载能下满 100% 但解密必然失败：
//   "decrypted header validation failed: header does not look like a valid media stream"
// 2026-09-26 实测：video_url 是 19:18:52 抓的那条（token=ic1n0xDG6aw9，
// 对应 Key=740903373），decrypt_key 还停在 18:59:54 的旧值 1040564494。
func TestResolveBrowseDecryptKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		existing string // 库里已有的 decrypt_key
		incoming string // 本次抓到的 decrypt_key
		want     string
	}{
		{
			name:     "新抓到的密钥必须覆盖旧密钥，保证与新 URL 配套",
			existing: "1040564494",
			incoming: "740903373",
			want:     "740903373",
		},
		{
			name:     "旧记录没有密钥时用新密钥",
			existing: "",
			incoming: "740903373",
			want:     "740903373",
		},
		{
			name:     "本次没抓到密钥时才沿用旧值，避免把已有密钥抹成空",
			existing: "1040564494",
			incoming: "",
			want:     "1040564494",
		},
		{
			name:     "两边都没有就是空",
			existing: "",
			incoming: "",
			want:     "",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			record := &database.BrowseRecord{DecryptKey: tt.incoming}
			existing := &database.BrowseRecord{DecryptKey: tt.existing}
			resolveBrowseDecryptKey(record, existing)
			if record.DecryptKey != tt.want {
				t.Fatalf("resolveBrowseDecryptKey() = %q, want %q", record.DecryptKey, tt.want)
			}
		})
	}
}
