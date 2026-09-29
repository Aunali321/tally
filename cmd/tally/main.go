package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/Aunali321/tally/internal/api"
	"github.com/Aunali321/tally/internal/nuvio"
	"github.com/Aunali321/tally/internal/store"
	"github.com/Aunali321/tally/internal/web"
)

const usage = `usage: tally <command>

  serve                                        run the server
  user add <email>                             create an account (asks for a password)
  user passwd <email>                          reset a password and sign out all devices
  user delete <email>                          delete an account and everything in it
  invite <email>                               print a single-use sign-up link from this account
  import trakt|simkl|imdb <email> <path>       import an export (folder, .zip or file)
  import stremio <email> <stremio-email>       import a Stremio account (asks for the password)
  import mdblist <email>                       import an MDBList account (asks for the API key)
  nuvio link [-profile N] [-backend URL] <email> <nuvio-email>
                                               mirror a Nuvio profile (asks for the Nuvio password)
  nuvio unlink <email>                         stop mirroring Nuvio
  backup <file>                                write a copy of the database, safe while serving

environment:
  TALLY_DB    database file (default tally.db)
  TALLY_ADDR  listen address (default :8080)`

var errUsage = errors.New(usage)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(ctx, log, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "tally:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, log *slog.Logger, args []string) error {
	if len(args) == 0 {
		return errUsage
	}
	st, err := store.Open(ctx, cmp.Or(os.Getenv("TALLY_DB"), "tally.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	switch cmd, rest := args[0], args[1:]; {
	case cmd == "serve" && len(rest) == 0:
		return serve(ctx, st, log, cmp.Or(os.Getenv("TALLY_ADDR"), ":8080"))
	case cmd == "user" && len(rest) == 2 && rest[0] == "add":
		return addUser(ctx, st, rest[1])
	case cmd == "user" && len(rest) == 2 && rest[0] == "passwd":
		return resetPassword(ctx, st, rest[1])
	case cmd == "user" && len(rest) == 2 && rest[0] == "delete":
		return deleteUser(ctx, st, rest[1])
	case cmd == "import" && len(rest) > 0:
		return runImport(ctx, st, rest[0], rest[1:])
	case cmd == "nuvio" && len(rest) > 0 && rest[0] == "link":
		return linkNuvio(ctx, st, rest[1:])
	case cmd == "nuvio" && len(rest) == 2 && rest[0] == "unlink":
		return unlinkNuvio(ctx, st, rest[1])
	case cmd == "invite" && len(rest) == 1:
		return invite(ctx, st, rest[0])
	case cmd == "backup" && len(rest) == 1:
		return st.Backup(ctx, rest[0])
	}
	return errUsage
}

func routes(ctx context.Context, st *store.Store, log *slog.Logger) (http.Handler, error) {
	ui, err := web.New(ctx, st, log)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("/v1/", api.New(st, log))
	mux.Handle("/", ui)
	return mux, nil
}

func localURL(addr, path string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port) + path
}

func serve(ctx context.Context, st *store.Store, log *slog.Logger, addr string) error {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	handler, err := routes(ctx, st, log)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	wg.Go(func() { nuvio.Run(ctx, st, log) })
	invite, err := st.SetupInvite(ctx)
	if err != nil {
		return err
	}
	if invite != "" {
		log.Info("no accounts yet, create the first one", "url", localURL(addr, "/signup?invite="+invite))
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("listening", "addr", addr)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	}
}
