package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"

	"github.com/cagojeiger/drp/internal/server"
)

func main() {
	nodeID := flag.String("node-id", "", "unique node identifier")
	httpPort := flag.Int("http-port", 80, "HTTP listener port")
	controlPort := flag.Int("control-port", 9000, "control + mesh port")
	peers := flag.String("peers", "", "comma-separated peer addresses (host:port)")
	verbose := flag.Bool("v", false, "verbose logging")
	help := flag.Bool("help", false, "show help")

	flag.Parse()

	if *help {
		flag.Usage()
		os.Exit(0)
	}

	if *nodeID == "" {
		fmt.Fprintln(os.Stderr, "error: --node-id is required")
		os.Exit(1)
	}

	srv := server.New(server.Config{
		NodeID:      *nodeID,
		HTTPPort:    *httpPort,
		ControlPort: *controlPort,
		Peers:       *peers,
		Verbose:     *verbose,
	})

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)

	if err := srv.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		cancel()
		log.Fatal(err)
	}
	cancel()
}
