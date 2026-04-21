package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/Archive-At-Home/archive-at-home/node/internal/config"
	"github.com/Archive-At-Home/archive-at-home/node/internal/ehentai"
	"github.com/Archive-At-Home/archive-at-home/node/internal/node"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	log.Printf("Starting EHentai node %s", cfg.Node.ID)
	log.Printf("Connecting to server: %s", cfg.Server.URL)

	// Create EHentai client
	ehClient, err := ehentai.NewClient(
		cfg.EHentai.Cookie,
		cfg.EHentai.UseExhentai,
		cfg.EHentai.MaxGPCost,
		cfg.Database.Path,
	)
	if err != nil {
		log.Fatalf("Failed to create EHentai client: %v", err)
	}

	// Create node
	n := node.NewNode(
		cfg.Node.ID,
		cfg.Node.Signature,
		cfg.Server.URL,
		ehClient,
		cfg.EHentai.MaxGPCost,
		cfg.Task.BaseBalanceGP,
		cfg.Task.BaseClaimDelay,
	)

	// Start node
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Determine dashboard address
	dashboardAddr := ""
	if cfg.Dashboard.Enabled {
		dashboardAddr = cfg.Dashboard.Address
		log.Printf("Dashboard enabled on %s", dashboardAddr)
	}

	if err := n.Start(ctx, dashboardAddr); err != nil {
		log.Fatalf("Failed to start node: %v", err)
	}

	log.Printf("Node started successfully")

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	<-sigChan
	log.Printf("Shutting down...")

	cancel()
	if err := n.Stop(); err != nil {
		log.Printf("Error during shutdown: %v", err)
	}

	log.Printf("Node stopped")
}
