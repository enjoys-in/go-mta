package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/enjoys-in/go-mta/pkg/configloader"
	"github.com/enjoys-in/go-mta/pkg/events"
	"github.com/enjoys-in/go-mta/pkg/logger"
	"github.com/enjoys-in/go-mta/pkg/server"
	"github.com/enjoys-in/go-mta/pkg/types"
)

func main() {
	cfgPath := flag.String("config", "", "path to TOML config file")
	cfgDir := flag.String("config-dir", "", "path to TOML config directory (loads ips.toml, domains.toml, etc.)")
	flag.Parse()

	log := logger.New("main")

	var cfg *configloader.ServerConfig
	switch {
	case *cfgDir != "":
		var err error
		cfg, err = configloader.LoadDir(*cfgDir)
		if err != nil {
			log.Error("failed to load config dir", err)
			os.Exit(1)
		}
	case *cfgPath != "":
		var err error
		cfg, err = configloader.LoadFile(*cfgPath)
		if err != nil {
			log.Error("failed to load config", err)
			os.Exit(1)
		}
	default:
		cfg = configloader.LoadEnv()
	}

	srv, err := server.New(cfg)
	if err != nil {
		log.Error("failed to create server", err)
		os.Exit(1)
	}

	// Register example event listeners.
	srv.Events().On(types.EventDeliverySuccess, func(evt events.Event) {
		fmt.Printf("[OK] job=%s from=%s method=%s attempts=%d\n",
			evt.JobID, evt.From, evt.Method, evt.Attempt)
	})
	srv.Events().On(types.EventDeliveryFailed, func(evt events.Event) {
		fmt.Printf("[FAIL] job=%s from=%s error=%v\n",
			evt.JobID, evt.From, evt.Err)
	})
	srv.Events().On(types.EventBounceDetected, func(evt events.Event) {
		fmt.Printf("[BOUNCE] job=%s category=%s code=%s\n",
			evt.JobID, evt.Meta["bounce_category"], evt.Meta["smtp_code"])
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv.Start(ctx)
	log.Info("gomta running. Waiting for signals...")

	<-ctx.Done()
	log.Info("signal received, shutting down...")
	srv.Shutdown(context.Background())
}
