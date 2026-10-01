package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"
)

// bookingRecord is a purchase made with this CLI. TicketURL is Renfe's PDF link; its
// random suffix is the only thing protecting the ticket, so bookings.json stays private.
type bookingRecord struct {
	Locator     string    `json:"locator"`
	PurchasedAt time.Time `json:"purchased_at"`
	TotalEUR    float64   `json:"total_eur,omitempty"`
	TicketURL   string    `json:"ticket_url"`
	TicketPDF   string    `json:"ticket_pdf,omitempty"`
}

var locatorPattern = regexp.MustCompile(`^[A-Z0-9]{5,8}$`)

// noPDF mirrors confirmacion.js's pdfExist: Renfe writes NO_PDF as the link's last segment
// when it has not generated the ticket.
func noPDF(link string) bool { return link[strings.LastIndex(link, "/")+1:] == "NO_PDF" || link == "" }

func loadBookings(dir string) ([]bookingRecord, error) {
	data, err := os.ReadFile(filepath.Join(dir, "bookings.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records []bookingRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("invalid bookings.json: %w", err)
	}
	return records, nil
}

func saveBooking(dir string, r bookingRecord) error {
	records, err := loadBookings(dir)
	if err != nil {
		return err
	}
	replaced := false
	for i := range records {
		if records[i].Locator == r.Locator {
			records[i], replaced = r, true
		}
	}
	if !replaced {
		records = append(records, r)
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, "bookings.json"), data)
}

// ticketURLCheck accepts only HTTPS PDF links on Renfe's own domains.
func ticketURLCheck(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return errors.New("Renfe's ticket link is not an HTTPS URL")
	}
	host := u.Hostname()
	if !(host == "renfe.es" || strings.HasSuffix(host, ".renfe.es") || host == "renfe.com" || strings.HasSuffix(host, ".renfe.com")) || !strings.HasSuffix(u.Path, ".pdf") {
		return fmt.Errorf("unexpected ticket link host or path (%s)", host)
	}
	return nil
}

// downloadTicket saves Renfe's ticket PDF to path with owner-only permissions.
func downloadTicket(rawURL, path string) (int, error) {
	if err := ticketURLCheck(rawURL); err != nil {
		return 0, err
	}
	client := &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 || ticketURLCheck(req.URL.String()) != nil {
			return errors.New("ticket download redirected outside Renfe")
		}
		return nil
	}}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("cannot download the ticket: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return 0, fmt.Errorf("cannot download the ticket: %w", err)
	}
	if resp.StatusCode != http.StatusOK || !bytes.HasPrefix(body, []byte("%PDF-")) {
		return 0, fmt.Errorf("Renfe did not return a PDF (HTTP %d)", resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return 0, err
	}
	if err := writeFileAtomic(path, body); err != nil {
		return 0, err
	}
	return len(body), nil
}

// keepTicket records a confirmed purchase and downloads its PDF. The purchase is already
// complete, so problems come back as a message for the caller to report, never as an
// error that could make a caller retry the payment.
func keepTicket(dir, ticketsDir, locator, ticketURL string, totalEUR float64) (pdfPath, problem string) {
	r := bookingRecord{Locator: locator, PurchasedAt: time.Now().UTC(), TotalEUR: totalEUR, TicketURL: ticketURL}
	switch {
	case !locatorPattern.MatchString(locator):
		problem = "no locator to name the ticket file; the ticket is in the buyer's email"
	case noPDF(ticketURL):
		problem = "Renfe did not provide a ticket PDF link; the ticket is in the buyer's email"
	default:
		path := filepath.Join(ticketsDir, locator+".pdf")
		if _, err := downloadTicket(ticketURL, path); err != nil {
			problem = err.Error() + "; retry with renfe ticket " + locator
		} else {
			r.TicketPDF, pdfPath = path, path
		}
	}
	if locator != "" {
		if err := saveBooking(dir, r); err != nil {
			problem = strings.TrimPrefix(problem+"; cannot record the booking: "+err.Error(), "; ")
		}
	}
	return pdfPath, problem
}

type ticketResult struct {
	Locator   string `json:"locator"`
	TicketPDF string `json:"ticket_pdf"`
	Bytes     int    `json:"bytes"`
}

// runTicket downloads the ticket of a purchase made with this CLI, or of the purchase
// whose confirmation page is open in the CLI's browser.
func runTicket(args []string, out, stderr io.Writer) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("ticket", flag.ContinueOnError)
	fs.SetOutput(stderr)
	ticketsDir := fs.String("tickets-dir", filepath.Join(dir, "tickets"), "where to save ticket PDFs")
	format := fs.String("format", "json", "json or table")
	var positional []string
	for len(args) > 0 { // allow flags after the locator
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(positional) > 1 || (*format != "json" && *format != "table") {
		return errors.New("usage: renfe ticket [LOCATOR] [--tickets-dir DIR] [--format json|table]")
	}
	locator := ""
	if len(positional) == 1 {
		if locator = strings.ToUpper(strings.TrimSpace(positional[0])); !locatorPattern.MatchString(locator) {
			return fmt.Errorf("%q is not a Renfe locator (5 to 8 letters and digits)", positional[0])
		}
	}

	records, err := loadBookings(dir)
	if err != nil {
		return err
	}
	var record *bookingRecord
	for i := range records {
		if records[i].Locator == locator {
			record = &records[i]
		}
	}
	if record == nil {
		// Not recorded: use the confirmation page still open in the CLI's browser.
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		t, err := findTab(ctx, "Confirmación")
		if err != nil {
			if locator == "" {
				return fmt.Errorf("no confirmation page open in the renfe browser (%v); pass a locator bought with this CLI", err)
			}
			return fmt.Errorf("no recorded booking %s and no confirmation page open in the renfe browser; the ticket is in the buyer's email", locator)
		}
		defer t.close()
		var c confirmation
		if err := t.eval(ctx, jsCall(confirmationJS), &c); err != nil {
			return err
		}
		if !c.Confirmed || (locator != "" && c.Locator != locator) {
			return fmt.Errorf("no recorded booking %s; the open confirmation page is for %q", locator, c.Locator)
		}
		record = &bookingRecord{Locator: c.Locator, PurchasedAt: time.Now().UTC(), TicketURL: c.TicketURL}
	}
	if noPDF(record.TicketURL) {
		return fmt.Errorf("Renfe provided no ticket PDF link for %s; the ticket is in the buyer's email", record.Locator)
	}
	path := filepath.Join(*ticketsDir, record.Locator+".pdf")
	n, err := downloadTicket(record.TicketURL, path)
	if err != nil {
		return err
	}
	record.TicketPDF = path
	if err := saveBooking(dir, *record); err != nil {
		fmt.Fprintf(stderr, "Warning: cannot record the booking: %v\n", err)
	}
	r := ticketResult{Locator: record.Locator, TicketPDF: path, Bytes: n}
	if *format == "table" {
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintf(w, "Locator:\t%s\nTicket:\t%s (%d bytes)\n", r.Locator, r.TicketPDF, r.Bytes)
		return w.Flush()
	}
	return writeJSON(out, r)
}
