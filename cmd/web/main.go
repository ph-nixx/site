package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ph-nixx/site/cmd/web/app"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP network address")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{AddSource: true}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app, err := app.New(ctx, logger)
	if err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}

	srv := &http.Server{Addr: *addr, Handler: app.Routes()}
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error(err.Error())
		}
	}()

	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		logger.Error(err.Error())
		os.Exit(1)
	}
	<-done
}
