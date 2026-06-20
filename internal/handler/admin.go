package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zhangguoguo1314/mimo-free-api/internal/config"
	"github.com/zhangguoguo1314/mimo-free-api/internal/pool"
	"github.com/zhangguoguo1314/mimo-free-api/internal/stats"
)

// cleanToken 清理 token 字符串首尾的空格和引号
func cleanToken(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"")
	return s
}

// validIDPattern 校验 ID 格式
var validIDPattern = regexp.MustCompile(`^[a-zA-Z0-9._-]{2,64}$`)

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

	// 清理所有字段
	acc.ID = cleanToken(acc.ID)
	acc.ServiceToken = cleanToken(acc.ServiceToken)
	acc.UserID = cleanToken(acc.UserID)
	acc.Ph = cleanToken(acc.Ph)

	if acc.ID == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}

	// ID 格式校验
	if !validIDPattern.MatchString(acc.ID) {
		writeError(w, http.StatusBadRequest, "id format invalid, only alphanumeric, dot, hyphen, underscore allowed (2-64 chars)")
		return
	}

	// 设置来源和时间戳
	acc.Source = "manual"
	acc.AddedAt = time.Now().UnixNano()

	var duplicate bool
	config.Update(func(cfg *config.Config) {
		// 重复检测
		for _, existing := range cfg.Accounts {
			if existing.ID == acc.ID {
				duplicate = true
				return
			}
		}
		acc.Active = true // 新添加的账号默认启用
		cfg.Accounts = append(cfg.Accounts, acc)
	})
	if duplicate {
		writeError(w, http.StatusConflict, "account id already exists")
		return
	}
	config.Save()
	h.pool.Reload(config.Get().Accounts)

	// 异步验证新账号
	go func() {
		accCopy := acc
		if !testAccountValidity(&accCopy) {
			// 找到对应的 WebClient 并标记不健康
			cfg := config.Get()
			for _, a := range cfg.Accounts {
				if a.ID == acc.ID {
					client := h.pool.GetClientByID(acc.ID)
					if client != nil {
						h.pool.MarkUnhealthy(client)
					}
					break
				}
			}
		}
	}()

	writeJSON(w, map[string]string{"status": "added"})
}

