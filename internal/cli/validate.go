package cli

import (
	"fmt"
	"regexp"
	"time"
)

// Everything the caller (often an agent) or the bank puts into a URL path is
// checked here first, so that `quote --symbol …` can only ever GET a quote.
// The client's egress guard is the second line (dot segments, encoded bytes).
var (
	// Boursorama symbols and index codes: 1rPENGI, 1rPCAC, FR0013412020,
	// $INDU / $COMPX (US indices)…
	reSymbol = regexp.MustCompile(`^\$?[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)
	// Opaque ids from the bank (accountKey, pfmAccountKey, budget id).
	reID   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	reYear = regexp.MustCompile(`^(19|20)[0-9]{2}$`)
)

func validSymbol(flag, v string) error {
	if !reSymbol.MatchString(v) {
		return fmt.Errorf("%s %q invalide : lettres, chiffres, « . », « _ », « - » uniquement, « $ » en tête permis (ex : 1rPENGI, $INDU)", flag, v)
	}
	return nil
}

func validID(what, v string) error {
	if !reID.MatchString(v) {
		return fmt.Errorf("%s %q invalide : identifiant attendu (lettres, chiffres, « _ », « - »)", what, v)
	}
	return nil
}

func validYear(flag, v string) error {
	if !reYear.MatchString(v) {
		return fmt.Errorf("%s %q invalide : année sur 4 chiffres attendue (ex : 2025)", flag, v)
	}
	return nil
}

func validDate(flag, v string) error {
	if _, err := time.Parse("02/01/2006", v); err != nil {
		return fmt.Errorf("%s %q invalide : date jj/mm/AAAA attendue", flag, v)
	}
	return nil
}

// validDateFlags checks --from / --to when they are given (the defaults are
// computed later and always valid).
func validDateFlags(from, to string) error {
	if from != "" {
		if err := validDate("--from", from); err != nil {
			return err
		}
	}
	if to != "" {
		return validDate("--to", to)
	}
	return nil
}
