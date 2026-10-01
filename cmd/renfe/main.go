// Command renfe searches Renfe trains and books them through venta.renfe.com.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"
)

const usage = `Usage: renfe <stations|search|book|pay|ticket> [flags]

  stations QUERY                      find station names and codes
  search --from Q --to Q --date YYYY-MM-DD [--return YYYY-MM-DD]
  book   --from Q --to Q --date YYYY-MM-DD --train HH:MM|NUMBER --fare NAME|CODE|cheapest
         [--return YYYY-MM-DD --return-train ... --return-fare ...]
         [--passenger ID ... [--pay bizum --max-total EUR]] [--dry-run]
  pay    --max-total EUR              pay the purchase "book --passenger" prepared
  ticket [LOCATOR]                    download the ticket PDF of a purchase made here

Stations accept names ("madrid", "barcelona sants", "Cádiz") or codes ("60000").
Passengers: --passenger IDs from ~/.config/renfe/passengers.json, or for search
--adults (default 1), --children (4-13), --infants (under 4).
JSON on stdout by default; --format table for people. Status messages go to stderr.

book selects the train and fare on renfe.com and opens the purchase in the CLI's
browser window. With --passenger it fills the passenger form and stops on the
payment page with Bizum selected (status ready_to_pay, with Renfe's total).
pay, or book --pay bizum, then requests the Bizum payment; the buyer approves
it in their bank app and the command returns the booking locator and saves the
ticket PDF to ~/.config/renfe/tickets/LOCATOR.pdf.
Run "renfe <command> --help" for all flags.`

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, "renfe:", err)
		os.Exit(1)
	}
}

func run(args []string, out, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(out, usage)
		return nil
	}
	commands := map[string]func([]string, io.Writer, io.Writer) error{
		"stations": runStations, "search": runSearch, "book": runBook, "pay": runPay, "ticket": runTicket,
	}
	if command, ok := commands[args[0]]; ok {
		return command(args[1:], out, stderr)
	}
	return fmt.Errorf("unknown command %q; run renfe --help", args[0])
}

// configDir is ~/.config/renfe, which holds the station cache, passengers,
// bookings, tickets and the browser profile.
func configDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".config", "renfe")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

// writeFileAtomic replaces path with data, readable by the owner only.
func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func writeJSON(out io.Writer, v any) error {
	e := json.NewEncoder(out)
	e.SetIndent("", "  ")
	e.SetEscapeHTML(false)
	return e.Encode(v)
}

func validFormat(format string) bool { return format == "json" || format == "table" }

// parseInterspersed parses flags that may follow positional arguments,
// e.g. "stations madrid --format table", and returns the positional ones.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
	return positional, nil
}

func stationList(dir string) ([]station, error) {
	return loadStations(dir, &http.Client{Timeout: 30 * time.Second})
}

func runStations(args []string, out, stderr io.Writer) error {
	fs := flag.NewFlagSet("stations", flag.ContinueOnError)
	fs.SetOutput(stderr)
	limit := fs.Int("limit", 20, "maximum matches to show (0: all)")
	format := fs.String("format", "json", "json or table")
	query, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(query) == 0 || *limit < 0 || !validFormat(*format) {
		return errors.New("usage: renfe stations QUERY [--limit N] [--format json|table]")
	}
	dir, err := configDir()
	if err != nil {
		return err
	}
	stations, err := stationList(dir)
	if err != nil {
		return err
	}
	matches := matchStations(stations, strings.Join(query, " "))
	if *limit > 0 && len(matches) > *limit {
		matches = matches[:*limit]
	}
	if matches == nil {
		matches = []station{}
	}
	if *format == "table" {
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "CODE\tNAME")
		for _, s := range matches {
			fmt.Fprintf(w, "%s\t%s\n", s.Code, s.Name)
		}
		return w.Flush()
	}
	return writeJSON(out, matches)
}
