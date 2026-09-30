package cli

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/spf13/cobra"
	"github.com/thomasmarcelin754/boursocli/internal/htmlx"
	"github.com/thomasmarcelin754/boursocli/internal/out"
)

// newBudgetMovementsCmd: cookie-plane PFM/budget movements.
// GET /budget/compte/<webid>/mouvements?movementSearch[fromDate|toDate]=
// dd/mm/YYYY&movementSearch[selectedAccounts][]=<webid>  (webid =
// pfmAccountKey). This page is NOT a table — it is
// <ul.list__movement> of <li.list-operation-item data-id> grouped by
// <li.list-operation-date-line>. Missing container = loud schema drift; an
// item with empty label AND amount = loud (never a silent half-row).
//
// Each page holds 30 movements, newest first. The last <li> carries
// data-operations-next-pagination=<cursor>; the next (older) page is a
// plain GET of the same path with ?continuationToken=<cursor> (seen in
// Chrome, 2026-09-30). The loop stops at the last page, at the first
// movement older than --from, or at --max-pages.
func newBudgetMovementsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "budget-movements",
		Short: "Mouvements PFM/budget d’un compte courant ou livret, paginés jusqu’à --from (plan cookie)",
	}
	sel := addAccountFlag(c)
	var from, to string
	var maxPages int
	c.Flags().StringVar(&from, "from", "", "date de début jj/mm/AAAA (défaut : il y a 3 mois)")
	c.Flags().StringVar(&to, "to", "", "date de fin jj/mm/AAAA (défaut : aujourd’hui+40j)")
	c.Flags().IntVar(&maxPages, "max-pages", 40, "nombre maximal de pages de 30 mouvements")
	c.RunE = func(cmd *cobra.Command, _ []string) error {
		if err := validDateFlags(from, to); err != nil {
			return out.Fail(err)
		}
		if maxPages < 1 {
			return out.Fail(fmt.Errorf("--max-pages %d invalide : ≥ 1 attendu", maxPages))
		}
		ctx := cmd.Context()
		cl, a, err := resolvePicked(ctx, *sel)
		if err != nil {
			return out.Fail(err)
		}
		if err := requireKind("budget-movements", a, "cav", "livret"); err != nil {
			return out.Fail(err)
		}
		webid := a.PfmAccountKey
		if webid == "" {
			return out.Fail(fmt.Errorf("le compte %s n’a pas de pfmAccountKey (webid budget) — non activé PFM", a.AccountKey))
		}
		if err := validID("pfmAccountKey (réponse de la banque)", webid); err != nil {
			return out.Fail(err)
		}
		now := time.Now()
		if from == "" {
			from = now.AddDate(0, -3, 0).Format("02/01/2006")
		}
		if to == "" {
			to = now.AddDate(0, 0, 40).Format("02/01/2006")
		}
		fromDate, _ := time.Parse("02/01/2006", from)
		base := "/budget/compte/" + webid + "/mouvements"
		q := url.Values{}
		q.Set("movementSearch[fromDate]", from)
		q.Set("movementSearch[toDate]", to)
		q.Add("movementSearch[selectedAccounts][]", webid)
		path := base + "?" + q.Encode()

		p := pfmPager{seen: map[string]bool{}}
		stop := "maxPages"
		pages := 0
		for pages < maxPages {
			if pages > 0 {
				select { // human pace between pages
				case <-ctx.Done():
					return out.Fail(ctx.Err())
				case <-time.After(time.Second):
				}
			}
			doc, err := getPage(ctx, cl, path, "ul.list__movement")
			if err != nil {
				return out.Fail(err)
			}
			pages++
			cursor, older, err := p.read(doc, fromDate)
			if err != nil {
				return out.Fail(err)
			}
			if older {
				stop = "from"
				break
			}
			if cursor == "" {
				stop = "end"
				break
			}
			if !validCursor(cursor) {
				return out.Fail(fmt.Errorf("budget-movements : curseur de pagination inattendu (%d caractères) — refusé dans l’URL", len(cursor)))
			}
			path = base + "?continuationToken=" + cursor
		}
		payload := map[string]any{
			"accountKey": a.AccountKey, "webid": webid,
			"from": from, "to": to, "count": len(p.rows), "movements": p.rows,
			"pages": pages, "stoppedBy": stop, // end | from | maxPages
		}
		if out.Format != "table" {
			return out.Data(payload)
		}
		t := out.Table{Cols: []string{"date", "label", "category", "amount"}}
		for _, m := range p.rows {
			t.Rows = append(t.Rows, []string{m.Date, m.Label, m.Category, m.Amount})
		}
		return out.Data(t)
	}
	return c
}

