package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestTicketURLCheck(t *testing.T) {
	if err := ticketURLCheck("https://w4.renfe.es//2026-10-08/ABC123-oX6KdaVa.pdf"); err != nil {
		t.Fatalf("Renfe's link rejected: %v", err)
	}
	for _, link := range []string{
		"http://w4.renfe.es/2026-10-08/ABC123-x.pdf",   // not HTTPS
		"https://w4.renfe.es.example.com/ABC123-x.pdf", // look-alike host
		"https://evilrenfe.es/ABC123-x.pdf",            // suffix without the dot
		"https://example.com/ABC123-x.pdf",             // foreign host
		"https://user@w4.renfe.es/ABC123-x.pdf",        // credentials
		"https://w4.renfe.es/2026-10-08/ABC123-x.html", // not a PDF
		"NO_PDF",
	} {
		if ticketURLCheck(link) == nil {
			t.Errorf("accepted %q", link)
		}
	}
}

// A confirmed purchase must never turn into an error, whatever happens to the PDF,
// and must still be recorded so "renfe ticket" can find it.
func TestKeepTicketReportsProblemsAndRecordsBooking(t *testing.T) {
	dir := t.TempDir()
	tickets := filepath.Join(dir, "tickets")
	for _, tc := range []struct{ link, want string }{
		{"https://w4.renfe.es//2026-10-08/NO_PDF", "in the buyer's email"},
		{"NO_PDF", "in the buyer's email"},
		{"https://example.com/ABC123-x.pdf", "retry with renfe ticket ABC123"},
	} {
		path, problem := keepTicket(dir, tickets, "ABC123", tc.link, 6.3)
		if path != "" || !strings.Contains(problem, tc.want) {
			t.Errorf("%q: path %q, problem %q", tc.link, path, problem)
		}
	}
	records, err := loadBookings(dir)
	if err != nil || len(records) != 1 || records[0].Locator != "ABC123" || records[0].TicketURL != "https://example.com/ABC123-x.pdf" {
		t.Fatalf("bookings: %+v %v", records, err)
	}
}
