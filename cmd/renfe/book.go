package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
)

type bookingResult struct {
	Status        string     `json:"status"` // dry_run, awaiting_passenger_details, ready_to_pay, payment_pending, confirmed
	Passengers    party      `json:"passengers"`
	PassengerIDs  []string   `json:"passenger_ids,omitempty"`
	Outbound      selection  `json:"outbound"`
	Return        *selection `json:"return,omitempty"`
	TotalEUR      *float64   `json:"total_eur,omitempty"` // Renfe's total for the whole purchase, from the payment page
	PaymentMethod string     `json:"payment_method,omitempty"`
	Locator       string     `json:"locator,omitempty"`
	TicketPDF     string     `json:"ticket_pdf,omitempty"`
	TicketError   string     `json:"ticket_error,omitempty"`   // the purchase succeeded; only the PDF download failed
	LoginRequired *bool      `json:"login_required,omitempty"` // only known after selecting on renfe.com
	Browser       string     `json:"browser,omitempty"`
	NextStep      string     `json:"next_step"`
}

type payResult struct {
	Status        string  `json:"status"` // payment_pending or confirmed
	TotalEUR      float64 `json:"total_eur"`
	PaymentMethod string  `json:"payment_method"`
	Locator       string  `json:"locator,omitempty"`
	TicketPDF     string  `json:"ticket_pdf,omitempty"`
	TicketError   string  `json:"ticket_error,omitempty"`
	NextStep      string  `json:"next_step"`
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// checkoutOptions are book's flags for completing the purchase.
type checkoutOptions struct {
	passengerIDs   multiFlag
	passengersFile string
	pay            string
	maxTotal       float64
	wait           time.Duration
	ticketsDir     string
}

func (o *checkoutOptions) register(fs *flag.FlagSet, dir string) {
	fs.Var(&o.passengerIDs, "passenger", "passenger id from the passengers file; repeat for each traveller, buyer first")
	fs.StringVar(&o.passengersFile, "passengers-file", filepath.Join(dir, "passengers.json"), "passengers file")
	fs.StringVar(&o.pay, "pay", "", `"bizum" to pay right away (requires --passenger and --max-total)`)
	fs.Float64Var(&o.maxTotal, "max-total", 0, "abort if Renfe's total for the whole purchase exceeds this, in EUR")
	fs.DurationVar(&o.wait, "wait", 5*time.Minute, "how long to wait for the Bizum approval")
	fs.StringVar(&o.ticketsDir, "tickets-dir", filepath.Join(dir, "tickets"), "where to save the ticket PDF after paying")
}

func (o *checkoutOptions) validate() error {
	switch {
	case o.pay != "" && o.pay != "bizum":
		return errors.New(`--pay only supports "bizum"`)
	case o.pay != "" && (len(o.passengerIDs) == 0 || o.maxTotal <= 0):
		return errors.New("--pay needs --passenger and --max-total")
	case o.maxTotal < 0 || o.wait <= 0:
		return errors.New("--max-total and --wait must be positive")
	}
	return nil
}

func maxCents(maxTotal float64) int { return int(math.Round(maxTotal * 100)) }

// lockBooking serialises book and pay: they share one browser and one purchase session.
func lockBooking(dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, "booking.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another renfe book or pay command is running")
	}
	return func() { f.Close() }, nil
}

// checkout selects the chosen fares on renfe.com, opens the purchase in the CLI's browser
// and, with passengers, drives it to the payment page and optionally pays.
func checkout(s *session, booking *bookingResult, travellers []passenger, bizumPhone string, o checkoutOptions, dir string, stderr io.Writer) error {
	unlock, err := lockBooking(dir)
	if err != nil {
		return err
	}
	defer unlock()
	fmt.Fprintln(stderr, "Selecting the train and fare on renfe.com...")
	loginRequired, err := s.selectFares(booking.Outbound, booking.Return)
	if err != nil {
		return err
	}
	booking.LoginRequired = &loginRequired
	ctx := context.Background()
	openCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	t, err := openBookingTab(openCtx, dir, s.jar, s.passengerURL(), stderr)
	cancel()
	if err != nil {
		return err
	}
	defer t.close()
	booking.Browser = t.Browser
	if len(travellers) == 0 {
		booking.Status = "awaiting_passenger_details"
		booking.NextStep = "In the opened browser window: enter passenger details, review the total, choose seats/extras and pay. Renfe sessions expire after a period of inactivity."
		if loginRequired {
			booking.NextStep = "Renfe requires signing in to buy this train: sign in from the browser window first. " + booking.NextStep
		}
		return nil
	}

	fmt.Fprintln(stderr, "Filling passenger details...")
	if err := fillPassengers(ctx, t, travellers); err != nil {
		return err
	}
	fmt.Fprintln(stderr, "Selecting Bizum on the payment page...")
	cents, err := preparePayment(ctx, t, &travellers[0])
	if err != nil {
		return err
	}
	total := float64(cents) / 100
	booking.TotalEUR, booking.PaymentMethod = &total, "bizum"
	if o.maxTotal > 0 && cents > maxCents(o.maxTotal) {
		return fmt.Errorf("Renfe's total is %s EUR, above --max-total %s; nothing was paid", euros(cents), euros(maxCents(o.maxTotal)))
	}
	if o.pay == "" {
		booking.Status = "ready_to_pay"
		booking.NextStep = fmt.Sprintf("Renfe's payment page is open with Bizum selected; total %s EUR. To pay: renfe pay --max-total %s, then the buyer approves the Bizum request in their bank app. Renfe's session expires after some minutes of inactivity.", euros(cents), euros(cents))
		return nil
	}
	p, err := startBizum(ctx, t, cents, maxCents(o.maxTotal), bizumPhone, o.wait, stderr)
	if err != nil {
		return err
	}
	booking.Status, booking.Locator = p.Status, p.Locator
	if p.Status == "confirmed" {
		booking.TicketPDF, booking.TicketError = keepTicket(dir, o.ticketsDir, p.Locator, p.TicketURL, total)
		reportTicket(stderr, booking.TicketPDF, booking.TicketError)
	}
	booking.NextStep = paymentNextStep(p)
	return nil
}

