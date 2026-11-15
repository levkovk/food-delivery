package main

import "testing"

var menu = map[int64]menuItem{
	1: {ID: 1, Name: "Борщ", Price: 390, Available: true},
	2: {ID: 2, Name: "Компот", Price: 120, Available: true},
	3: {ID: 3, Name: "Пирог", Price: 320, Available: false},
}

func TestQuoteSumsAndMergesLines(t *testing.T) {
	lines, total, err := calcQuote(menu, []quoteItem{{1, 1}, {2, 2}, {1, 1}})
	if err != nil {
		t.Fatal(err)
	}
	if total != 390*2+120*2 {
		t.Fatalf("total %d", total)
	}
	if len(lines) != 2 || lines[0].Qty != 2 || lines[0].Name != "Борщ" {
		t.Fatalf("lines %+v", lines)
	}
}

func TestQuoteRejects(t *testing.T) {
	for name, items := range map[string][]quoteItem{
		"empty":       nil,
		"foreign":     {{99, 1}},
		"unavailable": {{3, 2}},
		"below min":   {{2, 1}},
		"zero qty":    {{1, 0}},
	} {
		if _, _, err := calcQuote(menu, items); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
