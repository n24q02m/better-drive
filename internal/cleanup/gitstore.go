package cleanup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/n24q02m/better-drive/internal/protectedfs"
)

// GitRepo is a real local bare Git repository. Blobs use Git object IDs and
// multi-ref changes use one update-ref transaction, so a failed expectation
// cannot leave a partially advanced authority state.
type GitRepo struct {
	Root    string
	gitPath string
}

func NewGitRepo(root string) (*GitRepo, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("git repo root is required")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return nil, errors.New("git executable is required for cleanup authority storage")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve git repo root: %w", err)
	}
	initialize := false
	info, err := os.Stat(absoluteRoot)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := protectedfs.EnsurePrivateDir(absoluteRoot); err != nil {
			return nil, fmt.Errorf("protect cleanup authority root: %w", err)
		}
		initialize = true
	case err != nil:
		return nil, err
	case !info.IsDir():
		return nil, errors.New("git repo root must be a directory")
	default:
		if err := protectedfs.VerifyPrivateDir(absoluteRoot); err != nil {
			return nil, fmt.Errorf("verify cleanup authority root: %w", err)
		}
		entries, readErr := os.ReadDir(absoluteRoot)
		if readErr != nil {
			return nil, readErr
		}
		initialize = len(entries) == 0
	}

	repo := &GitRepo{Root: absoluteRoot, gitPath: gitPath}
	if initialize {
		if _, err := runGitProcess(gitPath, nil, "init", "--bare", "--quiet", absoluteRoot); err != nil {
			return nil, fmt.Errorf("initialize bare git repository: %w", err)
		}
	}
	isBare, err := repo.runGit(nil, "rev-parse", "--is-bare-repository")
	if err != nil || strings.TrimSpace(string(isBare)) != "true" {
		if err == nil {
			err = errors.New("repository did not report bare=true")
		}
		return nil, fmt.Errorf("cleanup authority root is not a bare git repository: %w", err)
	}
	if err := protectedfs.VerifyPrivateDir(absoluteRoot); err != nil {
		return nil, fmt.Errorf("verify cleanup authority root: %w", err)
	}
	return repo, nil
}

func (r *GitRepo) runGit(input []byte, args ...string) ([]byte, error) {
	if r == nil || strings.TrimSpace(r.Root) == "" || strings.TrimSpace(r.gitPath) == "" {
		return nil, errors.New("git repo is not configured")
	}
	repoArgs := append([]string{"--git-dir", r.Root}, args...)
	return runGitProcess(r.gitPath, input, repoArgs...)
}

func runGitProcess(gitPath string, input []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, gitPath, args...)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Env = gitProcessEnvironment()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, errors.New("git command timed out")
		}
		detail := strings.TrimSpace(stderr.String())
		if len(detail) > 4096 {
			detail = detail[:4096]
		}
		if detail == "" {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s", err, detail)
	}
	return stdout.Bytes(), nil
}

func gitProcessEnvironment() []string {
	environment := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		name, _, found := strings.Cut(entry, "=")
		if found && strings.HasPrefix(strings.ToUpper(name), "GIT_") {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
	)
}

// WriteBlob stores data as a native Git blob and returns its authoritative OID.
func (r *GitRepo) WriteBlob(data []byte) (string, error) {
	output, err := r.runGit(data, "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	oid := strings.TrimSpace(string(output))
	if !gitOIDPattern.MatchString(oid) {
		return "", errors.New("git hash-object returned an invalid OID")
	}
	return oid, nil
}

func (r *GitRepo) ReadBlob(oid string) ([]byte, error) {
	if !gitOIDPattern.MatchString(oid) {
		return nil, errors.New("oid must be an exact Git object ID")
	}
	return r.runGit(nil, "cat-file", "blob", oid)
}

// ReadRef returns the exact OID stored at ref.
func (r *GitRepo) ReadRef(ref string) (string, bool, error) {
	if err := validateSafeRef(ref); err != nil {
		return "", false, err
	}
	output, err := r.runGit(nil, "rev-parse", "--verify", "--quiet", ref)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", false, nil
		}
		return "", false, err
	}
	oid := strings.TrimSpace(string(output))
	if !gitOIDPattern.MatchString(oid) {
		return "", false, errors.New("git ref did not resolve to an exact object ID")
	}
	return oid, true, nil
}

