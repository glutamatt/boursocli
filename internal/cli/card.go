package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/thomasmarcelin754/boursocli/internal/out"
)

func newCardCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "card",
		Short: "Détails de la carte bancaire (Bearer bank/creditcard/parameterssummary)",
	}
	sel := addAccountFlag(c)
	c.RunE = func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		cl, accs, raw, err := resolveAccounts(ctx)
		if err != nil {
			return out.Fail(err)
		}
		a, err := pickAccount(accs, *sel)
		if err != nil {
			return out.Fail(err)
		}
		if err := requireKind("card", a, "card"); err != nil {
			return out.Fail(err)
		}
		// The card itself comes with the account list (details.creditCard).
		// bank/creditcard/parameterssummary/<key> answered 404 "Requête
		// invalide" on a live account (2026-09-30) for every key tried; its
		// extra parameters are added only when it answers.
		cc, err := cardDetails(raw, a.AccountKey)
		if err != nil {
			return out.Fail(err)
		}
		payload := map[string]json.RawMessage{"creditCard": cc}
		if b, st, err := cl.API(ctx, "bank/creditcard/parameterssummary/"+a.AccountKey); err == nil && st == 200 && json.Valid(b) {
			payload["parameters"] = b
		} else {
			payload["parametersUnavailable"] = json.RawMessage(`true`)
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return out.Fail(err)
		}
		if out.Format != "table" {
			return out.Raw(body)
		}
		var p struct {
			CreditCard struct {
				Name           string `json:"name"`
				Label          string `json:"label"`
				Number         string `json:"number"`
				Holder         string `json:"holder"`
				ExpirationDate string `json:"expirationDate"`
				Situation      string `json:"situation"`
				IsActive       bool   `json:"isActive"`
				IsLocked       bool   `json:"isLocked"`
				HasNfc         bool   `json:"hasNfc"`
				IsPrime        bool   `json:"isPrime"`
				IsMetal        bool   `json:"isMetal"`
				Virtual        bool   `json:"virtual"`
			} `json:"creditCard"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return out.Fail(fmt.Errorf("décodage carte : %w", err))
		}
		card := p.CreditCard
		t := out.Table{Cols: []string{"name", "number", "holder", "expiration", "situation", "active", "locked", "nfc", "prime"}}
		t.Rows = append(t.Rows, []string{
			firstNonEmpty(card.Label, card.Name), card.Number, card.Holder,
			card.ExpirationDate, card.Situation,
			fmt.Sprint(card.IsActive), fmt.Sprint(card.IsLocked),
			fmt.Sprint(card.HasNfc), fmt.Sprint(card.IsPrime),
		})
		return out.Data(t)
	}
	return c
}

// cardDetails returns details.creditCard of the account with this key, from
// the raw bank/account/accounts body.
func cardDetails(accountsBody []byte, key string) (json.RawMessage, error) {
	var list []struct {
		AccountKey string `json:"accountKey"`
		Details    struct {
			CreditCard json.RawMessage `json:"creditCard"`
		} `json:"details"`
	}
	if err := json.Unmarshal(accountsBody, &list); err != nil {
		return nil, fmt.Errorf("décodage bank/account/accounts : %w", err)
	}
	for _, x := range list {
		if x.AccountKey == key {
			if len(x.Details.CreditCard) == 0 || string(x.Details.CreditCard) == "null" {
				return nil, fmt.Errorf("card : pas de details.creditCard pour le compte %s", key)
			}
			return x.Details.CreditCard, nil
		}
	}
	return nil, fmt.Errorf("card : compte %s absent de bank/account/accounts", key)
}
