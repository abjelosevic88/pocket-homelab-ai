// Command pocket-ai is a web and phone front-end for the coding-agent CLIs
// installed on a server (Claude Code and Codex): it starts and resumes their
// sessions, streams what they do, relays permission prompts and images, and
// runs saved prompts on a schedule. One binary, one SQLite file. It runs as
// the Unix user whose CLIs and logins it drives.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/config"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/server"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
	"github.com/abjelosevic88/pocket-homelab-ai/web"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "--version", "-v":
			fmt.Println("pocket-ai", version)
			return
		case "backup":
			runBackup(os.Args[2:])
			return
		case "reset-password":
			runResetPassword(os.Args[2:])
			return
		case "help", "--help", "-h":
			fmt.Println(`pocket-ai — Claude Code and Codex on your server, from any browser or phone

Usage:
  pocket-ai                     start the server (default)
  pocket-ai backup [-o FILE]    write a consistent SQLite backup
  pocket-ai reset-password [-email USER] [-password NEW]
                                       set a user's password (default: first user, random password printed)
  pocket-ai version             print version

Configuration is read from environment variables, see docs/configuration.md.`)
			return
		}
	}
	serve()
}

func newLogger(cfg *config.Config) *slog.Logger {
	var level slog.Level
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	if cfg.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}

func mustLoad() (*config.Config, *slog.Logger, *store.Store) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	log := newLogger(cfg)
	st, err := store.Open(cfg.DBPath, log)
	if err != nil {
		log.Error("open database", "path", cfg.DBPath, "err", err)
		os.Exit(1)
	}
	return cfg, log, st
}

func serve() {
	server.Version = version
	cfg, log, st := mustLoad()
	defer st.Close()

	var webFS fs.FS
	if sub, err := fs.Sub(web.Dist, "dist"); err == nil {
		if f, err := sub.Open("index.html"); err == nil {
			f.Close()
			webFS = sub
		}
	}
	if webFS == nil {
		log.Warn("web UI not embedded; API only (run `make web` before building)")
	}

	srv := server.New(cfg, st, log, webFS)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := srv.Bootstrap(ctx); err != nil {
		log.Error("bootstrap", "err", err)
		os.Exit(1)
	}

	handler := srv.Router()
	var servers []*http.Server
	for _, addr := range cfg.Addrs() {
		hs := &http.Server{
			Addr:              addr,
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
		}
		servers = append(servers, hs)
		go listen(ctx, log, hs)
	}
	log.Info("pocket-ai started", "version", version, "addrs", cfg.Addrs(), "base_url", cfg.BaseURL, "db", cfg.DBPath,
		"auth_mode", cfg.AuthMode, "roots", cfg.Roots)
	<-ctx.Done()
	log.Info("shutting down")
	shutdownCtx, c2 := context.WithTimeout(context.Background(), 15*time.Second)
	defer c2()
	for _, hs := range servers {
		_ = hs.Shutdown(shutdownCtx)
	}
}

// listen serves one address; an address that is not there yet (the docker
// bridge before docker started) is retried instead of failing the service.
func listen(ctx context.Context, log *slog.Logger, hs *http.Server) {
	wait := 2 * time.Second
	for {
		err := hs.ListenAndServe()
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return
		}
		log.Warn("listen failed, retrying", "addr", hs.Addr, "err", err, "in", wait.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if wait < time.Minute {
			wait *= 2
		}
	}
}

func runBackup(args []string) {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	out := fs.String("o", "", "output file (default: DATA_DIR/backups/pocket-ai-<timestamp>.db)")
	_ = fs.Parse(args)
	cfg, log, st := mustLoad()
	defer st.Close()
	dst := *out
	if dst == "" {
		_ = os.MkdirAll(cfg.DataDir+"/backups", 0o755)
		dst = fmt.Sprintf("%s/backups/pocket-ai-%s.db", cfg.DataDir, time.Now().Format("20060102-150405"))
	}
	if err := st.Backup(context.Background(), dst); err != nil {
		log.Error("backup failed", "err", err)
		os.Exit(1)
	}
	fmt.Println(dst)
}

func runResetPassword(args []string) {
	fs := flag.NewFlagSet("reset-password", flag.ExitOnError)
	email := fs.String("email", "", "user email (default: the first user)")
	password := fs.String("password", "", "new password (default: generate a random one)")
	_ = fs.Parse(args)
	_, log, st := mustLoad()
	defer st.Close()
	ctx := context.Background()
	var u *store.User
	var err error
	if *email != "" {
		u, err = st.GetUserByEmail(ctx, *email)
	} else {
		var users []store.User
		users, err = st.ListUsers(ctx)
		if err == nil && len(users) > 0 {
			u = &users[0]
		} else if err == nil {
			err = errors.New("no users found")
		}
	}
	if err != nil {
		log.Error("reset-password: user lookup failed", "err", err)
		os.Exit(1)
	}
	pw := *password
	if pw == "" {
		const alphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			log.Error("reset-password: random", "err", err)
			os.Exit(1)
		}
		for i := range b {
			b[i] = alphabet[int(b[i])%len(alphabet)]
		}
		pw = string(b)
	}
	if len(pw) < 8 {
		log.Error("reset-password: password must be at least 8 characters")
		os.Exit(1)
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		log.Error("reset-password: hash", "err", err)
		os.Exit(1)
	}
	u.PasswordHash = string(h)
	if err := st.UpdateUser(ctx, u); err != nil {
		log.Error("reset-password: update", "err", err)
		os.Exit(1)
	}
	fmt.Printf("password for %s set", u.Email)
	if *password == "" {
		fmt.Printf(": %s", pw)
	}
	fmt.Println()
}
