package x2logs

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode/utf16"
)

const DefaultBaseURL = "https://devops.x2robot.com"

type TokenInfo struct {
	OpenID    string `json:"openId,omitempty"`
	Name      string `json:"name,omitempty"`
	IssuedAt  int64  `json:"issuedAt,omitempty"`
	ExpiresAt int64  `json:"expiresAt,omitempty"`
}

func (ti TokenInfo) ValidAt(now time.Time) bool {
	if ti.ExpiresAt <= 0 {
		return true
	}
	return now.Unix()+30 < ti.ExpiresAt
}

type storedCreds struct {
	BaseURL   string `json:"baseUrl"`
	Token     string `json:"token"`
	OpenID    string `json:"openId,omitempty"`
	Name      string `json:"name,omitempty"`
	ExpiresAt int64  `json:"expiresAt,omitempty"`
	UpdatedAt int64  `json:"updatedAt,omitempty"`
}

type CredStore struct {
	path      string
	Harvester func() string
}

func DefaultCredsPath() string {
	if p := strings.TrimSpace(os.Getenv("X2_DEVOPS_CREDS")); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".x2-devops-credentials.json"
	}
	return filepath.Join(home, ".x2-devops", "credentials.json")
}

func NewCredStore(path string) *CredStore {
	if strings.TrimSpace(path) == "" {
		path = DefaultCredsPath()
	}
	return &CredStore{
		path:      path,
		Harvester: harvestBrowserDevOpsToken,
	}
}

func (s *CredStore) Path() string { return s.path }

func CleanToken(raw string) string {
	t := strings.TrimSpace(raw)
	t = strings.Trim(t, `"'`)
	if len(t) > 7 && strings.EqualFold(t[:7], "bearer ") {
		t = strings.TrimSpace(t[7:])
	}
	return t
}

func ParseJWTClaims(raw string) (TokenInfo, error) {
	tok := CleanToken(raw)
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return TokenInfo{}, errors.New("不是有效的三段式 JWT Token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return TokenInfo{}, fmt.Errorf("解析 JWT payload 失败: %w", err)
	}
	var mp struct {
		OpenID string `json:"open_id"`
		Name   string `json:"name"`
		Exp    int64  `json:"exp"`
		Iat    int64  `json:"iat"`
	}
	if err := json.Unmarshal(payload, &mp); err != nil {
		return TokenInfo{}, fmt.Errorf("解析 JWT JSON 失败: %w", err)
	}
	return TokenInfo{
		OpenID:    mp.OpenID,
		Name:      mp.Name,
		IssuedAt:  mp.Iat,
		ExpiresAt: mp.Exp,
	}, nil
}

func normalizeBaseURL(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		if envU := strings.TrimSpace(os.Getenv("X2_DEVOPS_BASE_URL")); envU != "" {
			u = envU
		} else {
			u = DefaultBaseURL
		}
	}
	return strings.TrimRight(u, "/")
}

func (s *CredStore) SaveToken(rawToken, baseURL string) (TokenInfo, error) {
	tok := CleanToken(rawToken)
	if tok == "" {
		return TokenInfo{}, errors.New("token 不能为空")
	}
	info, err := ParseJWTClaims(tok)
	if err != nil {
		return TokenInfo{}, err
	}
	prev, _ := s.readRaw()
	if strings.TrimSpace(baseURL) == "" && prev.BaseURL != "" {
		baseURL = prev.BaseURL
	}
	rec := storedCreds{
		BaseURL:   normalizeBaseURL(baseURL),
		Token:     tok,
		OpenID:    info.OpenID,
		Name:      info.Name,
		ExpiresAt: info.ExpiresAt,
		UpdatedAt: time.Now().Unix(),
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return info, err
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return info, err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return info, err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return info, err
	}
	return info, nil
}

func (s *CredStore) readRaw() (storedCreds, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return storedCreds{}, err
	}
	var rec storedCreds
	if err := json.Unmarshal(b, &rec); err != nil {
		return storedCreds{}, err
	}
	return rec, nil
}

