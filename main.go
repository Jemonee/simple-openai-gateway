package main

import (
	"embed"
	"log"

	"github.com/Jemonee/simple-openai-gateway/cmd"
	"github.com/Jemonee/simple-openai-gateway/internal/config"
)

//go:embed all:frontend/dist/**
var staticFS embed.FS

func main() {
	if err := config.LoadRuntimeEnv(); err != nil {
		log.Fatalf("load runtime environment: %v", err)
	}
	run := make(chan int)
	app := cmd.InitializeApp()
	app.Start(staticFS)
	<-run
}
