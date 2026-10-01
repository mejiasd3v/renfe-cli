package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	userAgent   = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"
	saleHost    = "venta.renfe.com"
	saleOrigin  = "https://" + saleHost
	resultsPath = "/vol/buscarTrenEnlaces.do"
)

// session is one anonymous purchase flow on venta.renfe.com.
type session struct {
	http          *http.Client
	jar           *cookieJar
	purchaseID    string // the site's per-tab "compra" id
	scriptSession string
	batch         int
}

func newSession() *session {
	jar := &cookieJar{}
	s := &session{jar: jar, purchaseID: "_" + randomToken(4)}
	s.http = &http.Client{Timeout: 45 * time.Second, Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if req.URL.Host != saleHost {
			return http.ErrUseLastResponse // e.g. the queue-it.net waiting room; reported by the caller
		}
		return nil
	}}
	return s
}

func randomToken(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		v, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			panic(err)
		}
		b[i] = alphabet[v.Int64()]
	}
	return string(b)
}

// tokenify is DWR's base-64 page id encoding from engine.js.
func tokenify(n int64) string {
	const charmap = "1234567890abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ*$"
	var b strings.Builder
	for n > 0 {
		b.WriteByte(charmap[n&63])
		n /= 64
	}
	return b.String()
}

func renfeDate(t time.Time) string { return t.Format("02/01/2006") }

var (
	cutoverPattern = regexp.MustCompile(`id="esConvivencia" value="true"`)
	cutoverDate    = regexp.MustCompile(`id="fechaCorte" value="([0-9/]+)"`)
	cutoverURL     = regexp.MustCompile(`id="urlNSV" value="([^"]+)"`)
)