// CreateRef creates ref with oid if absent (create-only). Returns the existing
// OID on conflict.
func (r *GitRepo) CreateRef(ref, oid string) (string, error) {
	if err := r.AtomicUpdateRefs(GitRefUpdate{Ref: ref, NewOID: oid}); err != nil {
		existing, exists, readErr := r.ReadRef(ref)
		if readErr == nil && exists {
			return existing, fmt.Errorf("ref %q already exists with oid %q: %w", ref, existing, err)
		}
		return "", err
	}
	return oid, nil
}

// CAS updates ref from expectedOID to newOID atomically. expectedOID "" means
// create-only.
func (r *GitRepo) CAS(ref, expectedOID, newOID string) error {
	return r.AtomicUpdateRefs(GitRefUpdate{
		Ref:         ref,
		ExpectedOID: expectedOID,
		NewOID:      newOID,
	})
}

type GitRefUpdate struct {
	Ref         string
	ExpectedOID string
	NewOID      string
}

// AtomicUpdateRefs commits every create/CAS in one Git ref transaction. Any
// stale or existing member aborts the complete set.
func (r *GitRepo) AtomicUpdateRefs(updates ...GitRefUpdate) error {
	if len(updates) == 0 {
		return errors.New("git ref transaction requires at least one update")
	}
	seen := make(map[string]struct{}, len(updates))
	var transaction strings.Builder
	transaction.WriteString("start\n")
	for _, update := range updates {
		if err := validateSafeRef(update.Ref); err != nil {
			return err
		}
		if _, duplicate := seen[update.Ref]; duplicate {
			return fmt.Errorf("git ref transaction contains duplicate ref %q", update.Ref)
		}
		seen[update.Ref] = struct{}{}
		if !gitOIDPattern.MatchString(update.NewOID) {
			return fmt.Errorf("new OID for %q must be an exact Git object ID", update.Ref)
		}
		if update.ExpectedOID == "" {
			fmt.Fprintf(&transaction, "create %s %s\n", update.Ref, update.NewOID)
			continue
		}
		if !gitOIDPattern.MatchString(update.ExpectedOID) {
			return fmt.Errorf("expected OID for %q must be empty or an exact Git object ID", update.Ref)
		}
		fmt.Fprintf(&transaction, "update %s %s %s\n", update.Ref, update.NewOID, update.ExpectedOID)
	}
	transaction.WriteString("prepare\ncommit\n")
	if _, err := r.runGit([]byte(transaction.String()), "update-ref", "--stdin", "--no-deref"); err != nil {
		return fmt.Errorf("git ref transaction rejected: %w", err)
	}
	return nil
}

func validateSafeRef(ref string) error {
	if !strings.HasPrefix(ref, "refs/") {
		return fmt.Errorf("ref %q must start with refs/", ref)
	}
	if len(ref) > 255 || strings.Contains(ref, "..") || strings.Contains(ref, "@{") ||
		strings.ContainsAny(ref, " ~^:?*[]\\\x00\r\n\t") || strings.HasSuffix(ref, ".") {
		return fmt.Errorf("ref %q contains characters Git cannot safely transact", ref)
	}
	// ⚡ Bolt: Use strings.Cut to avoid allocating a slice for ref parts
	value := ref
	for {
		part, remainder, found := strings.Cut(value, "/")
		if !found {
			part = value
		}
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return fmt.Errorf("ref %q contains invalid component", ref)
		}
		if !found {
			break
		}
		value = remainder
	}
	return nil
}
