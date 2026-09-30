package cli

import (
	"fmt"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/spf13/cobra"
	"github.com/thomasmarcelin754/boursocli/internal/htmlx"
	"github.com/thomasmarcelin754/boursocli/internal/out"
)

// num keeps the raw cell text AND its parsed value so nothing is lost and a
// parse miss is visible in the payload (never silently zero).
type num struct {
	Raw    string   `json:"raw"`
	Value  *float64 `json:"value"`
	Parsed bool     `json:"parsed"`
}

func mknum(s string) num {
	n := num{Raw: strings.TrimSpace(s)}
	if v, ok := htmlx.FRNumber(s); ok {
		n.Value, n.Parsed = &v, true
	}
	return n
}

// position is one line of the positions table. Columns are read BY HEADER,
// not by position: the PEA table has no "Dernier Mvt" column (9 columns,
// checked live 2026-09-30) while the ORD table had one (10). A missing
// required header is a loud schema-drift error.
type position struct {
	Name        string `json:"name"`
	ISIN        string `json:"isin"`
	Symbol      string `json:"symbol"` // may be empty (/cours/ with no code)
	Quantity    num    `json:"quantity"`
	PRU         num    `json:"pxRevient"`
	LastPrice   num    `json:"cours"`
	DayVarPct   num    `json:"coursDayVarPct"`
	Amount      num    `json:"montant"`
	UnrealPL    num    `json:"plLatentes"`
	UnrealPLPct num    `json:"plLatentesPct"`
	DernierMvt  string `json:"dernierMvt,omitempty"` // ORD table only
}

func newPositionsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "positions",
		Short: "Positions du portefeuille ORD (HTML plan cookie, bi-domaine)",
	}
	sel := addAccountFlag(c)
	c.RunE = func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		cl, a, err := resolvePicked(ctx, *sel)
		if err != nil {
			return out.Fail(err)
		}
		if err := requireKind("positions", a, "pea", "ord"); err != nil {
			return out.Fail(err)
		}
		kind := a.urlKind()
		doc, err := getPage(ctx, cl, "/compte/"+kind+"/"+a.AccountKey+"/positions", "table.c-table.c-table--action")
		if err != nil {
			return out.Fail(err)
		}
		tbl, err := doc.ExtractOneTable("table.c-table.c-table--action")
		if err != nil {
			return out.Fail(err)
		}
		var ps []position
		for i, row := range tbl.Rows {
			cell := func(h string) (*goquery.Selection, error) {
				c, err := tbl.Cell(row, h)
				if err != nil {
					return nil, fmt.Errorf("positions ligne %d : %w", i, err)
				}
				return c, nil
			}
			var cells [7]*goquery.Selection
			for k, h := range []string{"Valeur", "Quantité", "Px. Revient", "Cours", "Montant", "+/- Latentes", "+/- %"} {
				c, err := cell(h)
				if err != nil {
					return out.Fail(err)
				}
				cells[k] = c
			}
			valeur, qty, pru, coursCell, montant, pl, plPct := cells[0], cells[1], cells[2], cells[3], cells[4], cells[5], cells[6]
			dernier := ""
			if c, err := tbl.Cell(row, "Dernier Mvt"); err == nil {
				dernier = htmlx.Clean(c.Text())
			}
			lastPrice := htmlx.Clean(coursCell.Find("span.u-ellipsis").First().Text())
			// Day change: u-color-positive / u-color-negative (live), and
			// u-color-big-stone for an unchanged price.
			dayVar := htmlx.Clean(coursCell.Find("span.u-color-positive, span.u-color-negative, span.u-color-big-stone").First().Text())
			p := position{
				Name:        htmlx.Clean(valeur.Find("span.c-link__label").First().Text()),
				ISIN:        htmlx.Clean(valeur.Find("span.c-table__mention").First().Text()),
				Symbol:      symbolFromCours(valeur),
				Quantity:    mknum(qty.Find("span.u-ellipsis").First().Text()),
				PRU:         mknum(pru.Text()),
				LastPrice:   mknum(lastPrice),
				DayVarPct:   mknum(dayVar),
				Amount:      mknum(montant.Text()),
				UnrealPL:    mknum(pl.Text()),
				UnrealPLPct: mknum(plPct.Text()),
				DernierMvt:  dernier,
			}
			if p.Name == "" && p.ISIN == "" {
				return out.Fail(fmt.Errorf("positions ligne %d : nom ET ISIN vides (dérive de schéma, cellule Valeur)", i))
			}
			ps = append(ps, p)
		}
		// Aggregate + cash live OUTSIDE the table (separate summary block).
		summary := doc.SummaryByLabel(".c-summary-account-wrapper__item", ".c-databox__name")
		payload := map[string]any{
			"accountKey": a.AccountKey,
			"positions":  ps,
			"summary":    summary, // label→value, verbatim (incl. dated cash line)
		}
		if out.Format != "table" {
			return out.Data(payload)
		}
		t := out.Table{Cols: []string{"name", "isin", "qty", "pru", "cours", "montant", "+/-lat", "+/-%"}}
		for _, p := range ps {
			t.Rows = append(t.Rows, []string{
				p.Name, p.ISIN, p.Quantity.Raw, p.PRU.Raw, p.LastPrice.Raw,
				p.Amount.Raw, p.UnrealPL.Raw, p.UnrealPLPct.Raw,
			})
		}
		return out.Data(t)
	}
	return c
}

// symbolFromCours pulls the Boursorama code from a /cours/<sym>/ href in the
// Valeur cell; empty is valid (some lines have a bare /cours/).
func symbolFromCours(cell *goquery.Selection) string {
	var sym string
	cell.Find("a[href]").EachWithBreak(func(_ int, a *goquery.Selection) bool {
		h, _ := a.Attr("href")
		if i := strings.Index(h, "/cours/"); i >= 0 {
			rest := strings.Trim(h[i+len("/cours/"):], "/")
			if rest != "" {
				sym = rest
				return false
			}
		}
		return true
	})
	return sym
}