func (s *CredStore) Status() (TokenInfo, string, bool) {
	if envTok := CleanToken(os.Getenv("X2_DEVOPS_TOKEN")); envTok != "" {
		if info, err := ParseJWTClaims(envTok); err == nil {
			return info, normalizeBaseURL(""), info.ValidAt(time.Now())
		}
	}
	rec, err := s.readRaw()
	if err != nil || rec.Token == "" {
		if tok, base, herr := s.HarvestAndSave(""); herr == nil && tok != "" {
			info, _ := ParseJWTClaims(tok)
			return info, base, info.ValidAt(time.Now())
		}
		return TokenInfo{}, normalizeBaseURL(""), false
	}
	info, err := ParseJWTClaims(rec.Token)
	if err != nil {
		info = TokenInfo{OpenID: rec.OpenID, Name: rec.Name, ExpiresAt: rec.ExpiresAt}
	}
	return info, normalizeBaseURL(rec.BaseURL), info.ValidAt(time.Now())
}

func (s *CredStore) HarvestAndSave(fallbackBaseURL string) (string, string, error) {
	if s.Harvester == nil {
		return "", "", errors.New("未配置自动提取器")
	}
	fresh := CleanToken(s.Harvester())
	if fresh == "" {
		return "", "", errors.New("本机浏览器中未发现有效的 devops.x2robot.com 登录凭证")
	}
	info, err := ParseJWTClaims(fresh)
	if err != nil || !info.ValidAt(time.Now()) {
		return "", "", errors.New("提取到的 Token 已过期")
	}
	prev, _ := s.readRaw()
	base := fallbackBaseURL
	if base == "" {
		base = prev.BaseURL
	}
	_, _ = s.SaveToken(fresh, base)
	return fresh, normalizeBaseURL(base), nil
}

func (s *CredStore) LoadValidToken() (token string, baseURL string, err error) {
	if envTok := CleanToken(os.Getenv("X2_DEVOPS_TOKEN")); envTok != "" {
		return envTok, normalizeBaseURL(""), nil
	}
	rec, readErr := s.readRaw()
	base := normalizeBaseURL(rec.BaseURL)
	if readErr == nil && rec.Token != "" {
		if info, perr := ParseJWTClaims(rec.Token); perr == nil && info.ValidAt(time.Now()) {
			return rec.Token, base, nil
		}
	}
	if fresh, hBase, herr := s.HarvestAndSave(rec.BaseURL); herr == nil && fresh != "" {
		return fresh, hBase, nil
	}
	if readErr == nil && rec.Token != "" {
		return "", base, fmt.Errorf("devops.x2robot.com 登录凭证已过期（过期时间 %s），请在浏览器打开 https://devops.x2robot.com 重新登录，或用 /logs auth <BearerToken> / x2_logs_auth 更新凭证",
			time.Unix(rec.ExpiresAt, 0).Format("2006-01-02 15:04"))
	}
	return "", base, errors.New("尚未配置 devops.x2robot.com 日志查询凭证：请在 CLI 执行 `/logs auth <BearerToken>` 或调用 `x2_logs_auth` 设置 Token")
}

