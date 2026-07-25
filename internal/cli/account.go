package cli

import (
	"fmt"
	"strings"

	"github.com/n24q02m/better-drive/internal/config"
	"github.com/n24q02m/better-drive/internal/engine"
	"github.com/n24q02m/better-drive/internal/output"
	"github.com/n24q02m/better-drive/internal/paths"
	"github.com/spf13/cobra"
)

// accountEngine is the slice of engine.Engine the account commands use. It is
// an interface (rather than the concrete type) so the tests can drive the
// commands with a double instead of a real rclone binary and a real Drive
// account - the same reasoning runSyncOnce documents for taking a
// syncloop.Syncer.
type accountEngine interface {
	ListDriveRemotes() ([]string, error)
	RemoteConfigured(name string) (bool, error)
	About(name string) (engine.Quota, error)
	DeleteRemote(name string) error
}

// loadConfigBestEffort reads config.toml but reports every failure as "there
// is no config yet" instead of as an error. The account commands run
// legitimately before any [[pair]] exists - an account has to be added before
// it can be paired with a folder - so a missing or unparseable config must not
// stop them. loadConfig stays the right helper for run/status/sync, which have
// nothing to do without pairs.
func loadConfigBestEffort() *config.Config {
	cfg, err := config.Load(paths.ConfigFile())
	if err != nil {
		return nil
	}
	return cfg
}

// rcloneConfigPathOf reads cfg's explicit rclone config path, tolerating the
// nil cfg loadConfigBestEffort returns by falling back to "" - which
// engine.New passes through as "let rclone discover its own config".
func rcloneConfigPathOf(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.RcloneConfig
}

// pairsUsingRemote returns the local path of every configured pair that syncs
// against remoteName. It splits each pair's Remote on the first ":" (the same
// split runCmd uses to check a pair's remote is set up) rather than matching a
// prefix, because "gdrive-work:" starts with "gdrive" as a string while being
// an entirely different account - and this answer decides whether `account
// remove` is allowed to delete one.
func pairsUsingRemote(cfg *config.Config, remoteName string) []string {
	if cfg == nil {
		return nil
	}
	var locals []string
	for _, p := range cfg.Pairs {
		name, _, _ := strings.Cut(p.Remote, ":")
		if name == remoteName {
			locals = append(locals, p.Local)
		}
	}
	return locals
}

// humanBytes renders a byte count in binary units with one decimal place.
// Binary units (and not the decimal GB Google's own storage page shows) are
// what rclone measures in, so this reports the same number the sync engine
// works with rather than a converted one that would not match.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	// exp stops at 3 (TiB): rclone reports a Drive quota in the terabytes at
	// most, and inventing a PiB rung would only add an untested branch.
	for v := n / unit; v >= unit && exp < 3; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}

func accountCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "account",
		Short: "Manage the Google Drive accounts (rclone remotes) better-drive syncs with",
		Long: "Manage the Google Drive accounts better-drive syncs with. Each account is\n" +
			"an rclone remote; a [[pair]] in config.toml points its `remote` at one of\n" +
			"them by name, so several accounts can be used side by side. `account add`\n" +
			"is the same command as `better-drive setup`.",
		Example: "  better-drive account list\n" +
			"  better-drive account list --quota\n" +
			"  better-drive account add --remote gdrive-work\n" +
			"  better-drive account remove gdrive-work",
	}
	c.AddCommand(accountListCmd())
	return c
}

func accountListCmd() *cobra.Command {
	var format string
	var withQuota bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List the Google Drive accounts and the pairs using them",
		Long: "List every Google Drive remote in the rclone config: its name, whether it\n" +
			"has a usable token, and which pairs from config.toml sync against it.\n" +
			"Read-only and offline by default - it never touches the network. Pass\n" +
			"--quota to additionally ask Drive for each configured account's storage\n" +
			"usage, which does make one network call per account.",
		Example: "  better-drive account list\n" +
			"  better-drive account list --quota\n" +
			"  better-drive account list --format json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := loadConfigBestEffort()
			e := engine.New(config.ResolveRcloneConfig(rcloneConfigPathOf(cfg)))
			defer e.Close()
			return runAccountList(cmd, e, cfg, format, withQuota)
		},
	}
	output.AddFormatFlag(c, &format)
	c.Flags().BoolVar(&withQuota, "quota", false, "also report each account's Drive storage usage (one network call per account)")
	return c
}

// runAccountList is the list command's body, taking its engine and config as
// parameters so it can be tested without an rclone binary or a config.toml on
// disk. A nil cfg means no config was loadable, which pairsUsingRemote reads
// as "no pair uses this account".
func runAccountList(cmd *cobra.Command, e accountEngine, cfg *config.Config, format string, withQuota bool) error {
	if err := output.Validate(format); err != nil {
		return badFormatErr(err)
	}
	names, err := e.ListDriveRemotes()
	if err != nil {
		return err
	}
	accounts := make([]output.AccountStatus, 0, len(names))
	for _, name := range names {
		configured, _ := e.RemoteConfigured(name)
		account := output.AccountStatus{
			Name:       name,
			Configured: configured,
			// An account with no pairs renders as [] rather than null: a
			// caller decoding this should be able to range over the field
			// without first testing it for absence.
			Pairs: append([]string{}, pairsUsingRemote(cfg, name)...),
		}
		// A token-less remote is never asked for its quota: `rclone about`
		// can only fail there, spending a round trip to produce a warning
		// that the "not set up" state already explains.
		if withQuota && configured {
			quota, err := e.About(name)
			if err != nil {
				// list is the command a user reaches for when something is
				// already wrong, so an unreadable quota degrades to a
				// diagnostic on stderr and leaves the field empty - it must
				// not take down the rest of the report.
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not read quota for %q: %v\n", name, err)
			} else {
				account.Quota = &output.Quota{Total: quota.Total, Used: quota.Used, Free: quota.Free}
			}
		}
		accounts = append(accounts, account)
	}

	if format == output.FormatJSON {
		return output.RenderJSON(cmd.OutOrStdout(), accounts)
	}
	if len(accounts) == 0 {
		// Nothing to report is not a failure - it is the first-run state - so
		// this exits 0 with the next step on stderr, leaving stdout empty for
		// whatever is consuming it.
		fmt.Fprintln(cmd.ErrOrStderr(), "no Google Drive account configured; run: better-drive account add")
		return nil
	}
	for _, account := range accounts {
		state := "not set up"
		if account.Configured {
			state = "ready"
		}
		line := fmt.Sprintf("account %s: %s, %d pair(s)", account.Name, state, len(account.Pairs))
		if account.Quota != nil {
			line += fmt.Sprintf(", %s of %s used", humanBytes(account.Quota.Used), humanBytes(account.Quota.Total))
		}
		fmt.Fprintln(cmd.OutOrStdout(), line)
	}
	return nil
}
