package main

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/zhangguoguo1314/mimo-free-api/internal/config"
	"github.com/zhangguoguo1314/mimo-free-api/internal/convstore"
	"github.com/zhangguoguo1314/mimo-free-api/internal/handler"
	"github.com/zhangguoguo1314/mimo-free-api/internal/pool"
	"github.com/zhangguoguo1314/mimo-free-api/internal/stats"
)

//go:embed all:static/*
var staticFiles embed.FS

func main() {
	configPath := "config.json"
	if p := os.Getenv("CONFIG_PATH"); p != "" {
		configPath = p
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		// Fallback: try /data/config.json (HF Space persistent storage)
		if configPath != "/data/config.json" {
			cfg, err = config.Load("/data/config.json")
		}
		if err != nil {
			log.Fatalf("Failed to load config (tried %s and /data/config.json): %v", configPath, err)
		}
	}

	// 初始化统计追踪 (use /data for HF Space persistence)
	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "data"
	}
	stats.Init(dataDir + "/stats.json")
	stats.InitConvStore(dataDir + "/conversations.json")

	// 初始化账号池
	poolCfg := pool.PoolConfig{
		MaxConcurrent: pool.DefaultPoolConfig.MaxConcurrent,
		CooldownTime:  pool.DefaultPoolConfig.CooldownTime,
		RateLimit:     pool.DefaultPoolConfig.RateLimit,
	}
	if cfg.Pool.MaxConcurrent > 0 {
		poolCfg.MaxConcurrent = cfg.Pool.MaxConcurrent
	}
	if cfg.Pool.CooldownTime > 0 {
		poolCfg.CooldownTime = time.Duration(cfg.Pool.CooldownTime) * time.Second
	}
	if cfg.Pool.RateLimit > 0 {
		poolCfg.RateLimit = cfg.Pool.RateLimit
	}
	if cfg.Pool.DailyLimit > 0 {
		poolCfg.DailyLimit = cfg.Pool.DailyLimit
	}
	if cfg.Pool.JitterMax > 0 {
		poolCfg.JitterMax = time.Duration(cfg.Pool.JitterMax) * time.Millisecond
	}
	accountPool := pool.NewWithConfig(cfg.Accounts, poolCfg)

	// 启动后台定时健康检查（每 10 分钟）
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			log.Printf("[health-check] starting periodic health check...")
			results := accountPool.HealthCheck(context.Background())
			healthy := 0
			for _, ok := range results {
				if ok {
					healthy++
				}
			}
			log.Printf("[health-check] completed: %d/%d healthy", healthy, len(results))
		}
	}()

	// 路由
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"*"},
		ExposedHeaders:   []string{"*"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	convStore := convstore.New()
	chatHandler := handler.NewChatHandler(accountPool, convStore)
	messagesHandler := handler.NewMessagesHandler(accountPool, convStore)
	adminHandler := handler.NewAdminHandler(accountPool)

	// OpenAI 兼容
	r.Route("/v1", func(r chi.Router) {
		r.Use(apiKeyAuth)
		r.Post("/chat/completions", chatHandler.Handle)
		r.Get("/models", handler.ModelsHandler)
		r.Get("/models/{id}", handler.ModelsHandler)
		r.Post("/messages", messagesHandler.Handle)
		r.Post("/messages/count_tokens", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"input_tokens":0}`))
		})
	})

	// Anthropic 快捷路径
	r.With(apiKeyAuth).Post("/anthropic/v1/messages", messagesHandler.Handle)

	// 管理接口
	r.Route("/admin/api", func(r chi.Router) {
		// 不需要认证的路由
		r.Get("/password/status", adminHandler.PasswordStatus)
		r.Post("/password/set", adminHandler.SetPassword)
		r.Post("/password/login", adminHandler.Login)

		// 需要认证的路由
		r.Group(func(r chi.Router) {
			r.Use(handler.AdminAuth)
			r.Get("/config", adminHandler.GetConfig)
			r.Post("/config", adminHandler.UpdateConfig)
			r.Post("/accounts", adminHandler.AddAccount)
			r.Delete("/accounts", adminHandler.DeleteAccount)
			r.Post("/accounts/toggle", adminHandler.ToggleAccount)
			r.Post("/accounts/test", adminHandler.TestAccount)
			r.Post("/accounts/test-model", adminHandler.TestModel)
			r.Post("/accounts/test-all-models", adminHandler.TestAllModels)
			r.Get("/accounts/export", adminHandler.ExportAccounts)
			r.Post("/accounts/import", adminHandler.ImportAccounts)
			r.Put("/accounts/cookie", adminHandler.UpdateCookie)
			r.Post("/pool/test-all", adminHandler.TestPoolAll)
			r.Get("/pool/status", adminHandler.PoolStatus)
			r.Get("/health", adminHandler.HealthCheck)
			r.Get("/stats", adminHandler.GetStats)
			r.Post("/password/change", adminHandler.ChangePassword)
		})
	})

	// 前端 SPA
	staticFS, _ := fs.Sub(staticFiles, "static")
	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" {
			path = "/index.html"
		}
		filePath := path[1:]
		if data, err := fs.ReadFile(staticFS, filePath); err == nil {
			ext := filePath[len(filePath)-len(getExt(filePath)):]
			w.Header().Set("Content-Type", contentType(ext))
			w.Write(data)
			return
		}
		if data, err := fs.ReadFile(staticFS, "index.html"); err == nil {
			w.Header().Set("Content-Type", "text/html")
			w.Write(data)
			return
		}
		http.NotFound(w, r)
	})

	addr := fmt.Sprintf(":%s", cfg.Port)
	log.Printf("🚀 MiMo Gateway starting on http://localhost%s", addr)
	log.Printf("   API Key: %s", cfg.APIKey)
	log.Printf("   Accounts: %d", accountPool.Count())

	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

func apiKeyAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Authorization")
		if key == "" {
			key = r.Header.Get("api-key")
		}
		if len(key) > 7 && key[:7] == "Bearer " {
			key = key[7:]
		}
		currentCfg := config.Get()
		if key != currentCfg.APIKey {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"message":"Invalid API key","type":"authentication_error"}}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func getExt(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '.' {
			return path[i:]
		}
		if path[i] == '/' {
			return ""
		}
	}
	return ""
}

func contentType(ext string) string {
	switch ext {
	case ".html":
		return "text/html"
	case ".css":
		return "text/css"
	case ".js":
		return "application/javascript"
	case ".json":
		return "application/json"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	default:
		return "application/octet-stream"
	}
}
