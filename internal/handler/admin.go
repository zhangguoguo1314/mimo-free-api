package handler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zhangguoguo1314/mimo-free-api/internal/config"
	"github.com/zhangguoguo1314/mimo-free-api/internal/pool"
	"github.com/zhangguoguo1314/mimo-free-api/internal/stats"
)

type AdminHandler struct {
	pool *pool.Pool
}

func NewAdminHandler(p *pool.Pool) *AdminHandler {
	return &AdminHandler{pool: p}
}

func (h *AdminHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	writeJSON(w, cfg)
}

func (h *AdminHandler) UpdateConfig(w http.ResponseWriter, r *http.Request) {
	var update struct {
		DefaultModel string `json:"default_model,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	config.Update(func(cfg *config.Config) {
		if update.DefaultModel != "" {
			cfg.DefaultModel = update.DefaultModel
		}
	})
	config.Save()
	writeJSON(w, map[string]string{"status": "ok"})
}

func (h *AdminHandler) AddAccount(w http.ResponseWriter, r *http.Request) {
	var acc config.Account
	if err := json.NewDecoder(r.Body).Decode(&acc); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if acc.ID == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	config.Update(func(cfg *config.Config) {
		cfg.Accounts = append(cfg.Accounts, acc)
	})
	config.Save()
	h.pool.Reload(config.Get().Accounts)
	writeJSON(w, map[string]string{"status": "added"})
}

func (h *AdminHandler) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "id is required"})
		return
	}
	found := false
	config.Update(func(cfg *config.Config) {
		filtered := make([]config.Account, 0, len(cfg.Accounts))
		for _, acc := range cfg.Accounts {
			if acc.ID == req.ID {
				found = true
				continue
			}
			filtered = append(filtered, acc)
		}
		cfg.Accounts = filtered
	})
	if !found {
		writeError(w, http.StatusNotFound, "account not found")
		return
	}
	config.Save()
	h.pool.Reload(config.Get().Accounts)
	writeJSON(w, map[string]string{"status": "deleted"})
}

func (h *AdminHandler) HealthCheck(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, h.pool.HealthCheck(r.Context()))
}

// GetStats 返回 token 用量统计
func (h *AdminHandler) GetStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, stats.Get().GetStats())
}

// TestAccount 测试账号有效性
func (h *AdminHandler) TestAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}

	// 查找账号
	var account *config.Account
	cfg := config.Get()
	for i := range cfg.Accounts {
		if cfg.Accounts[i].ID == req.ID {
			account = &cfg.Accounts[i]
			break
		}
	}

	if account == nil {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "account not found"})
		return
	}

	// 测试账号有效性
	valid := testAccountValidity(account)
	writeJSON(w, map[string]interface{}{
		"valid":   valid,
		"account": req.ID,
	})
}

// testAccountValidity 测试账号是否有效（通过发送一条简短chat消息验证）
func testAccountValidity(acc *config.Account) bool {
	client := &http.Client{Timeout: 15 * time.Second}

	const webBaseURL = "https://aistudio.xiaomimimo.com"
	encodedPh := url.QueryEscape(acc.Ph)
	reqURL := fmt.Sprintf("%s/open-apis/bot/chat?xiaomichatbot_ph=%s", webBaseURL, encodedPh)

	reqBody := map[string]interface{}{
		"msgId":          uuid.New().String(),
		"conversationId": uuid.New().String(),
		"query":          "hi",
		"messages":       []interface{}{},
		"parentId":       "0",
		"save":           false,
		"isEditedQuery":  false,
		"source":         "STATION",
		"scene":          "STATION",
		"isLocal":        false,
		"modelConfig": map[string]interface{}{
			"enableThinking":  false,
			"webSearchStatus": "disabled",
			"model":           "mimo-v2.5",
			"temperature":     0.8,
			"topP":            0.95,
		},
		"multiMedias": []interface{}{},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return false
	}

	req, err := http.NewRequest("POST", reqURL, bytes.NewReader(body))
	if err != nil {
		return false
	}

	cookie := fmt.Sprintf("userId=%s; serviceToken=%q; xiaomichatbot_ph=%q",
		acc.UserID, acc.ServiceToken, acc.Ph)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Origin", webBaseURL)
	req.Header.Set("Referer", webBaseURL+"/")
	req.Header.Set("x-timezone", "Asia/Shanghai")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.0")

	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}

// TestModel 测试指定账号在指定模型下的响应
func (h *AdminHandler) TestModel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AccountID string `json:"account_id"`
		Model     string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.AccountID == "" || req.Model == "" {
		writeError(w, http.StatusBadRequest, "account_id and model are required")
		return
	}

	var account *config.Account
	cfg := config.Get()
	for i := range cfg.Accounts {
		if cfg.Accounts[i].ID == req.AccountID {
			account = &cfg.Accounts[i]
			break
		}
	}
	if account == nil {
		writeError(w, http.StatusNotFound, "account not found")
		return
	}

	response, statusCode, err := testModelChat(r.Context(), account, req.Model)
	if err != nil {
		writeJSON(w, map[string]interface{}{
			"account_id":  req.AccountID,
			"model":       req.Model,
			"status_code": statusCode,
			"error":       err.Error(),
		})
		return
	}

	writeJSON(w, map[string]interface{}{
		"account_id":  req.AccountID,
		"model":       req.Model,
		"status_code": statusCode,
		"response":    response,
	})
}

// TestAllModels 测试指定账号在所有模型下的响应
func (h *AdminHandler) TestAllModels(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AccountID string `json:"account_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.AccountID == "" {
		writeError(w, http.StatusBadRequest, "account_id is required")
		return
	}

	var account *config.Account
	cfg := config.Get()
	for i := range cfg.Accounts {
		if cfg.Accounts[i].ID == req.AccountID {
			account = &cfg.Accounts[i]
			break
		}
	}
	if account == nil {
		writeError(w, http.StatusNotFound, "account not found")
		return
	}

	models := []string{"mimo-v2.5", "mimo-v2.5-pro"}
	results := make([]map[string]interface{}, 0, len(models))

	for _, model := range models {
		response, statusCode, err := testModelChat(r.Context(), account, model)
		result := map[string]interface{}{
			"model":       model,
			"status_code": statusCode,
		}
		if err != nil {
			result["error"] = err.Error()
		} else {
			result["response"] = response
		}
		results = append(results, result)
	}

	writeJSON(w, map[string]interface{}{
		"account_id": req.AccountID,
		"results":    results,
	})
}

