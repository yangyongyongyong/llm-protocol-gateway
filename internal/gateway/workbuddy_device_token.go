package gateway

import (
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

const (
	workBuddyDeviceTokenFileTTL    = 5 * time.Minute
	workBuddyDeviceTokenFileMaxLen = 1024
)

var errWorkBuddyDeviceTokenTooLarge = errors.New("workbuddy device token file too large")

var workBuddyDeviceTokenFileCache struct {
	mu     sync.Mutex
	path   string
	token  string
	readAt time.Time
}

// resolveWorkBuddyDeviceToken picks X-Device-Token for outbound chat: per-account
// credential first, then WORKBUDDY_DEVICE_TOKEN, then WORKBUDDY_DEVICE_TOKEN_FILE.
// Empty means omit the header — never invent a token.
func resolveWorkBuddyDeviceToken(credential *domain.WorkBuddyCredential) string {
	if credential != nil {
		if token := strings.TrimSpace(credential.DeviceToken); token != "" {
			return token
		}
	}
	if token := strings.TrimSpace(os.Getenv("WORKBUDDY_DEVICE_TOKEN")); token != "" {
		return token
	}
	if path := strings.TrimSpace(os.Getenv("WORKBUDDY_DEVICE_TOKEN_FILE")); path != "" {
		return readWorkBuddyDeviceTokenFile(path)
	}
	return ""
}

func readWorkBuddyDeviceTokenFile(path string) string {
	workBuddyDeviceTokenFileCache.mu.Lock()
	defer workBuddyDeviceTokenFileCache.mu.Unlock()
	if path == workBuddyDeviceTokenFileCache.path &&
		time.Since(workBuddyDeviceTokenFileCache.readAt) < workBuddyDeviceTokenFileTTL {
		return workBuddyDeviceTokenFileCache.token
	}
	workBuddyDeviceTokenFileCache.path = path
	token, err := readTrimmedFileLimited(path, workBuddyDeviceTokenFileMaxLen)
	if err != nil {
		workBuddyDeviceTokenFileCache.token = ""
		workBuddyDeviceTokenFileCache.readAt = time.Now()
		return ""
	}
	workBuddyDeviceTokenFileCache.token = token
	workBuddyDeviceTokenFileCache.readAt = time.Now()
	return token
}

func readTrimmedFileLimited(path string, maxLen int) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() > int64(maxLen) {
		return "", errWorkBuddyDeviceTokenTooLarge
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}
