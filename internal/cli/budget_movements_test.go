package cli

import (
	"testing"
	"time"

	"github.com/thomasmarcelin754/boursocli/internal/htmlx"
)

func pfmPage(t *testing.T, items string) *htmlx.Doc {
	t.Helper()
	d, err := htmlx.Parse([]byte(`<html><body><ul class="list__movement">` + items + `</ul></body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func item(id, label, amount string) string {
	return `<li class="list-operation-item" data-id="` + id + `"><span class="list-operation-item__label-name">` + label +
		`</span><span class="list-operation-item__amount">` + amount + `</span></li>`
}

// Two pages as the bank sends them: a day split across the pages, the
// site's duplicate rule (same data-id), the cursor on the last <li>.
func TestPFMPager(t *testing.T) {
	p := pfmPager{seen: map[string]bool{}}
	page1 := pfmPage(t, `<li class="list-operation-date-line">mercredi 30 septembre 2026</li>`+
		item("a", "A", "− 1,00 €")+
		`<li class="list-operation-date-line">mardi 29 septembre 2026</li>`+
		item("b", "B", "2,00 €")+
		`<li data-operations-next-pagination="eJxNEXT_1-a"><a data-operations-next-pagination-trigger>Mouvements précédents</a></li>`)
	cur, older, err := p.read(page1, time.Time{})
	if err != nil || older || cur != "eJxNEXT_1-a" {
		t.Fatalf("page 1: cursor=%q older=%v err=%v", cur, older, err)
	}
	page2 := pfmPage(t, item("b", "B", "2,00 €")+ // already seen
		item("c", "C", "3,00 €")+ // same day as the last date line of page 1
		`<li class="list-operation-date-line">lundi 28 septembre 2026</li>`+
		item("d", "D", "4,00 €"))
	cur, older, err = p.read(page2, time.Time{})
	if err != nil || older || cur != "" {
		t.Fatalf("page 2: cursor=%q older=%v err=%v", cur, older, err)
	}
	if len(p.rows) != 4 {
		t.Fatalf("%d rows, want 4 (duplicate skipped): %+v", len(p.rows), p.rows)
	}
	if p.rows[2].DataID != "c" || p.rows[2].Date != "mardi 29 septembre 2026" {
		t.Errorf("split day lost its date: %+v", p.rows[2])
	}

	// --from: stop at the first date line older than it.
	q := pfmPager{seen: map[string]bool{}}
	from := time.Date(2026, time.September, 29, 0, 0, 0, 0, time.UTC)
	_, older, err = q.read(page1, from)
	if err != nil || older {
		t.Fatalf("page 1 within --from: older=%v err=%v", older, err)
	}
	_, older, err = q.read(page2, from)
	if err != nil || !older || len(q.rows) != 3 {
		t.Fatalf("stop at --from: older=%v rows=%d err=%v", older, len(q.rows), err)
	}
}

func TestFrLongDate(t *testing.T) {
	cases := map[string]time.Time{
		"mercredi 30 septembre 2026": time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC),
		"1 août 2026":                time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		"Jeudi 12 Février 2026":      time.Date(2026, 2, 12, 0, 0, 0, 0, time.UTC),
	}
	for in, want := range cases {
		if got, ok := frLongDate(in); !ok || !got.Equal(want) {
			t.Errorf("frLongDate(%q) = %v %v, want %v", in, got, ok, want)
		}
	}
	for _, in := range []string{"Aujourd'hui", "", "30 foo 2026", "32 mai 2026"} {
		if _, ok := frLongDate(in); ok {
			t.Errorf("frLongDate(%q) accepted", in)
		}
	}
	if !validCursor("eJxabc_-9") || validCursor("eJx?a=b") || validCursor("") {
		t.Error("cursor check")
	}
}
