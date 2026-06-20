package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/zhangguoguo1314/mimo-free-api/internal/config"
	"github.com/zhangguoguo1314/mimo-free-api/internal/convstore"
	"github.com/zhangguoguo1314/mimo-free-api/internal/handler"
	"github.com/zhangguoguo1314/mimo-free-api/internal/pool"
)

// New 创建并返回 HTTP server
func New(cfg *config.Config, pool *pool.Pool) http.Handler {
	r := chi.NewRouter()

	// 中间件
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

	// API Key 验证中间件
	apiKeyAuth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 管理接口不需要验证
			if r.URL.Path[:8] == "/admin/" || r.URL.Path == "/" {
				next.ServeHTTP(w, r)
				return
			}

			key := r.Header.Get("Authorization")
			if key == "" {
				key = r.Header.Get("api-key")
			}
			// 去掉 Bearer 前缀
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

	convStore := convstore.New()
	chatHandler := handler.NewChatHandler(pool, convStore)
	messagesHandler := handler.NewMessagesHandler(pool, convStore)
	adminHandler := handler.NewAdminHandler(pool)

	// OpenAI 兼容路由
	r.Route("/v1", func(r chi.Router) {
		r.Use(apiKeyAuth)
		r.Post("/chat/completions", chatHandler.Handle)
		r.Get("/models", handler.ModelsHandler)
		r.Get("/models/{id}", handler.ModelsHandler)

		// Anthropic 兼容
		r.Post("/messages", messagesHandler.Handle)
		r.Post("/messages/count_tokens", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"input_tokens":0}`))
		})
	})

	// Anthropic 快捷路径
	r.Post("/anthropic/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		messagesHandler.Handle(w, r)
	})

	// 管理接口
	r.Route("/admin/api", func(r chi.Router) {
		r.Get("/config", adminHandler.GetConfig)
		r.Post("/config", adminHandler.UpdateConfig)
		r.Post("/accounts", adminHandler.AddAccount)
		r.Get("/health", adminHandler.HealthCheck)
	})

	// 临时端点：捕获浏览器 Cookie（用于获取 HTTP Only serviceToken）
	r.Get("/capture-cookie", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		w.Header().Set("Content-Type", "application/json")
		cookie := r.Header.Get("Cookie")
		w.Write([]byte(`{"cookie":"` + cookie + `"}`))
	})

	return r
}
