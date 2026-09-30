package cli

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/spf13/cobra"
	"github.com/thomasmarcelin754/boursocli/internal/htmlx"
	"github.com/thomasmarcelin754/boursocli/internal/out"
)

func newOrdMouvementsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "ord-mouvements",
		Short: "Historique des mouvements comptabilisés ORD/PEA (plan cookie, table legacy)",
	}
	sel := addAccountFlag(c)
	var periods []string
	c.Flags().StringSliceVar(&periods, "period", nil, "mois M-AAAA, répétable ou séparés par des virgules (ex : 8-2026,9-2026 ; défaut : mois courant ; voir availablePeriods)")
	c.RunE = func(cmd *cobra.Command, _ []string) error {
		for _, p := range periods {
			if !rePeriod.MatchString(p) {
				return out.Fail(fmt.Errorf("--period %q invalide : M-AAAA attendu (ex : 8-2026)", p))
			}
		}
		ctx := cmd.Context()
		cl, a, err := resolvePicked(ctx, *sel)
		if err != nil {
			return out.Fail(err)
		}
		if err := requireKind("ord-mouvements", a, "pea", "ord"); err != nil {
			return out.Fail(err)
		}
		path := fmt.Sprintf("/compte/%s/%s/mouvements", a.urlKind(), a.AccountKey)
		doc, err := getPage(ctx, cl, path, "table.trading-operations__table")
		if err != nil {
			return out.Fail(err)
		}
		// The month filter is a GET form with a CSRF token (checked live
		// 2026-09-30: without the token the period is ignored).
		available := doc.Sel("select[name='form[period]'] option").Map(func(_ int, o *goquery.Selection) string {
			v, _ := o.Attr("value")
			return v
		})
		token, _ := doc.Sel("input[name='form[_token]']").Attr("value")
		type byPeriod struct {
			Period     string `json:"period"`
			Mouvements []mvt  `json:"mouvements"`
		}
		var result []byPeriod
		if len(periods) == 0 {
			m, err := parseMouvements(doc)
			if err != nil {
				return out.Fail(err)
			}
			result = append(result, byPeriod{Period: "", Mouvements: m})
		}
		for _, p := range periods {
			if token == "" {
				return out.Fail(fmt.Errorf("ord-mouvements : jeton du filtre introuvable (dérive de schéma)"))
			}
			q := url.Values{}
			q.Set("form[period]", p)
			q.Set("form[type]", "")
			q.Set("form[_token]", token)
			d, err := getPage(ctx, cl, path+"?"+q.Encode(), "table.trading-operations__table")
			if err != nil {
				return out.Fail(err)
			}
			m, err := parseMouvements(d)
			if err != nil {
				return out.Fail(err)
			}
			result = append(result, byPeriod{Period: p, Mouvements: m})
		}
		var all []mvt
		for _, r := range result {
			all = append(all, r.Mouvements...)
		}
		payload := map[string]any{"accountKey": a.AccountKey, "mouvements": all, "availablePeriods": available}
		if len(periods) > 0 {
			payload["byPeriod"] = result
		}
		if out.Format != "table" {
			return out.Data(payload)
		}
		t := out.Table{Cols: []string{"dateOp", "dateVal", "operation", "name", "isin", "montant", "qty", "cours"}}
		for _, m := range all {
			t.Rows = append(t.Rows, []string{m.DateOp, m.DateVal, m.Operation, m.Name, m.ISIN, m.Montant.Raw, m.Quantite, m.Cours.Raw})
		}
		return out.Data(t)
	}
	return c
}

var rePeriod = regexp.MustCompile(`^(1[0-2]|[1-9])-20[0-9]{2}$`)

type mvt struct {
	DateOp    string `json:"dateOp"`
	DateVal   string `json:"dateVal"`
	Operation string `json:"operation"`
	Name      string `json:"name"`
	ISIN      string `json:"isin"`
	Montant   num    `json:"montant"`
	Quantite  string `json:"quantite"`
	Cours     num    `json:"cours"`
}

// parseMouvements reads the 8-column movements table (an empty state = no
// movement that month).
func parseMouvements(doc *htmlx.Doc) ([]mvt, error) {
	if doc.Sel("table.trading-operations__table").Length() == 0 {
		return []mvt{}, nil // getPage saw an explicit empty state
	}
	tbl, err := doc.ExtractOneTable("table.trading-operations__table")
	if err != nil {
		return nil, err
	}
	if len(tbl.Headers) != 8 {
		return nil, fmt.Errorf("ord-mouvements : 8 colonnes attendues, %d obtenues : %v", len(tbl.Headers), tbl.Headers)
	}
	mvts := []mvt{}
	for i, row := range tbl.Rows {
		if len(row) != 8 {
			return nil, fmt.Errorf("ord-mouvements ligne %d : 8 cellules attendues, %d obtenues (dérive de schéma)", i, len(row))
		}
		name, isin := mouvNameISIN(row[3])
		mvts = append(mvts, mvt{
			DateOp:    htmlx.Clean(row[0].Text()),
			DateVal:   htmlx.Clean(row[1].Text()),
			Operation: htmlx.Clean(row[2].Text()),
			Name:      name,
			ISIN:      isin,
			Montant:   mknum(row[5].Text()),
			Quantite:  htmlx.Clean(row[6].Text()),
			Cours:     mknum(row[7].Text()),
		})
	}
	return mvts, nil
}

func mouvNameISIN(cell *goquery.Selection) (string, string) {
	raw := htmlx.Clean(cell.Text())
	parts := strings.Fields(raw)
	for i := len(parts) - 1; i >= 0; i-- {
		if isISIN(parts[i]) {
			return strings.TrimSpace(strings.Join(parts[:i], " ")), parts[i]
		}
	}
	return raw, ""
}

func isISIN(s string) bool {
	if len(s) != 12 {
		return false
	}
	for i, c := range s {
		if i < 2 {
			if c < 'A' || c > 'Z' {
				return false
			}
		} else {
			if (c < '0' || c > '9') && (c < 'A' || c > 'Z') {
				return false
			}
		}
	}
	return true
}