func reportTicket(stderr io.Writer, path, problem string) {
	if path != "" {
		fmt.Fprintf(stderr, "Ticket saved to %s\n", path)
	}
	if problem != "" {
		fmt.Fprintf(stderr, "Ticket PDF not saved: %s\n", problem)
	}
}

func startBizum(ctx context.Context, t *tab, cents, max int, phone string, wait time.Duration, stderr io.Writer) (payment, error) {
	fmt.Fprintf(stderr, "Requesting a Bizum payment of %s EUR. Approve it in your bank app within %s...\n", euros(cents), wait)
	return payBizum(ctx, t, cents, max, phone, wait)
}

func paymentNextStep(p payment) string {
	if p.Status == "confirmed" {
		return "Booked. Renfe emails the tickets to the buyer; ticket_pdf is a local copy and the locator identifies the booking."
	}
	return "No confirmation yet. If the Bizum request was approved, check the buyer's email and the browser window before booking again; otherwise approve it in the bank app, or let it expire."
}

// runPay pays the purchase that "book --passenger" left on Renfe's payment page.
func runPay(args []string, out, stderr io.Writer) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("pay", flag.ContinueOnError)
	fs.SetOutput(stderr)
	maxTotal := fs.Float64("max-total", 0, "required: abort if Renfe's total exceeds this, in EUR")
	wait := fs.Duration("wait", 5*time.Minute, "how long to wait for the Bizum approval")
	passengersFile := fs.String("passengers-file", filepath.Join(dir, "passengers.json"), "passengers file (for bizum_phone)")
	ticketsDir := fs.String("tickets-dir", filepath.Join(dir, "tickets"), "where to save the ticket PDF after paying")
	format := fs.String("format", "json", "json or table")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *maxTotal <= 0 || *wait <= 0 || (*format != "json" && *format != "table") {
		return errors.New("usage: renfe pay --max-total EUR [--wait 5m] [--format json|table]")
	}
	file, err := loadPassengerFile(*passengersFile, stderr)
	if err != nil {
		return err
	}
	phone := bizumPhone(file)
	unlock, err := lockBooking(dir)
	if err != nil {
		return err
	}
	defer unlock()
	ctx := context.Background()
	findCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	t, err := findTab(findCtx, "Método de pago")
	cancel()
	if err != nil {
		return fmt.Errorf("%w; prepare the purchase with renfe book --passenger ... first", err)
	}
	defer t.close()
	cents, err := preparePayment(ctx, t, nil)
	if err != nil {
		return err
	}
	p, err := startBizum(ctx, t, cents, maxCents(*maxTotal), phone, *wait, stderr)
	if err != nil {
		return err
	}
	r := payResult{Status: p.Status, TotalEUR: float64(cents) / 100, PaymentMethod: "bizum", Locator: p.Locator, NextStep: paymentNextStep(p)}
	if p.Status == "confirmed" {
		r.TicketPDF, r.TicketError = keepTicket(dir, *ticketsDir, p.Locator, p.TicketURL, r.TotalEUR)
		reportTicket(stderr, r.TicketPDF, r.TicketError)
	}
	if *format == "table" {
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintf(w, "Status:\t%s\nTotal:\t%s EUR (Bizum)\n", r.Status, euros(cents))
		if r.Locator != "" {
			fmt.Fprintf(w, "Locator:\t%s\n", r.Locator)
		}
		if r.TicketPDF != "" {
			fmt.Fprintf(w, "Ticket:\t%s\n", r.TicketPDF)
		}
		fmt.Fprintf(w, "Next:\t%s\n", r.NextStep)
		return w.Flush()
	}
	return writeJSON(out, r)
}

