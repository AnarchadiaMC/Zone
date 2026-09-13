package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"zone-online/zone-server/internal/config"
	"zone-online/zone-server/internal/database"
	"zone-online/zone-server/internal/game"
	"go.uber.org/zap"
)

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	cfg, err := config.Load("zone_server.yaml")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	logger.Info("Starting Zone Server", zap.Int("port", cfg.Port))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		logger.Info("Received shutdown signal")
		cancel()
	}()

	db, err := database.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("Failed to open DB: %v", err)
	}
	
	if err := game.SeedSafeZones(db.RawDB()); err != nil {
		log.Fatalf("Failed to seed safe zones: %v", err)
	}

	dbQueue := db.StartWriteQueue(ctx)

	server := game.NewServer(cfg, db, logger, dbQueue)

	adminServer := game.NewAdminServer(server, cfg.AdminPipe)
	if err := adminServer.Start(ctx); err != nil {
		logger.Warn("Failed to start admin server named pipe", zap.Error(err))
	} else {
		defer adminServer.Stop()
	}

	go server.RunGameLoop(ctx)
	if err := server.Run(ctx); err != nil && err != context.Canceled {
		logger.Error("Server error", zap.Error(err))
	}

	logger.Info("Server shutdown complete")
}
