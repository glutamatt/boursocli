package cli

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/spf13/cobra"
	"github.com/thomasmarcelin754/boursocli/internal/htmlx"
	"github.com/thomasmarcelin754/boursocli/internal/out"
)

var docSections = map[string]string{
	"ifu":     "/documents/ifu",
	"bourse":  "/documents/bourse/",
	"releves": "/documents/releves",
	"banque":  "/documents/compte-bancaire",
}

func newDocsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "docs",
		Short: "Documents BoursoBank globaux : IFU, bourse (avis d'opérés), relevés, banque",
	}
	var section, year, from, to string
	c.Flags().StringVar(&section, "section", "bourse", "section : ifu | bourse | releves | banque")
	c.Flags().StringVar(&year, "year", "", "année (IFU uniquement, ex: 2025)")
	account := addAccountFlag(c)
	c.Flags().Lookup("account").Usage = "bourse : compte titres (accountKey, pea ou ord ; défaut : tous les comptes titres)"
	c.Flags().StringVar(&from, "from", "", "bourse : date de début jj/mm/AAAA (défaut : il y a 1 an)")
	c.Flags().StringVar(&to, "to", "", "bourse : date de fin jj/mm/AAAA (défaut : aujourd’hui)")
	c.RunE = func(cmd *cobra.Command, _ []string) error {
		if year != "" {
			if err := validYear("--year", year); err != nil {
				return out.Fail(err)
			}
		}
		if err := validDateFlags(from, to); err != nil {
			return out.Fail(err)
		}
		path, ok := docSections[section]
		if !ok {
			return out.Fail(fmt.Errorf("section %q inconnue (choix : ifu, bourse, releves, banque)", section))
		}
		ctx := cmd.Context()
		cl, _, _, err := session(ctx)
		if err != nil {
			return out.Fail(err)
		}
		if year != "" && section == "ifu" {
			path += "?documents_bank_ifu_type%5Bperiod%5D=" + year
		}
		if section == "bourse" {
			// Without an account filter the page lists nothing (checked
			// live): send the site's own GET filter form.
			q, err := bourseFilter(ctx, *account, from, to)
			if err != nil {
				return out.Fail(err)
			}
			path += "?" + q
		}
		doc, err := getPage(ctx, cl, path, "table.documents__table")
		if err != nil {
			return out.Fail(err)
		}
		sel := doc.Sel("table.documents__table")
		if sel.Length() == 0 {
			return out.Data(map[string]any{"section": section, "count": 0, "documents": []any{}})
		}

		type docrow struct {
			Name        string `json:"name"`
			Detail      string `json:"detail,omitempty"`
			Date        string `json:"date,omitempty"`
			DownloadURL string `json:"downloadUrl,omitempty"`
		}
		var rows []docrow
		sel.Find("tbody tr").Each(func(_ int, tr *goquery.Selection) {
			cells := tr.Find("td")
			if cells.Length() < 2 {
				return
			}

			dlURL := documentLink(tr)
			r := docrow{DownloadURL: dlURL}
			switch {
			case cells.Length() >= 5:
				r.Name = htmlx.Clean(cells.Eq(1).Text())
				r.Detail = htmlx.Clean(cells.Eq(2).Text())
				r.Date = htmlx.Clean(cells.Eq(3).Text())
			default:
				r.Name = htmlx.Clean(cells.Eq(0).Text())
				r.Date = htmlx.Clean(cells.Eq(1).Text())
			}
			rows = append(rows, r)
		})

		payload := map[string]any{"section": section, "count": len(rows), "documents": rows}
		if year != "" {
			payload["year"] = year
		}
		if out.Format != "table" {
			return out.Data(payload)
		}
		t := out.Table{Cols: []string{"name", "detail", "date", "download"}}
		for _, d := range rows {
			t.Rows = append(t.Rows, []string{d.Name, d.Detail, d.Date, d.DownloadURL})
		}
		return out.Data(t)
	}
	return c
}

// bourseFilter builds the query of the /documents/bourse/ filter form (GET):
// the chosen securities account, or all of them, and a date range.
func bourseFilter(ctx context.Context, sel, from, to string) (string, error) {
	_, accs, _, err := resolveAccounts(ctx)
	if err != nil {
		return "", err
	}
	var keys []string
	if sel != "" {
		a, err := pickAccount(accs, sel)
		if err != nil {
			return "", err
		}
		if err := requireKind("docs --section bourse", a, "pea", "ord"); err != nil {
			return "", err
		}
		keys = append(keys, a.AccountKey)
	} else {
		for _, a := range accs {
			if k := a.urlKind(); k == "pea" || k == "ord" {
				keys = append(keys, a.AccountKey)
			}
		}
		if len(keys) == 0 {
			return "", fmt.Errorf("docs --section bourse : aucun compte titres (pea/ord)")
		}
	}
	now := time.Now()
	if from == "" {
		from = now.AddDate(-1, 0, 0).Format("02/01/2006")
	}
	if to == "" {
		to = now.Format("02/01/2006")
	}
	q := url.Values{}
	for _, k := range keys {
		if err := validID("accountKey (réponse de la banque)", k); err != nil {
			return "", err
		}
		q.Add("FiltersTradingAccountDocumentsType[accountsKeys][]", k)
	}
	q.Set("FiltersTradingAccountDocumentsType[fromDate]", from)
	q.Set("FiltersTradingAccountDocumentsType[toDate]", to)
	return q.Encode(), nil
}

// documentLink returns the download URL of a document row: the PDF download
// link (a.c-link--download-pdf, checked live 2026-09-30), else the link on
// the document name. Relative links are made absolute.
func documentLink(tr *goquery.Selection) string {
	var h string
	for _, sel := range []string{"a.c-link--download-pdf[href]", "a.documents__name[href]"} {
		if v, ok := tr.Find(sel).First().Attr("href"); ok && v != "" {
			h = v
			break
		}
	}
	if strings.HasPrefix(h, "/") {
		h = cookieBase + h
	}
	return h
}