// bizumPhone is the file's bizum_phone, or the first passenger's phone.
func bizumPhone(f passengerFile) string {
	if f.BizumPhone != "" || len(f.Passengers) == 0 {
		return f.BizumPhone
	}
	return f.Passengers[0].Phone
}

func runBook(args []string, out, stderr io.Writer) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("book", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var trip tripFlags
	trip.register(fs)
	trainSel := fs.String("train", "", "outbound departure time (HH:MM) or train number(s), e.g. 03063 or 05871+02100")
	fareSel := fs.String("fare", "", "outbound fare name or code (basico, elige, N1010...) or cheapest")
	returnTrainSel := fs.String("return-train", "", "return departure time or train number(s)")
	returnFareSel := fs.String("return-fare", "", "return fare name or code, or cheapest")
	maxFare := fs.Float64("max-fare", 0, "abort if a selected fare exceeds this per-passenger price in EUR (0: no limit)")
	dryRun := fs.Bool("dry-run", false, "resolve the train and fare without selecting them or opening a browser")
	var co checkoutOptions
	co.register(fs, dir)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || !validFormat(trip.format) {
		return errors.New("unexpected arguments or invalid --format; run renfe book --help")
	}
	if *trainSel == "" || *fareSel == "" {
		return errors.New("book requires --train and --fare (see renfe search for options)")
	}
	roundTrip := trip.ret != ""
	if roundTrip != (*returnTrainSel != "") || roundTrip != (*returnFareSel != "") {
		return errors.New("a round trip needs --return, --return-train and --return-fare together")
	}
	if *maxFare < 0 {
		return errors.New("--max-fare cannot be negative")
	}
	if err := co.validate(); err != nil {
		return err
	}
	var travellers []passenger
	var phone string
	if len(co.passengerIDs) > 0 {
		if countsSet(fs) {
			return errors.New("use either --passenger or --adults/--children/--infants")
		}
		file, err := loadPassengerFile(co.passengersFile, stderr)
		if err != nil {
			return err
		}
		if travellers, err = pickPassengers(file, co.passengerIDs); err != nil {
			return err
		}
		p := countParty(travellers)
		trip.adults, trip.children, trip.infants = p.Adults, p.Children, p.Infants
		phone = bizumPhone(file)
	}

	s, result, err := searchTrip(trip, dir, stderr)
	if err != nil {
		return err
	}
	booking := bookingResult{Status: "dry_run", Passengers: result.Passengers}
	if booking.Outbound, err = choose(result.Outbound, *trainSel, *fareSel, *maxFare); err != nil {
		return fmt.Errorf("outbound: %w", err)
	}
	if result.Return != nil {
		sel, err := choose(*result.Return, *returnTrainSel, *returnFareSel, *maxFare)
		if err != nil {
			return fmt.Errorf("return: %w", err)
		}
		booking.Return = &sel
	}
	for _, p := range travellers {
		booking.PassengerIDs = append(booking.PassengerIDs, p.ID)
	}
	booking.NextStep = "Run again without --dry-run to select this on renfe.com; add --passenger to fill in the passengers and reach the payment page."
	if !*dryRun {
		if err := checkout(s, &booking, travellers, phone, co, dir, stderr); err != nil {
			return err
		}
	}
	for _, sel := range []*selection{&booking.Outbound, booking.Return} {
		if sel != nil {
			sel.Train.Fares = nil // the chosen fare is reported separately
		}
	}
	if trip.format == "table" {
		return printBookingTable(out, booking)
	}
	return writeJSON(out, booking)
}

func printBookingTable(out io.Writer, b bookingResult) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "Status:\t%s\n", b.Status)
	for i, sel := range []*selection{&b.Outbound, b.Return} {
		if sel == nil {
			continue
		}
		label := "Outbound:"
		if i == 1 {
			label = "Return:"
		}
		fmt.Fprintf(w, "%s\t%s %s-%s  %s  %s (%s) %.2f EUR per passenger\n", label, sel.Train.Date, sel.Train.Departure, sel.Train.Arrival, trainTypes(sel.Train), sel.Fare.Name, sel.Fare.Code, sel.Fare.PriceEUR)
	}
	fmt.Fprintf(w, "Passengers:\t%d adults, %d children, %d infants\n", b.Passengers.Adults, b.Passengers.Children, b.Passengers.Infants)
	if b.TotalEUR != nil {
		fmt.Fprintf(w, "Total:\t%.2f EUR (%s)\n", *b.TotalEUR, b.PaymentMethod)
	}
	if b.Locator != "" {
		fmt.Fprintf(w, "Locator:\t%s\n", b.Locator)
	}
	if b.TicketPDF != "" {
		fmt.Fprintf(w, "Ticket:\t%s\n", b.TicketPDF)
	}
	if b.Browser != "" {
		fmt.Fprintf(w, "Browser:\t%s\n", b.Browser)
	}
	fmt.Fprintf(w, "Next:\t%s\n", b.NextStep)
	return w.Flush()
}