// reCursor: the bank's cursor is base64url without padding (~470 chars).
var reCursor = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func validCursor(c string) bool { return len(c) <= 4096 && reCursor.MatchString(c) }

type pfmMvt struct {
	Date      string `json:"date"`
	DataID    string `json:"dataId"`
	Label     string `json:"label"`
	LabelSub  string `json:"labelSub"`
	Category  string `json:"category"`
	Amount    string `json:"amount"`
	AmountNum num    `json:"amountValue"`
}

// pfmPager accumulates movements across pages. The current date line
// carries over (a day can be split between two pages) and a data-id seen
// before is skipped, as the site's own JS does.
type pfmPager struct {
	rows    []pfmMvt
	seen    map[string]bool
	curDate string
}

// read adds the page's movements up to the first one older than from
// (older=true then), and returns the next-page cursor ("" = last page).
func (p *pfmPager) read(doc *htmlx.Doc, from time.Time) (cursor string, older bool, err error) {
	ul := doc.Sel("ul.list__movement")
	if ul.Length() == 0 {
		return "", false, fmt.Errorf("budget-movements : pas de conteneur <ul.list__movement> (dérive de schéma — structure de page modifiée)")
	}
	ul.First().Children().EachWithBreak(func(i int, li *goquery.Selection) bool {
		switch {
		case li.HasClass("list-operation-date-line"):
			p.curDate = htmlx.Clean(li.Text())
			if d, ok := frLongDate(p.curDate); ok && !from.IsZero() && d.Before(from) {
				older = true
				return false
			}
		case li.HasClass("list-operation-item"):
			id, _ := li.Attr("data-id")
			if id != "" && p.seen[id] {
				return true
			}
			label := htmlx.Clean(li.Find(".list-operation-item__label-name").First().Text())
			if label == "" {
				label = htmlx.Clean(li.Find(".list-operation-item__label").First().Text())
			}
			amount := htmlx.Clean(li.Find(".list-operation-item__amount").First().Text())
			if label == "" && amount == "" {
				err = fmt.Errorf("budget-movements élément %d (date %q, id %q) : libellé ET montant vides — dérive de schéma", i, p.curDate, id)
				return false
			}
			p.seen[id] = true
			p.rows = append(p.rows, pfmMvt{
				Date:      p.curDate,
				DataID:    id,
				Label:     label,
				LabelSub:  htmlx.Clean(li.Find(".list-operation-item__label-sub").First().Text()),
				Category:  htmlx.Clean(li.Find(".list-operation-item__category").First().Text()),
				Amount:    amount,
				AmountNum: mknum(amount),
			})
		}
		if c, ok := li.Attr("data-operations-next-pagination"); ok {
			cursor = c
		}
		return true
	})
	if older || err != nil {
		return "", older, err
	}
	return cursor, false, nil
}

var frMonths = map[string]time.Month{
	"janvier": time.January, "février": time.February, "fevrier": time.February,
	"mars": time.March, "avril": time.April, "mai": time.May, "juin": time.June,
	"juillet": time.July, "août": time.August, "aout": time.August,
	"septembre": time.September, "octobre": time.October,
	"novembre": time.November, "décembre": time.December, "decembre": time.December,
}

// frLongDate parses a date line like "mercredi 30 septembre 2026" (the
// weekday is optional). ok=false for anything else ("Aujourd'hui"…).
func frLongDate(s string) (time.Time, bool) {
	f := strings.Fields(strings.ToLower(s))
	for i := 0; i+2 < len(f); i++ {
		day, err := strconv.Atoi(f[i])
		if err != nil || day < 1 || day > 31 {
			continue
		}
		m, ok := frMonths[f[i+1]]
		if !ok {
			continue
		}
		year, err := strconv.Atoi(f[i+2])
		if err != nil || year < 1900 {
			continue
		}
		return time.Date(year, m, day, 0, 0, 0, 0, time.UTC), true
	}
	return time.Time{}, false
}
