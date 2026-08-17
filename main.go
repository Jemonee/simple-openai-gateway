package main

import (
	"embed"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/Jemonee/simple-openai-gateway/cmd"
	"github.com/Jemonee/simple-openai-gateway/internal/app"
	"github.com/Jemonee/simple-openai-gateway/internal/config"
)

//go:embed all:frontend/dist/**
var staticFS embed.FS

func main() {
	startupOptions, err := parseStartupOptions(os.Args[1:])
	if err != nil {
		log.Fatalf("parse startup options: %v", err)
	}
	if err := config.LoadRuntimeEnv(); err != nil {
		log.Fatalf("load runtime environment: %v", err)
	}
	run := make(chan int)
	application := cmd.InitializeApp()
	application.Start(staticFS, startupOptions)
	<-run
}

func parseStartupOptions(args []string) (app.StartupOptions, error) {
	flags := flag.NewFlagSet("simple-openai-gateway", flag.ContinueOnError)
	disableIFrame := flags.Bool("disable-iframe", false, "deny iframe embedding with X-Frame-Options: DENY")
	if err := flags.Parse(args); err != nil {
		return app.StartupOptions{}, err
	}
	if flags.NArg() != 0 {
		return app.StartupOptions{}, fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	return app.StartupOptions{DisableIFrame: *disableIFrame}, nil
}