// search submits the website's search form, then loads the train list it renders.
func (s *session) search(q query) (*searchResult, error) {
	form := url.Values{
		"tipoBusqueda": {"autocomplete"}, "currenLocation": {"menuBusqueda"}, "vengoderenfecom": {"SI"},
		"desOrigen": {q.From.Name}, "desDestino": {q.To.Name}, "cdgoOrigen": {q.From.Key}, "cdgoDestino": {q.To.Key},
		"idiomaBusqueda": {"ES"}, "FechaIdaSel": {renfeDate(q.Date)}, "_fechaIdaVisual": {renfeDate(q.Date)},
		"FechaVueltaSel": {""}, "_fechaVueltaVisual": {""}, "minPriceDeparture": {"false"}, "minPriceReturn": {"false"},
		"adultos_": {strconv.Itoa(q.Adults)}, "ninos_": {strconv.Itoa(q.Children)}, "ninosMenores": {strconv.Itoa(q.Infants)},
		"codPromocional": {""}, "plazaH": {"false"}, "sinEnlace": {"false"}, "conMascota": {"false"}, "conBicicleta": {"false"},
		"asistencia": {"false"}, "franjaHoraI": {""}, "franjaHoraV": {""}, "Idioma": {"es"}, "Pais": {"ES"},
	}
	if !q.Return.IsZero() {
		form.Set("FechaVueltaSel", renfeDate(q.Return))
		form.Set("_fechaVueltaVisual", renfeDate(q.Return))
	}
	req, err := http.NewRequest(http.MethodPost, saleOrigin+"/vol/buscarTren.do?Idioma=es&Pais=ES", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", "https://www.renfe.com/")
	req.Header.Set("Origin", "https://www.renfe.com")
	page, final, err := s.do(req)
	if err != nil {
		return nil, err
	}
	if final.Path != resultsPath {
		return nil, fmt.Errorf("Renfe did not show search results (landed on %s)", final.Path)
	}
	if cutoverPattern.Match(page) {
		if m := cutoverDate.FindSubmatch(page); m != nil {
			if cut, err := time.Parse("02/01/2006", string(m[1])); err == nil && !q.Date.Before(cut) {
				next := ""
				if u := cutoverURL.FindSubmatch(page); u != nil {
					next = " at " + html.UnescapeString(string(u[1]))
				}
				return nil, fmt.Errorf("Renfe sells trains from %s on its new website%s, which this CLI does not support yet", m[1], next)
			}
		}
	}
	if err := s.startScripting(); err != nil {
		return nil, err
	}
	trayecto := "I"
	if !q.Return.IsZero() {
		trayecto = "IV"
	}
	returnDate := ""
	if !q.Return.IsZero() {
		returnDate = renfeDate(q.Return)
	}
	filter := []field{
		{"origen", q.From.Code}, {"destino", q.To.Code}, {"fechaSalida", renfeDate(q.Date)}, {"fechaVuelta", returnDate},
		{"trayecto", trayecto}, {"idaVuelta", ""}, {"adultos", strconv.Itoa(q.Adults)}, {"ninos", strconv.Itoa(q.Children)},
		{"ninosMenores", strconv.Itoa(q.Infants)}, {"sinEnlace", "false"}, {"conMascota", "false"}, {"conBicicleta", "false"},
		{"plazaH", "false"}, {"atendo", "false"}, {"tipoFranjaI", ""}, {"horaFranjaIda", ""}, {"tipoFranjaV", ""},
		{"horaFranjaVuelta", ""}, {"codPromo", ""},
	}
	// listaTrenes.js sends the filter values again in this header (1/0 for booleans).
	key := make([]string, len(filter))
	for i, f := range filter {
		v := f.Value.(string)
		switch v {
		case "true":
			v = "1"
		case "false":
			v = "0"
		}
		key[i] = v
	}
	raw, err := s.call("trainEnlacesManager", "getTrainsList", []any{filter}, map[string]string{"Akamai-Key": strings.Join(key, ", ")})
	if err != nil {
		return nil, err
	}
	result := &searchResult{Origin: q.From, Destination: q.To, Passengers: party{q.Adults, q.Children, q.Infants}}
	result.Outbound, result.Return, err = parseTrainList(raw, q)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// startScripting opens the DWR channel the results page uses, as engine.js does on first call.
func (s *session) startScripting() error {
	raw, err := s.call("__System", "generateId", nil, nil)
	if err != nil {
		return err
	}
	var id string
	if json.Unmarshal(raw, &id) != nil || id == "" {
		return errors.New("Renfe did not return a script session id")
	}
	s.jar.SetCookies(&url.URL{Scheme: "https", Host: saleHost, Path: "/vol/"}, []*http.Cookie{{Name: "DWRSESSIONID", Value: id, Path: "/vol"}})
	random, err := rand.Int(rand.Reader, big.NewInt(1e16))
	if err != nil {
		return err
	}
	s.scriptSession = id + "/" + tokenify(time.Now().UnixMilli()) + "-" + tokenify(random.Int64())
	// sesiones.js registers the tab's purchase id before anything else.
	_, err = s.call("buyEnlacesManager", "actualizaObjetosSesion", []any{[]any{s.purchaseID, ""}}, nil)
	return err
}

func (s *session) call(script, method string, args []any, headers map[string]string) (json.RawMessage, error) {
	body := dwrBody(script, method, args, s.batch, resultsPath, s.scriptSession)
	s.batch++
	req, err := http.NewRequest(http.MethodPost, saleOrigin+"/vol/dwr/call/plaincall/"+script+"."+method+".dwr", strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Origin", saleOrigin)
	req.Header.Set("Referer", saleOrigin+resultsPath)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	reply, _, err := s.do(req)
	if err != nil {
		return nil, fmt.Errorf("%s.%s: %w", script, method, err)
	}
	raw, err := dwrReply(reply)
	if err != nil {
		return nil, fmt.Errorf("%s.%s: %w", script, method, err)
	}
	return raw, nil
}

// do sends a request and returns the body and final URL, failing on waiting rooms and HTTP errors.
func (s *session) do(req *http.Request) ([]byte, *url.URL, error) {
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Language", "es-ES,es;q=0.9,en;q=0.8")
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("request to Renfe failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, nil, fmt.Errorf("cannot read Renfe response: %w", err)
	}
	if loc, _ := resp.Location(); loc != nil && loc.Host != saleHost {
		if strings.HasSuffix(loc.Hostname(), "queue-it.net") {
			return nil, nil, errors.New("Renfe's virtual waiting room (queue-it) is active; try again later or use renfe.com in a browser")
		}
		return nil, nil, fmt.Errorf("Renfe redirected to unexpected host %s", loc.Host)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("Renfe returned HTTP %d", resp.StatusCode)
	}
	return body, resp.Request.URL, nil
}

// selectFares records the chosen trains and fares in the purchase, as the
// "Seleccionar" button does, and loads the passenger-details step.
// It reports whether Renfe requires signing in before buying.
func (s *session) selectFares(outbound selection, ret *selection) (loginRequired bool, err error) {
	ids := []any{s.purchaseID, ""}
	choices := []field{{"ida", []any{outbound.Train.ID, outbound.Fare.Code, false, outbound.Fare.seatLink}}}
	all := []selection{outbound}
	if ret != nil {
		choices = append(choices, field{"vuelta", []any{ret.Train.ID, ret.Fare.Code, false, ret.Fare.seatLink}})
		all = append(all, *ret)
	}
	choices = append(choices, field{"idCompra", s.purchaseID})
	for _, sel := range all {
		operator := ""
		for _, l := range sel.Train.Legs {
			if l.Operator == "LC" { // AVLO; listaTrenes.js only passes this operator code
				operator = "LC"
			}
		}
		raw, err := s.call("trainEnlacesManager", "showModalLogin", []any{operator, false, ids}, nil)
		if err != nil {
			return false, err
		}
		var show bool
		if json.Unmarshal(raw, &show) == nil && show {
			loginRequired = true
		}
	}
	if _, err := s.call("trainEnlacesManager", "validarTarifasSeleccionadas", []any{choices}, nil); err != nil {
		return false, err
	}
	if _, err := s.call("trainEnlacesManager", "setTrenEnlaceSeleccionado", []any{choices}, nil); err != nil {
		return false, err
	}
	req, err := http.NewRequest(http.MethodGet, s.passengerURL(), nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Referer", saleOrigin+resultsPath)
	page, final, err := s.do(req)
	if err != nil {
		return false, err
	}
	if final.Path != "/vol/datosViajeEnlaces.do" || !strings.Contains(string(page), "Datos Viajeros") {
		return false, fmt.Errorf("Renfe did not accept the selection (landed on %s)", final.Path)
	}
	for _, sel := range all {
		for _, l := range sel.Train.Legs {
			if !strings.Contains(string(page), l.Train) {
				return false, fmt.Errorf("Renfe's passenger page does not show train %s", l.Train)
			}
		}
	}
	return loginRequired, nil
}

func (s *session) passengerURL() string {
	return saleOrigin + "/vol/datosViajeEnlaces.do?c=" + url.QueryEscape(s.purchaseID)
}
