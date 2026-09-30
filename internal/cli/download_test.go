package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidDocumentURL(t *testing.T) {
	ok := []string{
		"https://api.boursobank.com/services/api/files/transaction-notice-document.phtml?type=deflap&id=X&type=AO",
		"https://api.boursobank.com/services/api/files/statements-document.phtml?type=ccs&id=X",
		"https://api.boursobank.com/services/api/files/customer-document.phtml?resourceId=X",
		"https://api.boursobank.com/services/api/files/lifeinsurance-document.phtml?docType=a&contractId=b",
		"https://clients.boursobank.com/documents/document/telechargement?contactId=1&documentId=2",
		"https://clients.boursobank.com/documents/rib/0123456789abcdef0123456789abcdef/telecharger",
	}
	bad := []string{
		"",
		"http://api.boursobank.com/services/api/files/statements-document.phtml?id=X",          // cleartext
		"https://api.boursobank.com/services/api/v1.7/_user_/_H_/bank/account/accounts",        // an API, not a file
		"https://api.boursobank.com/services/api/files/../v1.7/x.phtml",                        // dot segment
		"https://api.boursobank.com/services/api/files/a%2Fb.phtml",                            // encoded slash
		"https://clients.boursobank.com/feature-redirect?featureId=x&params%5BtransferId%5D=1", // a feature, not a file
		"https://clients.boursobank.com/documents/rib/abc/telecharger/../../virement",          // dot segments
		"https://evil.example/services/api/files/statements-document.phtml",                    // other host
		"https://user:pw@api.boursobank.com/services/api/files/statements-document.phtml",      // userinfo
		"https://api.boursobank.com/services/api/files/statements-document.phtml#x",            // fragment
	}
	for _, u := range ok {
		if err := validDocumentURL(u); err != nil {
			t.Errorf("%s refused: %v", u, err)
		}
	}
	for _, u := range bad {
		if err := validDocumentURL(u); err == nil {
			t.Errorf("%s accepted", u)
		}
	}
}

func TestWriteNewFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "avis.pdf")
	if err := writeNewFile(p, []byte("%PDF-1.4")); err != nil {
		t.Fatalf("new file: %v", err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Errorf("perm = %o, want 600", fi.Mode().Perm())
	}
	if err := writeNewFile(p, []byte("other")); err == nil {
		t.Error("existing file overwritten")
	}
	if b, _ := os.ReadFile(p); string(b) != "%PDF-1.4" { //nolint:gosec // G304: test temp file
		t.Errorf("content changed: %q", b)
	}
	target := filepath.Join(dir, "elsewhere")
	link := filepath.Join(dir, "link.pdf")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := writeNewFile(link, []byte("secret")); err == nil {
		t.Error("write through a symlink accepted")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Error("symlink target was created")
	}
}
