package cli

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/thomasmarcelin754/boursocli/internal/out"
)

// newDownloadCmd saves one document (avis d'opéré, relevé, IFU, RIB…) as a
// PDF. The URL is a downloadUrl printed by `docs` or `documents`; only the
// bank's document-download endpoints are accepted, the answer must be a
// PDF, and the file is always a NEW file (never an overwrite).
func newDownloadCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "download",
		Short: "Télécharge un document PDF (downloadUrl de `docs` / `documents`) dans un nouveau fichier",
	}
	var rawURL, outFile string
	c.Flags().StringVar(&rawURL, "url", "", "downloadUrl donné par `docs` ou `documents`")
	c.Flags().StringVar(&outFile, "out", "", "fichier PDF à créer (ne doit pas exister)")
	c.RunE = func(cmd *cobra.Command, _ []string) error {
		if err := validDocumentURL(rawURL); err != nil {
			return out.Fail(err)
		}
		if outFile == "" {
			return out.Fail(fmt.Errorf("--out requis : chemin du nouveau fichier PDF"))
		}
		// Fail before the download; writeNewFile still enforces it.
		if _, err := os.Lstat(outFile); err == nil {
			return out.Fail(fmt.Errorf("--out %s existe déjà : choisir un nouveau nom (le CLI n’écrase jamais un fichier)", outFile))
		}
		ctx := cmd.Context()
		cl, _, _, err := session(ctx)
		if err != nil {
			return out.Fail(err)
		}
		body, status, err := cl.Cookie(ctx, rawURL)
		if err != nil {
			return out.Fail(err)
		}
		if status != 200 {
			return out.Fail(fmt.Errorf("download → HTTP %d : %s", status, snippet(body)))
		}
		if !bytes.HasPrefix(body, []byte("%PDF-")) {
			return out.Fail(fmt.Errorf("download : la réponse n’est pas un PDF (%d octets, début %q) — lien expiré ou page de la banque ; relister avec `docs` / `documents`", len(body), snippet(body)))
		}
		if err := writeNewFile(outFile, body); err != nil {
			return out.Fail(err)
		}
		abs, _ := filepath.Abs(outFile)
		return out.OK("download", map[string]any{"file": abs, "bytes": len(body)})
	}
	return c
}

// Document-download endpoints seen on a live account (2026-09-30):
//
//	https://api.boursobank.com/services/api/files/<name>.phtml?…
//	  (transaction-notice-document, statements-document, customer-document,
//	  lifeinsurance-document)
//	https://clients.boursobank.com/documents/document/telechargement?…
//	https://clients.boursobank.com/documents/rib/<id>/telecharger
//
// Anything else — including feature-redirect links, which lead to account
// features, not files — is refused.
var (
	reAPIFile = regexp.MustCompile(`^/services/api/files/[a-z-]+\.phtml$`)
	reRIB     = regexp.MustCompile(`^/documents/rib/[A-Za-z0-9_-]{1,64}/telecharger$`)
)

func validDocumentURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("--url requis : un downloadUrl donné par `docs` ou `documents`")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("--url invalide : %w", err)
	}
	bad := fmt.Errorf("--url %q refusée : seuls les liens de téléchargement de documents BoursoBank sont acceptés (downloadUrl de `docs` / `documents`)", u.Redacted())
	if u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.RawPath != "" || strings.Contains(u.Path, "%") {
		return bad
	}
	switch strings.ToLower(u.Host) {
	case "api.boursobank.com":
		if reAPIFile.MatchString(u.Path) {
			return nil
		}
	case "clients.boursobank.com":
		if u.Path == "/documents/document/telechargement" || reRIB.MatchString(u.Path) {
			return nil
		}
	}
	return bad
}

// writeNewFile creates path (0600) and refuses to touch anything that is
// already there: O_EXCL fails on an existing file and on any symlink, even a
// dangling one, so a download can neither overwrite a user file nor be
// redirected to a place someone else reads.
func writeNewFile(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the caller's explicit --out; O_EXCL, never overwrites
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("--out %s existe déjà : choisir un nouveau nom (le CLI n’écrase jamais un fichier)", path)
		}
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	return f.Close()
}
