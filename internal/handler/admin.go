package handler

import (
	"encoding/json"
	"fmt"
	"net/http"

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

// testAccountValidity 测试账号是否有效
func testAccountValidity(acc *config.Account) bool {
	// 构建测试请求
	client := &http.Client{}
	req, err := http.NewRequest("GET", "https://aistudio.xiaomimimo.com/api/user/info", nil)
	if err != nil {
		return false
	}

	// 设置 Cookie
	cookie := fmt.Sprintf("serviceToken=%s; userId=%s; xiaomichatbot_ph=%s",
		acc.ServiceToken, acc.UserID, acc.PH)
	req.Header.Set("Cookie", cookie)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.0")

	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	// 如果返回 200，说明账号有效
	return resp.StatusCode == 200
}

func writeJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}