// TestPoolAll 对账号池中所有活跃账号执行健康检查
func (h *AdminHandler) TestPoolAll(w http.ResponseWriter, r *http.Request) {
	results := h.pool.HealthCheck(r.Context())
	writeJSON(w, map[string]interface{}{
		"total":   len(results),
		"healthy": countHealthy(results),
		"results": results,
	})
}

// ExportAccounts 导出所有账号（不包含敏感配置）
func (h *AdminHandler) ExportAccounts(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	export := make([]config.Account, len(cfg.Accounts))
	for i, acc := range cfg.Accounts {
		export[i] = config.Account{
			ID:           acc.ID,
			ServiceToken: acc.ServiceToken,
			UserID:       acc.UserID,
			Ph:           acc.Ph,
			Active:       acc.Active,
		}
	}

	data, err := json.MarshalIndent(export, "", "  ")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to marshal")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="mimo-accounts.json"`)
	w.Write(data)
}

// ImportAccounts 导入账号
func (h *AdminHandler) ImportAccounts(w http.ResponseWriter, r *http.Request) {
	var accounts []config.Account
	if err := json.NewDecoder(r.Body).Decode(&accounts); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body, expected array of accounts")
		return
	}

	var added, skipped, failed int
	config.Update(func(cfg *config.Config) {
		existingIDs := make(map[string]bool)
		for _, acc := range cfg.Accounts {
			existingIDs[acc.ID] = true
		}

		for _, acc := range accounts {
			if acc.ID == "" || acc.ServiceToken == "" || acc.UserID == "" || acc.Ph == "" {
				failed++
				continue
			}
			if existingIDs[acc.ID] {
				skipped++
				continue
			}
			cfg.Accounts = append(cfg.Accounts, acc)
			existingIDs[acc.ID] = true
			added++
		}
	})
	config.Save()
	h.pool.Reload(config.Get().Accounts)

	writeJSON(w, map[string]interface{}{
		"status":  "ok",
		"added":   added,
		"skipped": skipped,
		"failed":  failed,
	})
}

