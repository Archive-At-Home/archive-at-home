package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Archive-At-Home/archive-at-home/server/internal/auth"
	"github.com/Archive-At-Home/archive-at-home/server/internal/balance"
	"github.com/Archive-At-Home/archive-at-home/server/internal/config"
	"github.com/Archive-At-Home/archive-at-home/server/internal/handler"
	"github.com/Archive-At-Home/archive-at-home/server/internal/middleware"
	"github.com/Archive-At-Home/archive-at-home/server/internal/node"
	"github.com/Archive-At-Home/archive-at-home/server/internal/scheduler"
	"github.com/Archive-At-Home/archive-at-home/server/internal/service"
	"github.com/Archive-At-Home/archive-at-home/server/internal/store"
	"github.com/Archive-At-Home/archive-at-home/server/internal/ws"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

func main() {
	// ── Configuration ──
	cfg := config.Load()

	// ── Redis ──
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("failed to connect to redis: %v", err)
	}
	log.Println("connected to Redis at", cfg.RedisAddr)

	// ── SQL Store ──
	dbDSN := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		cfg.DBHost, cfg.DBPort, cfg.DBUser, cfg.DBPassword, cfg.DBName, cfg.DBSSLMode)
	st, err := store.NewStore(dbDSN)
	if err != nil {
		log.Fatalf("failed to init store: %v", err)
	}
	log.Printf("database initialised: %s@%s:%s/%s", cfg.DBUser, cfg.DBHost, cfg.DBPort, cfg.DBName)

	// ── Scheduler ──
	sched := scheduler.NewScheduler(rdb, cfg)

	// ── WebSocket Hub ──
	waiter := ws.NewResultWaiter()
	hub := ws.NewHub(sched, waiter)

	// ── User & Balance Services ──
	userSvc := auth.NewUserService(st.DB())
	balanceSvc := balance.NewBalanceService(st.DB())

	// ── Node Authenticator (ED25519) ──
	nodeAuth, err := node.NewAuthenticator(cfg.NodeVerifyKey)
	if err != nil {
		log.Fatalf("failed to init node authenticator: %v", err)
	}

	// ── Service ──
	svc := service.NewGalleryService(sched, hub, waiter, st, cfg, balanceSvc)

	// ── Lease Watchdog (background) ──
	watchdogCtx, watchdogCancel := context.WithCancel(ctx)
	defer watchdogCancel()
	go sched.StartLeaseWatchdog(watchdogCtx)

	// ── Gin Router ──
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(middleware.CORS())
	r.Use(middleware.Logger())

	h := handler.NewHandler(svc, hub, nodeAuth, cfg)
	authHandler := handler.NewAuthHandler(userSvc, cfg)
	userHandler := handler.NewUserHandler(userSvc, balanceSvc, cfg)
	adminHandler := handler.NewAdminHandler(userSvc, balanceSvc, hub)

	// Register routes with API key authentication
	authHandler.RegisterRoutes(r)
	h.RegisterRoutes(r, middleware.APIKeyAuth(userSvc))
	userHandler.RegisterRoutes(r.Group("/api/v1", middleware.APIKeyAuth(userSvc)))

	// Register admin routes with admin token authentication
	adminHandler.RegisterRoutes(r.Group("/api/v1/admin", middleware.AdminTokenAuth(cfg.AdminToken)))

	// ── HTTP Server with graceful shutdown ──
	srv := &http.Server{
		Addr:    cfg.ServerAddr,
		Handler: r,
	}

	go func() {
		log.Printf("server listening on %s", cfg.ServerAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen error: %v", err)
		}
	}()

	// ── Graceful Shutdown ──
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("shutting down server...")
	watchdogCancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(ctx, 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("server shutdown error: %v", err)
	}

	rdb.Close()
	log.Println("server exited cleanly")
}
