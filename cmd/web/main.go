package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"

	"github.com/ph-nixx/site/cmd/web/app"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP network address")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{AddSource: true}))
	app, err := app.New(logger)
	if err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}

	if err := http.ListenAndServe(*addr, app.Routes()); err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}
}
