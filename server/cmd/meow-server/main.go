// Command meow-server runs the Meow API: one process, one SQLite database.
//
// Configuration is read from the environment:
//
//	MEOW_ADDR           listen address (default :8080)
//	MEOW_DB             SQLite database path (default meow.db); keep it on a persistent volume
//	MEOW_RETENTION      how long resolved questions and notifications are kept (default 720h)
//	MEOW_TRUST_PROXY    "true" to take the client IP from X-Forwarded-For (behind a reverse proxy)
//	MEOW_LOG_LEVEL      debug | info | warn | error (default info)
//	MEOW_APNS_KEY_FILE  path to the APNs .p8 auth key; push is disabled when unset
//	MEOW_APNS_KEY_ID    APNs key ID
//	MEOW_APNS_TEAM_ID   Apple developer team ID
//	MEOW_APNS_TOPIC     iOS app bundle identifier
//
// "meow-server backup <path>" writes a consistent snapshot of the live database.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"meow.blue/meow/server/internal/api"
	"meow.blue/meow/server/internal/push"
	"meow.blue/meow/server/internal/store"
)

var version = "dev"

func main() {
	var err error
	switch {
	case len(os.Args) > 1 && os.Args[1] == "backup":
		err = backup(os.Args[2:])
	case len(os.Args) > 1 && os.Args[1] == "version":
		fmt.Println("meow-server", version)
	case len(os.Args) > 1:
		err = fmt.Errorf("unknown command %q (usage: meow-server [backup <path> | version])", os.Args[1])
	default:
		err = run()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "meow-server:", err)
		os.Exit(1)
	}
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func newLogger() *slog.Logger {
	var level slog.Level
	if err := level.UnmarshalText([]byte(env("MEOW_LOG_LEVEL", "info"))); err != nil {
		level = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

func newPusher(log *slog.Logger) (push.Pusher, error) {
	keyFile := env("MEOW_APNS_KEY_FILE", "")
	if keyFile == "" {
		log.Warn("MEOW_APNS_KEY_FILE not set: notifications are stored but not pushed")
		return push.Disabled{Log: log}, nil
	}
	p8, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}
	return push.NewAPNs(p8, env("MEOW_APNS_KEY_ID", ""), env("MEOW_APNS_TEAM_ID", ""), env("MEOW_APNS_TOPIC", ""))
}

func run() error {
	log := newLogger()
	retention, err := time.ParseDuration(env("MEOW_RETENTION", "720h"))
	if err != nil || retention < time.Hour {
		return fmt.Errorf("MEOW_RETENTION must be a duration of at least 1h, e.g. 720h")
	}
	pusher, err := newPusher(log)
	if err != nil {
		return err
	}
	dbPath := env("MEOW_DB", "meow.db")
	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	bg, stopBG := context.WithCancel(context.Background())
	defer stopBG()
	var wg sync.WaitGroup

	dispatcher := push.NewDispatcher(st, pusher, log)
	wg.Go(func() { dispatcher.Run(bg, 4) })

	const maxWait = 60 * time.Second
	srv := api.New(api.Config{
		Store: st, Push: dispatcher, Log: log, MaxWait: maxWait,
		TrustProxy: env("MEOW_TRUST_PROXY", "") == "true",
	})
	wg.Go(func() { maintain(bg, srv, st, retention, log) })

	hs := &http.Server{
		Addr:              env("MEOW_ADDR", ":8080"),
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      maxWait + 30*time.Second, // must outlast long polls
		IdleTimeout:       2 * time.Minute,
	}
	hs.RegisterOnShutdown(srv.Close) // release long polls so shutdown is quick

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	log.Info("meow-server started", "version", version, "addr", hs.Addr, "db", dbPath)

	select {
	case err = <-errc:
	case <-ctx.Done():
		log.Info("shutting down")
		sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err = hs.Shutdown(sctx)
		cancel()
	}
	stopBG()
	wg.Wait()
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

// maintain expires overdue questions every few seconds and prunes old data hourly.
func maintain(ctx context.Context, srv *api.Server, st *store.Store, retention time.Duration, log *slog.Logger) {
	expire := time.NewTicker(5 * time.Second)
	defer expire.Stop()
	prune := time.NewTicker(time.Hour)
	defer prune.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-expire.C:
			if err := srv.ExpireDue(ctx); err != nil && ctx.Err() == nil {
				log.Error("expire questions", "err", err)
			}
		case <-prune.C:
			n, err := st.Prune(ctx, retention)
			if err != nil && ctx.Err() == nil {
				log.Error("prune", "err", err)
			} else if n > 0 {
				log.Info("pruned old records", "rows", n)
			}
		}
	}
}

func backup(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: meow-server backup <path>")
	}
	if _, err := os.Stat(args[0]); err == nil {
		return fmt.Errorf("%s already exists", args[0])
	}
	st, err := store.Open(env("MEOW_DB", "meow.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Backup(context.Background(), args[0]); err != nil {
		return err
	}
	fmt.Println("backup written to", args[0])
	return nil
}
