package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	iofs "io/fs"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/Aunali321/tally/internal/importer"
	"github.com/Aunali321/tally/internal/nuvio"
	"github.com/Aunali321/tally/internal/store"
)

func addUser(ctx context.Context, st *store.Store, email string) error {
	email, err := store.NormalizeEmail(email)
	if err != nil {
		return err
	}
	password, err := readPassword("Password for " + email + ": ")
	if err != nil {
		return err
	}
	if err := st.CreateUser(ctx, email, password); err != nil {
		return err
	}
	fmt.Printf("Created the account %s.\n", email)
	return nil
}

func resetPassword(ctx context.Context, st *store.Store, email string) error {
	password, err := readPassword("New password for " + email + ": ")
	if err != nil {
		return err
	}
	if err := st.SetPassword(ctx, email, password); err != nil {
		return err
	}
	fmt.Printf("Password changed. Every device of %s is signed out.\n", email)
	return nil
}

// One reader for all prompts: separate buffered readers would each swallow lines meant for the next prompt.
var stdin = bufio.NewReader(os.Stdin)

var fileImporters = map[string]func(context.Context, *store.Store, int64, iofs.FS, importer.Options) ([]store.ImportSection, error){
	"trakt": importer.Trakt,
	"simkl": importer.Simkl,
	"imdb":  importer.IMDb,
}

func deleteUser(ctx context.Context, st *store.Store, email string) error {
	uid, err := st.UserID(ctx, email)
	if err != nil {
		return err
	}
	if err := st.DeleteUser(ctx, uid); err != nil {
		return err
	}
	fmt.Printf("Deleted the account %s and everything in it.\n", email)
	return nil
}

func runImport(ctx context.Context, st *store.Store, source string, args []string) error {
	if len(args) < 1 {
		return errUsage
	}
	uid, err := st.UserID(ctx, args[0])
	if err != nil {
		return err
	}
	opts := importer.Options{
		Starting: func(section string) { fmt.Printf("%s...\n", section) },
		Finished: func(r store.ImportSection) {
			fmt.Printf("  %d imported, %d without an IMDb or TMDB id, %d skipped\n", r.Imported, r.NoID, r.Skipped)
		},
	}
	_, err = importFrom(ctx, st, uid, source, args[1:], opts)
	return err
}

func importFrom(ctx context.Context, st *store.Store, uid int64, source string, args []string, opts importer.Options) ([]store.ImportSection, error) {
	switch {
	case fileImporters[source] != nil && len(args) == 1:
		files, closeFiles, err := importer.Open(args[0])
		if err != nil {
			return nil, err
		}
		defer closeFiles()
		return fileImporters[source](ctx, st, uid, files, opts)
	case source == "stremio" && len(args) == 1:
		password, err := readPassword("Stremio password for " + args[0] + ": ")
		if err != nil {
			return nil, err
		}
		return importer.Stremio(ctx, st, uid, args[0], password, opts)
	case source == "mdblist" && len(args) == 0:
		key, err := readPassword("MDBList API key: ")
		if err != nil {
			return nil, err
		}
		return importer.MDBList(ctx, st, uid, key, opts)
	}
	return nil, errUsage
}

func linkNuvio(ctx context.Context, st *store.Store, args []string) error {
	fs := flag.NewFlagSet("nuvio link", flag.ContinueOnError)
	profile := fs.Int("profile", 0, "Nuvio profile number, asked for when the account has several")
	backend := fs.String("backend", nuvio.DefaultBackend, "Nuvio backend URL, for self-hosted Nuvio")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return errUsage
	}
	uid, err := st.UserID(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	password, err := readPassword("Nuvio password for " + fs.Arg(1) + ": ")
	if err != nil {
		return err
	}
	profiles, err := nuvio.SignIn(ctx, st, uid, *backend, fs.Arg(1), password)
	if err != nil {
		return err
	}
	if len(profiles) > 1 {
		if *profile == 0 {
			if *profile, err = pickProfile(profiles); err != nil {
				return err
			}
		}
		if _, err := nuvio.ChooseProfile(ctx, st, uid, *profile); err != nil {
			return err
		}
	}
	link, err := st.NuvioLink(ctx, uid)
	if err != nil {
		return err
	}
	fmt.Printf("Mirroring Nuvio profile %s. Pulling history...\n", link.ProfileName)
	res, err := nuvio.Sync(ctx, st, link)
	if err != nil {
		return err
	}
	fmt.Printf("Applied %d events, skipped %d that are not movies or shows. The server keeps syncing every minute.\n", res.Applied, res.Skipped)
	return nil
}

func pickProfile(profiles []nuvio.Profile) (int, error) {
	for _, p := range profiles {
		fmt.Printf("  %d  %s\n", p.Index, p.Name)
	}
	fmt.Print("Profile to mirror: ")
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		return 0, errors.New("a profile number is required")
	}
	return strconv.Atoi(strings.TrimSpace(line))
}

func invite(ctx context.Context, st *store.Store, email string) error {
	uid, err := st.UserID(ctx, email)
	if err != nil {
		return err
	}
	code, err := st.CreateInvite(ctx, uid)
	if err != nil {
		return err
	}
	fmt.Printf("Send this link. It works once and expires in %d days:\n  <your tally address>/signup?invite=%s\n", int(store.InviteTTL.Hours()/24), code)
	return nil
}

func unlinkNuvio(ctx context.Context, st *store.Store, email string) error {
	uid, err := st.UserID(ctx, email)
	if err != nil {
		return err
	}
	if err := st.DeleteNuvioLink(ctx, uid); errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%s is not linked to Nuvio", email)
	} else if err != nil {
		return err
	}
	fmt.Printf("Unlinked %s from Nuvio. The history stays in tally.\n", email)
	return nil
}

func readPassword(prompt string) (string, error) {
	var password string
	if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, prompt)
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		password = string(b)
	} else {
		line, err := stdin.ReadString('\n')
		if err != nil && line == "" {
			return "", errors.New("password required on stdin")
		}
		password = strings.TrimRight(line, "\r\n")
	}
	if password == "" {
		return "", errors.New("password must not be empty")
	}
	return password, nil
}
