package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/thomasmarcelin754/boursocli/internal/client"
	"github.com/thomasmarcelin754/boursocli/internal/out"
)

// acct holds ONLY the fields needed for account selection + the table view.
// The exhaustive payload is emitted verbatim via out.Raw (lossless), so this
// struct is deliberately minimal and uses only fields whose live type is
// stable (verified against the real account: the full
// object has 47 fields, 7 nullable, and `visibility` is a bool not a string —
// an earlier field list was partly inaccurate; verified, not
// invented). `accountKey` is the 32-hex key (== customId == pfmAccountKey for
// CAV/ORD) used by BOTH the Bearer and cookie planes; `id` (24-hex) is NOT it.
type acct struct {
	AccountKey      string  `json:"accountKey"`
	CustomID        string  `json:"customId"`
	PfmAccountKey   string  `json:"pfmAccountKey"`
	AccountNumber   string  `json:"accountNumber"`
	IBAN            string  `json:"iban"`
	Balance         float64 `json:"balance"`
	Currency        string  `json:"currency"`
	Name            string  `json:"name"`
	Type            string  `json:"type"`         // COMPTE | EPARGNE | CCREDIT | ORD | PEA | FEDEAV | CAR_INSURANCE…
	TypeCategory    string  `json:"typeCategory"` // BANK | SAVINGS | CREDITCARD | TRADING | INSURANCE
	BankAccountType string  `json:"bankAccountType"`
	// LIVRET_A, LDD, LEP, CSL, PEA_PEA_PME, ASSURANCE_VIE… (null for CAV/card)
	BankAccountTypeExtended string `json:"bankAccountTypeExtended"`
}

// urlKind classifies an account; for cav/ord/pea it is also the
// cookie-plane URL segment (/compte/<kind>/<key>/). Discriminators are
// `type` / `typeCategory`, checked on a live account (2026-09-30):
//
//	COMPTE  / BANK       → cav      EPARGNE / SAVINGS   → livret
//	PEA     / TRADING    → pea      ORD     / TRADING   → ord
//	CCREDIT / CREDITCARD → card     FEDEAV  / INSURANCE → av (assurance-vie)
//	other   / INSURANCE  → assurance (car, home… — no data to read)
//
// bankAccountType is NOT a discriminator: it is PLACEMENT_BANCAIRE for
// livrets and PLACEMENT_FINANCIER for both PEA and assurance-vie.
func (a acct) urlKind() string {
	t := strings.ToUpper(a.Type)
	tc := strings.ToUpper(a.TypeCategory)
	ext := strings.ToUpper(a.BankAccountTypeExtended)
	switch {
	case t == "PEA" || (tc == "TRADING" && strings.HasPrefix(ext, "PEA")):
		return "pea"
	case t == "ORD" || tc == "TRADING":
		return "ord"
	case t == "EPARGNE" || tc == "SAVINGS":
		return "livret"
	case tc == "INSURANCE" && (t == "FEDEAV" || ext == "ASSURANCE_VIE"):
		return "av"
	case tc == "INSURANCE":
		return "assurance"
	case t == "CCREDIT" || tc == "CREDITCARD":
		return "card"
	case t == "COMPTE" || tc == "BANK":
		return "cav"
	default:
		return ""
	}
}

// requireKind fails with a pointer to the right command when a command is
// used on an account kind it cannot read.
func requireKind(cmd string, a acct, kinds ...string) error {
	k := a.urlKind()
	for _, want := range kinds {
		if k == want {
			return nil
		}
	}
	hint := ""
	switch k {
	case "pea", "ord":
		hint = " — pour ce compte titres : positions, ord-mouvements, ord-orders, ord-fiscalite, documents"
	case "livret", "cav":
		hint = " — pour ce compte : operations, budget-movements, transfers, documents"
	case "av":
		hint = " — l’assurance-vie n’est lisible qu’à travers `accounts` (solde) pour l’instant"
	}
	return fmt.Errorf("%s : le compte %s (%s) est de type %q, attendu %s%s", cmd, a.AccountKey, a.Name, k, strings.Join(kinds, " ou "), hint)
}

// resolveAccounts opens a session and returns the live account list (Bearer
// bank/account/accounts) AND the raw body (for lossless JSON output). No
// silent failure: non-200 or empty list is fatal.
func resolveAccounts(ctx context.Context) (*client.Client, []acct, []byte, error) {
	cl, _, _, err := session(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	body, handled, err := getJSON(ctx, cl, "bank/account/accounts")
	if err != nil {
		return nil, nil, nil, err
	}
	if handled {
		return nil, nil, nil, fmt.Errorf("bank/account/accounts : réponse inattendue (non-200 traitée)")
	}
	var accs []acct
	if err := json.Unmarshal(body, &accs); err != nil {
		return nil, nil, nil, fmt.Errorf("décodage bank/account/accounts : %w (corps : %s)", err, snippet(body))
	}
	if len(accs) == 0 {
		return nil, nil, nil, fmt.Errorf("bank/account/accounts a renvoyé une liste vide (≥1 compte attendu) — probablement un souci de session/schéma, pas un état réel")
	}
	return cl, accs, body, nil
}

// pickAccount selects one account by selector: an exact accountKey, or a kind
// (cav|livret|pea|ord|card|av). Ambiguous or no match ⇒ loud error listing the choices
// (never a silent default).
func pickAccount(accs []acct, selector string) (acct, error) {
	if selector == "" {
		return acct{}, fmt.Errorf("--account manquant : fournir un accountKey ou un type (cav|livret|pea|ord|card|av). %s", choices(accs))
	}
	var byKey, byKind []acct
	for _, a := range accs {
		if a.AccountKey == selector {
			byKey = append(byKey, a)
		}
		if a.urlKind() == strings.ToLower(selector) {
			byKind = append(byKind, a)
		}
	}
	if len(byKey) == 1 {
		return byKey[0], nil
	}
	switch len(byKind) {
	case 1:
		return byKind[0], nil
	case 0:
		return acct{}, fmt.Errorf("aucun compte ne correspond à %q. %s", selector, choices(accs))
	default:
		return acct{}, fmt.Errorf("%q est ambigu (%d comptes) — préciser un accountKey explicite. %s", selector, len(byKind), choices(accs))
	}
}

func choices(accs []acct) string {
	lines := make([]string, 0, len(accs))
	for _, a := range accs {
		lines = append(lines, fmt.Sprintf("%s=%s (%s)", a.urlKind(), a.AccountKey, a.Name))
	}
	sort.Strings(lines)
	return "Disponibles : " + strings.Join(lines, " · ")
}

// acctsTable renders []acct for the accounts command (table view only).
func acctsTable(accs []acct) out.Table {
	t := out.Table{Cols: []string{"kind", "name", "balance", "currency", "iban", "accountKey"}}
	for _, a := range accs {
		t.Rows = append(t.Rows, []string{
			a.urlKind(), a.Name,
			fmt.Sprintf("%.2f", a.Balance), a.Currency, a.IBAN, a.AccountKey,
		})
	}
	return t
}