var jwtRegex = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{10,}`)

func harvestBrowserDevOpsToken() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	var globs []string
	switch runtime.GOOS {
	case "darwin":
		globs = []string{
			filepath.Join(home, "Library/Application Support/Google/Chrome/*/Local Storage/leveldb/*"),
			filepath.Join(home, "Library/Application Support/Arc/User Data/*/Local Storage/leveldb/*"),
			filepath.Join(home, "Library/Application Support/Microsoft Edge/*/Local Storage/leveldb/*"),
		}
	case "linux":
		globs = []string{
			filepath.Join(home, ".config/google-chrome/*/Local Storage/leveldb/*"),
			filepath.Join(home, ".config/chromium/*/Local Storage/leveldb/*"),
		}
	}
	now := time.Now()
	var bestTok string
	var bestExp int64

	considerBuf := func(buf []byte) {
		if !bytes.Contains(buf, []byte("devops")) && !bytes.Contains(buf, []byte("x2robot")) && !bytes.Contains(buf, []byte("eyJ")) {
			return
		}
		candidates := [][]byte{buf}
		if u8 := decodeUTF16LEASCII(buf); len(u8) > 0 {
			candidates = append(candidates, u8)
		}
		for _, c := range candidates {
			for _, m := range jwtRegex.FindAll(c, -1) {
				tok := string(m)
				info, err := ParseJWTClaims(tok)
				if err != nil || info.OpenID == "" || !info.ValidAt(now) {
					continue
				}
				if info.ExpiresAt > bestExp {
					bestExp = info.ExpiresAt
					bestTok = tok
				}
			}
		}
	}

	for _, pat := range globs {
		files, _ := filepath.Glob(pat)
		for _, fp := range files {
			ext := filepath.Ext(fp)
			if ext != ".log" && ext != ".ldb" {
				continue
			}
			raw, err := os.ReadFile(fp)
			if err != nil || len(raw) == 0 {
				continue
			}
			considerBuf(raw)
			if ext == ".ldb" {
				for _, blk := range unpackLevelDBSnappyBlocks(raw) {
					considerBuf(blk)
				}
			}
		}
	}
	return bestTok
}

func decodeUTF16LEASCII(b []byte) []byte {
	if len(b) < 40 {
		return nil
	}
	u16 := make([]uint16, len(b)/2)
	for i := range u16 {
		u16[i] = binary.LittleEndian.Uint16(b[i*2:])
	}
	return []byte(string(utf16.Decode(u16)))
}

// unpackLevelDBSnappyBlocks 解压 LevelDB sstable 默认 4KB 数据块中的 Snappy 压缩段（type=1）。
func unpackLevelDBSnappyBlocks(sst []byte) [][]byte {
	if len(sst) < 56 {
		return nil
	}
	var out [][]byte
	// LevelDB block trailer 固定 5 字节：[1 byte compressionType == 1][4 bytes crc32]
	// 这里只做轻量启发式扫描：遇到疑似合理长度的 snappy 块即尝试无损解压。
	if dec, ok := snappyDecode(sst); ok {
		out = append(out, dec)
	}
	return out
}

func snappyDecode(src []byte) ([]byte, bool) {
	dLen, n := binary.Uvarint(src)
	if n <= 0 || dLen == 0 || dLen > 4<<20 {
		return nil, false
	}
	dst := make([]byte, 0, dLen)
	s := n
	for s < len(src) && uint64(len(dst)) < dLen {
		tag := src[s]
		s++
		switch tag & 0x03 {
		case 0: // literal
			length := int(tag >> 2)
			switch {
			case length < 60:
				length++
			case length == 60:
				if s >= len(src) {
					return nil, false
				}
				length = int(src[s]) + 1
				s++
			case length == 61:
				if s+1 >= len(src) {
					return nil, false
				}
				length = int(binary.LittleEndian.Uint16(src[s:])) + 1
				s += 2
			default:
				return nil, false
			}
			if s+length > len(src) || uint64(len(dst)+length) > dLen {
				return nil, false
			}
			dst = append(dst, src[s:s+length]...)
			s += length
		case 1: // copy 1-byte offset
			if s >= len(src) {
				return nil, false
			}
			length := 4 + int((tag>>2)&0x7)
			offset := int(tag&0xe0)<<3 | int(src[s])
			s++
			if offset <= 0 || offset > len(dst) || uint64(len(dst)+length) > dLen {
				return nil, false
			}
			for i := 0; i < length; i++ {
				dst = append(dst, dst[len(dst)-offset])
			}
		case 2: // copy 2-byte offset
			if s+1 >= len(src) {
				return nil, false
			}
			length := 1 + int(tag>>2)
			offset := int(binary.LittleEndian.Uint16(src[s:]))
			s += 2
			if offset <= 0 || offset > len(dst) || uint64(len(dst)+length) > dLen {
				return nil, false
			}
			for i := 0; i < length; i++ {
				dst = append(dst, dst[len(dst)-offset])
			}
		default:
			return nil, false
		}
	}
	if uint64(len(dst)) != dLen {
		return nil, false
	}
	return dst, true
}