// UpdateCookie 更新账号的 Cookie 字段
func (h *AdminHandler) UpdateCookie(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AccountID    string `json:"account_id"`
		ServiceToken string `json:"service_token"`
		UserID       string `json:"user_id"`
		Ph           string `json:"ph"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.AccountID == "" {
		writeError(w, http.StatusBadRequest, "account_id is required")
		return
	}

	found := false
	config.Update(func(cfg *config.Config) {
		for i := range cfg.Accounts {
			if cfg.Accounts[i].ID == req.AccountID {
				found = true
				if req.ServiceToken != "" {
					cfg.Accounts[i].ServiceToken = req.ServiceToken
				}
				if req.UserID != "" {
					cfg.Accounts[i].UserID = req.UserID
				}
				if req.Ph != "" {
					cfg.Accounts[i].Ph = req.Ph
				}
				break
			}
		}
	})

	if !found {
		writeError(w, http.StatusNotFound, "account not found")
		return
	}
	config.Save()
	h.pool.Reload(config.Get().Accounts)

	// 返回更新后的账号信息
	cfg := config.Get()
	for _, acc := range cfg.Accounts {
		if acc.ID == req.AccountID {
			writeJSON(w, map[string]interface{}{
				"status":  "ok",
				"account": acc,
			})
			return
		}
	}
}

// SetPassword 设置密码（允许随时覆盖）
func (h *AdminHandler) SetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Password == "" {
		writeError(w, http.StatusBadRequest, "password is required")
		return
	}

	hash, err := hashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to hash password")
		return
	}

	config.Update(func(cfg *config.Config) {
		cfg.AdminPassword = hash
		cfg.PasswordSet = true
	})
	config.Save()

	writeJSON(w, map[string]string{"status": "ok"})
}

// Login 登录验证
func (h *AdminHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Password == "" {
		writeError(w, http.StatusBadRequest, "password is required")
		return
	}

	cfg := config.Get()
	if !cfg.PasswordSet {
		writeError(w, http.StatusForbidden, "password not set yet")
		return
	}

	if !checkPasswordHash(req.Password, cfg.AdminPassword) {
		writeError(w, http.StatusUnauthorized, "invalid password")
		return
	}

	token := generateSessionToken()
	activeSessions.Store(token, time.Now().Add(24*time.Hour))

	http.SetCookie(w, &http.Cookie{
		Name:     "admin_session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400,
	})

	writeJSON(w, map[string]string{"status": "ok", "token": token})
}

// ChangePassword 修改密码
func (h *AdminHandler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.OldPassword == "" || req.NewPassword == "" {
		writeError(w, http.StatusBadRequest, "old_password and new_password are required")
		return
	}

	cfg := config.Get()
	if !cfg.PasswordSet {
		writeError(w, http.StatusForbidden, "password not set yet")
		return
	}

	if !checkPasswordHash(req.OldPassword, cfg.AdminPassword) {
		writeError(w, http.StatusUnauthorized, "invalid old password")
		return
	}

	hash, err := hashPassword(req.NewPassword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to hash password")
		return
	}

	config.Update(func(cfg *config.Config) {
		cfg.AdminPassword = hash
	})
	config.Save()

	writeJSON(w, map[string]string{"status": "ok"})
}

// PasswordStatus 返回密码设置状态
func (h *AdminHandler) PasswordStatus(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	writeJSON(w, map[string]bool{
		"password_set": cfg.PasswordSet,
	})
}

// testModelChat 使用指定账号和模型发送测试消息
func testModelChat(ctx context.Context, acc *config.Account, model string) (string, int, error) {
	const webBaseURL = "https://aistudio.xiaomimimo.com"
	const chatAPI = "/open-apis/bot/chat"

	encodedPh := url.QueryEscape(acc.Ph)
	reqURL := fmt.Sprintf("%s%s?xiaomichatbot_ph=%s", webBaseURL, chatAPI, encodedPh)

	reqBody := map[string]interface{}{
		"msgId":          uuid.New().String(),
		"conversationId": uuid.New().String(),
		"query":          "hi",
		"messages":       []interface{}{},
		"parentId":       "0",
		"save":           false,
		"isEditedQuery":  false,
		"source":         "STATION",
		"scene":          "STATION",
		"isLocal":        false,
		"modelConfig": map[string]interface{}{
			"enableThinking":  false,
			"webSearchStatus": "disabled",
			"model":           model,
			"temperature":     0.8,
			"topP":            0.95,
		},
		"multiMedias": []interface{}{},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", 0, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", reqURL, bytes.NewReader(body))
	if err != nil {
		return "", 0, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Origin", webBaseURL)
	httpReq.Header.Set("Referer", webBaseURL+"/")
	httpReq.Header.Set("x-timezone", "Asia/Shanghai")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.0")
	httpReq.Header.Set("Cookie", fmt.Sprintf(
		"userId=%s; serviceToken=%q; xiaomichatbot_ph=%q",
		acc.UserID, acc.ServiceToken, acc.Ph,
	))

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", 0, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", resp.StatusCode, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 500))
	}

	// 流式读取 SSE，收集文本内容，避免 io.ReadAll 阻塞等待连接关闭
	var content strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return truncate(content.String(), 500), resp.StatusCode, nil
		default:
		}
		line := scanner.Text()
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimPrefix(line, "data:")
			data = strings.TrimSpace(data)
			if data == "" || data == "[DONE]" {
				continue
			}
			var evt map[string]interface{}
			if err := json.Unmarshal([]byte(data), &evt); err != nil {
				continue
			}
			if choices, ok := evt["choices"].([]interface{}); ok && len(choices) > 0 {
				if choice, ok := choices[0].(map[string]interface{}); ok {
					if delta, ok := choice["delta"].(map[string]interface{}); ok {
						if text, ok := delta["text"].(string); ok {
							content.WriteString(text)
						}
					}
				}
			}
			if evtData, ok := evt["data"].(map[string]interface{}); ok {
				if text, ok := evtData["text"].(string); ok {
					content.WriteString(text)
				}
			}
		}
	}

	result := content.String()
	if result == "" {
		result = "(模型响应成功但内容为空)"
	}
	return truncate(result, 500), resp.StatusCode, nil
}

// extractTextFromSSE 从 SSE 响应中提取文本内容
func extractTextFromSSE(sseBody string) string {
	var content strings.Builder
	lines := strings.Split(sseBody, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimPrefix(line, "data:")
			data = strings.TrimSpace(data)
			if data == "" || data == "[DONE]" {
				continue
			}
			var evt map[string]interface{}
			if err := json.Unmarshal([]byte(data), &evt); err != nil {
				continue
			}
			// 尝试提取文本内容
			if choices, ok := evt["choices"].([]interface{}); ok && len(choices) > 0 {
				if choice, ok := choices[0].(map[string]interface{}); ok {
					if delta, ok := choice["delta"].(map[string]interface{}); ok {
						if text, ok := delta["text"].(string); ok {
							content.WriteString(text)
						}
					}
				}
			}
			// 也尝试从 event.data 中提取
			if evtData, ok := evt["data"].(map[string]interface{}); ok {
				if text, ok := evtData["text"].(string); ok {
					content.WriteString(text)
				}
			}
		}
	}
	return content.String()
}

// truncate 截取字符串到指定长度
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// countHealthy 统计健康检查中健康的数量
func countHealthy(results map[string]bool) int {
	count := 0
	for _, v := range results {
		if v {
			count++
		}
	}
	return count
}

func writeJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}
