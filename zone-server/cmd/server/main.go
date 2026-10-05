package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"zone-online/zone-server/internal/config"
	"zone-online/zone-server/internal/database"
	"zone-online/zone-server/internal/game"
)

func main() {
	configPath := flag.String("config", "zone_server.yaml", "path to the YAML configuration file")
	port := flag.Int("port", 0, "override UDP listen port (0 = use config)")
	dbPath := flag.String("db-path", "", "override SQLite database path (empty = use config)")
	tickRate := flag.Int("tick-rate", 0, "override tick rate in Hz (0 = use config)")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	if *port > 0 {
		cfg.Port = *port
	}
	if *dbPath != "" {
		cfg.DBPath = *dbPath
	}
	if *tickRate > 0 {
		cfg.TickRateHz = *tickRate
	}

	level := zapcore.InfoLevel
	switch strings.ToLower(strings.TrimSpace(cfg.LogLevel)) {
	case "debug":
		level = zapcore.DebugLevel
	case "info":
		level = zapcore.InfoLevel
	case "warn":
		level = zapcore.WarnLevel
	case "error":
		level = zapcore.ErrorLevel
	}

	zapCfg := zap.NewProductionConfig()
	zapCfg.Level = zap.NewAtomicLevelAt(level)
	logger, err := zapCfg.Build()
	if err != nil {
		log.Fatalf("Failed to build logger: %v", err)
	}
	defer logger.Sync()

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
	defer db.Close()

	if err := game.SeedSafeZones(db.RawDB()); err != nil {
		log.Fatalf("Failed to seed safe zones: %v", err)
	}

	dbQueue := db.StartWriteQueue(ctx)

	server := game.NewServer(cfg, db, logger, dbQueue)
	if err := server.InitUDP(); err != nil {
		log.Fatalf("Failed to initialize UDP listener: %v", err)
	}

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