func (h *AdminHandler) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		writeError(w, http.StatusBadRequest, "id is required")
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
		writeError(w, http.StatusNotFound, "account not found")
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
// 使用与官方网页端完全一致的精简请求格式，避免多余字段导致空回复
func testAccountValidity(acc *config.Account) bool {
	// 清理字段
	acc.Ph = cleanToken(acc.Ph)
	acc.ServiceToken = cleanToken(acc.ServiceToken)
	acc.UserID = cleanToken(acc.UserID)

	client := &http.Client{Timeout: 15 * time.Second}

	const webBaseURL = "https://aistudio.xiaomimimo.com"
	encodedPh := url.QueryEscape(acc.Ph)
	reqURL := fmt.Sprintf("%s/open-apis/bot/chat?xiaomichatbot_ph=%s", webBaseURL, encodedPh)

	// 精简请求格式：只包含官网实际发送的字段
	reqBody := map[string]interface{}{
		"msgId":          uuid.New().String(),
		"conversationId": uuid.New().String(),
		"query":          "hi",
		"isEditedQuery":  false,
		"modelConfig": map[string]interface{}{
			"enableThinking":  false,
			"webSearchStatus": "disabled",
			"model":           "mimo-v2.5",
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

	cookie := fmt.Sprintf("userId=%s; serviceToken=%s; xiaomichatbot_ph=%s",
		acc.UserID, acc.ServiceToken, acc.Ph)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Origin", webBaseURL)
	req.Header.Set("Referer", webBaseURL+"/")
	req.Header.Set("x-timezone", "Asia/Shanghai")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")

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

// PoolStatus 返回账号池中每个账号的详细状态（并发数、冷却、速率）
func (h *AdminHandler) PoolStatus(w http.ResponseWriter, r *http.Request) {
	statuses := h.pool.Status()
	writeJSON(w, map[string]interface{}{
		"total":   len(statuses),
		"accounts": statuses,
	})
}

// ExportAccounts 导出所有账号（默认脱敏，?full=true 导出完整信息）
func (h *AdminHandler) ExportAccounts(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	full := r.URL.Query().Get("full") == "true"

	export := make([]config.Account, len(cfg.Accounts))
	for i, acc := range cfg.Accounts {
		if full {
			export[i] = acc
		} else {
			// 脱敏导出
			maskedToken := acc.ServiceToken
			if len(maskedToken) > 8 {
				maskedToken = maskedToken[:8] + "..."
			}
			maskedUserID := acc.UserID
			if len(maskedUserID) > 4 {
				maskedUserID = maskedUserID[:4] + "..."
			}
			maskedPh := acc.Ph
			if len(maskedPh) > 4 {
				maskedPh = maskedPh[:4] + "..."
			}
			export[i] = config.Account{
				ID:           acc.ID,
				ServiceToken: maskedToken,
				UserID:       maskedUserID,
				Ph:           maskedPh,
				Active:       acc.Active,
				Source:       acc.Source,
				AddedAt:      acc.AddedAt,
			}
		}
	}

	data, err := json.MarshalIndent(map[string]interface{}{"accounts": export}, "", "  ")
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
	// 支持两种格式: 直接数组 [...] 和包裹对象 {"accounts": [...]}
	var accounts []config.Account
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read body")
		return
	}

	// 先尝试直接作为数组解析
	if err := json.Unmarshal(bodyBytes, &accounts); err != nil {
		// 再尝试作为包裹对象解析
		var wrapper struct {
			Accounts []config.Account `json:"accounts"`
		}
		if err2 := json.Unmarshal(bodyBytes, &wrapper); err2 != nil || wrapper.Accounts == nil {
			writeError(w, http.StatusBadRequest, "invalid body, expected array of accounts or {accounts: [...]}")
			return
		}
		accounts = wrapper.Accounts
	}

	var added, skipped int
	var importErrors []map[string]string

	config.Update(func(cfg *config.Config) {
		existingIDs := make(map[string]bool)
		for _, acc := range cfg.Accounts {
			existingIDs[acc.ID] = true
		}

		for _, acc := range accounts {
			// 清理所有字段
			acc.ID = cleanToken(acc.ID)
			acc.ServiceToken = cleanToken(acc.ServiceToken)
			acc.UserID = cleanToken(acc.UserID)
			acc.Ph = cleanToken(acc.Ph)

			if acc.ID == "" || acc.ServiceToken == "" || acc.UserID == "" || acc.Ph == "" {
				importErrors = append(importErrors, map[string]string{
					"id":     acc.ID,
					"reason": "missing required fields (id, service_token, user_id, ph)",
				})
				continue
			}

			// ID 格式校验
			if !validIDPattern.MatchString(acc.ID) {
				importErrors = append(importErrors, map[string]string{
					"id":     acc.ID,
					"reason": "id format invalid",
				})
				continue
			}

			if existingIDs[acc.ID] {
				skipped++
				continue
			}

			// 设置来源、时间戳和激活状态
			acc.Source = "file"
			acc.AddedAt = time.Now().UnixNano()
			acc.Active = true
			cfg.Accounts = append(cfg.Accounts, acc)
			existingIDs[acc.ID] = true
			added++
		}
	})
	config.Save()
	h.pool.Reload(config.Get().Accounts)

	resp := map[string]interface{}{
		"status":  "ok",
		"added":   added,
		"skipped": skipped,
	}
	if len(importErrors) > 0 {
		resp["errors"] = importErrors
	}
	writeJSON(w, resp)
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

	// 清理所有字段
	req.AccountID = cleanToken(req.AccountID)
	req.ServiceToken = cleanToken(req.ServiceToken)
	req.UserID = cleanToken(req.UserID)
	req.Ph = cleanToken(req.Ph)

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

	// 异步验证新 cookie
	go func() {
		cfg := config.Get()
		for _, acc := range cfg.Accounts {
			if acc.ID == req.AccountID {
				accCopy := acc
				if !testAccountValidity(&accCopy) {
					client := h.pool.GetClientByID(req.AccountID)
					if client != nil {
						h.pool.MarkUnhealthy(client)
					}
				}
				break
			}
		}
	}()

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
// testModelChat 测试指定账号在指定模型下的响应
// 使用与官方网页端完全一致的精简请求格式，避免多余字段导致空回复
func testModelChat(ctx context.Context, acc *config.Account, model string) (string, int, error) {
	const webBaseURL = "https://aistudio.xiaomimimo.com"
	const chatAPI = "/open-apis/bot/chat"

	encodedPh := url.QueryEscape(acc.Ph)
	reqURL := fmt.Sprintf("%s%s?xiaomichatbot_ph=%s", webBaseURL, chatAPI, encodedPh)

	// 精简请求格式：只包含官网实际发送的字段
	reqBody := map[string]interface{}{
		"msgId":          uuid.New().String(),
		"conversationId": uuid.New().String(),
		"query":          "hi",
		"isEditedQuery":  false,
		"modelConfig": map[string]interface{}{
			"enableThinking":  false,
			"webSearchStatus": "disabled",
			"model":           model,
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
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")
	httpReq.Header.Set("Cookie", fmt.Sprintf(
			"userId=%s; serviceToken=%s; xiaomichatbot_ph=%s",
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

	// 读取SSE响应（使用LimitReader避免内存问题，设置ctx超时控制总时间）
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024)) // 最多读取1MB

	content, errMsg := extractTextFromSSE(string(respBody))
	if content == "" && errMsg != "" {
		content = errMsg
	}
	if content == "" {
		content = truncate(string(respBody), 500)
	}

	return truncate(content, 500), resp.StatusCode, nil
}

// extractTextFromSSE 从 SSE 响应中提取文本内容
// extractTextFromSSE 从 MiMo SSE 响应中提取文本内容
// 返回 (内容, 错误信息)
func extractTextFromSSE(sseBody string) (string, string) {
	var content strings.Builder
	var errMsg string
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
			// OpenAI 格式: choices[0].delta.text
			if choices, ok := evt["choices"].([]interface{}); ok && len(choices) > 0 {
				if choice, ok := choices[0].(map[string]interface{}); ok {
					if delta, ok := choice["delta"].(map[string]interface{}); ok {
						if text, ok := delta["text"].(string); ok {
							content.WriteString(text)
						}
					}
				}
			}
			// MiMo 格式: 提取 type=="text" 的内容
			if evtType, ok := evt["type"].(string); ok && evtType == "text" {
				if text, ok := evt["content"].(string); ok {
					if text != "[DONE]" {
						content.WriteString(text)
					}
				}
			}
			// MiMo 错误格式: 提取 type=="error" 的内容作为错误信息
			if evtType, ok := evt["type"].(string); ok && evtType == "error" {
				if text, ok := evt["content"].(string); ok && text != "" {
					errMsg = text
				}
			}
		}
	}
	return content.String(), errMsg
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
