package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"modelrouter/internal/admin"
	"modelrouter/internal/config"
	"modelrouter/internal/metrics"
	"modelrouter/internal/proxy"
	"modelrouter/internal/router"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	configPath := flag.String("config", "config.json", "path to JSON config file")
	flag.Parse()

	cfg, err := config.LoadFile(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	store := router.NewStore(cfg)
	recorder := metrics.NewRecorder()
	proxyHandler := proxy.NewHandler(store, recorder)
	adminHandler := admin.NewHandler(store, recorder, *configPath).WithClientLimitProvider(proxyHandler)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", proxyHandler.Models)
	mux.HandleFunc("/v1/chat/completions", proxyHandler.ChatCompletions)
	mux.HandleFunc("/admin/config", adminHandler.Config)
	mux.HandleFunc("/admin/reload", adminHandler.Reload)
	mux.HandleFunc("/admin/overview", adminHandler.Overview)
	mux.HandleFunc("/admin/health", adminHandler.Health)
	mux.HandleFunc("/admin/limits", adminHandler.Limits)
	mux.HandleFunc("/admin/metrics", adminHandler.Metrics)
	mux.HandleFunc("/admin/metrics/", adminHandler.Metrics)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	server := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("modelrouter listening on %s", *addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
	proxyHandler.Close()
}
