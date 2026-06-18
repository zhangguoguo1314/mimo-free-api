package handler

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"

	"github.com/zhangguoguo1314/mimo-free-api/internal/config"
	"golang.org/x/crypto/bcrypt"
)

// activeSessions 存储活跃的 session token 及其过期时间
var activeSessions sync.Map

// AdminAuth 返回管理后台认证中间件
func AdminAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := config.Get()

		// 如果密码未设置，允许所有请求（除了需要保护的端点由路由层控制）
		if !cfg.PasswordSet {
			next.ServeHTTP(w, r)
			return
		}

		// 检查 Authorization header
		token := r.Header.Get("Authorization")
		if len(token) > 7 && token[:7] == "Bearer " {
			token = token[7:]
		}

		// 检查 admin_password query parameter（兼容简单客户端）
		if token == "" {
			token = r.URL.Query().Get("admin_password")
		}

		// 检查 cookie
		if token == "" {
			if cookie, err := r.Cookie("admin_session"); err == nil {
				token = cookie.Value
			}
		}

		if token == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"unauthorized","message":"admin password required"}`))
			return
		}

		// 验证 session token
		if val, ok := activeSessions.Load(token); ok {
			if expiry, ok := val.(time.Time); ok && time.Now().Before(expiry) {
				next.ServeHTTP(w, r)
				return
			}
			// Token 已过期，清理
			activeSessions.Delete(token)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"unauthorized","message":"session expired or invalid"}`))
	})
}

// hashPassword 使用 bcrypt 哈希密码
func hashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(bytes), err
}

// checkPasswordHash 验证密码哈希
func checkPasswordHash(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// generateSessionToken 生成随机 session token
func generateSessionToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// CleanupExpiredSessions 清理过期的 session（可定期调用）
func CleanupExpiredSessions() {
	now := time.Now()
	activeSessions.Range(func(key, value interface{}) bool {
		if expiry, ok := value.(time.Time); ok && now.After(expiry) {
			activeSessions.Delete(key)
		}
		return true
	})
}
