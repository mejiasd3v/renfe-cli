package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// debugAddress is the loopback DevTools endpoint of the CLI's own browser.
const debugAddress = "127.0.0.1:19229"

func browserExecutable() (string, error) {
	if path := os.Getenv("RENFE_BROWSER"); path != "" {
		return path, nil
	}
	for _, name := range []string{"/Applications/Helium.app/Contents/MacOS/Helium", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "helium", "google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", errors.New("install Chrome or Helium, or set RENFE_BROWSER to a Chromium-based browser executable")
}

func debugJSON(ctx context.Context, path string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+debugAddress+path, nil)
	if err != nil {
		return err
	}
	// Never route the local debugging endpoint through an environment proxy.
	h := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer h.CloseIdleConnections()
	resp, err := h.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("browser debugging endpoint returned HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(dst)
}

type browserVersion struct {
	Browser              string `json:"Browser"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

// ensureBrowser reuses this CLI's browser if it is running, or starts it with a dedicated
// profile. The returned name is the executable when launched, otherwise the reported product.
func ensureBrowser(ctx context.Context, dir string, stderr io.Writer) (v browserVersion, name string, err error) {
	if debugJSON(ctx, "/json/version", &v) == nil {
		return v, v.Browser, nil
	}
	executable, err := browserExecutable()
	if err != nil {
		return v, "", err
	}
	name = filepath.Base(executable)
	profile := filepath.Join(dir, "browser-"+strings.ReplaceAll(name, " ", "-"))
	if err := os.MkdirAll(profile, 0700); err != nil {
		return v, "", err
	}
	cmd := exec.Command(executable, "--user-data-dir="+profile, "--remote-debugging-address=127.0.0.1", "--remote-debugging-port=19229", "--no-first-run", "--no-default-browser-check", "about:blank")
	if err := cmd.Start(); err != nil {
		return v, "", fmt.Errorf("cannot launch %s: %w", executable, err)
	}
	go cmd.Wait()
	fmt.Fprintf(stderr, "Starting %s with profile %s\n", name, profile)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return v, "", errors.New("the browser did not open its debugging endpoint on " + debugAddress)
		case <-ticker.C:
			if debugJSON(ctx, "/json/version", &v) == nil {
				return v, name, nil
			}
		}
	}
}

// cdp is a DevTools connection to the browser; page commands carry a session id.
type cdp struct {
	ws *websocket.Conn
	id int
}

func dialBrowser(ctx context.Context, v browserVersion) (*cdp, error) {
	u, err := url.Parse(v.WebSocketDebuggerURL)
	if err != nil || u.Scheme != "ws" || u.Host != debugAddress || !strings.HasPrefix(u.Path, "/devtools/browser/") {
		return nil, errors.New("invalid local browser debugging endpoint")
	}
	ws, _, err := websocket.Dial(ctx, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to the browser: %w", err)
	}
	ws.SetReadLimit(16 << 20)
	return &cdp{ws: ws}, nil
}

func (c *cdp) call(ctx context.Context, session, method string, params any, result any) error {
	c.id++
	msg := map[string]any{"id": c.id, "method": method, "params": params}
	if session != "" {
		msg["sessionId"] = session
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if err := c.ws.Write(ctx, websocket.MessageText, data); err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	for {
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			return fmt.Errorf("%s: %w", method, err)
		}
		var reply struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &reply) != nil || reply.ID != c.id {
			continue // events
		}
		if reply.Error != nil {
			return fmt.Errorf("%s: %s", method, reply.Error.Message)
		}
		if result == nil {
			return nil
		}
		return json.Unmarshal(reply.Result, result)
	}
}

// tab is one page of the CLI's browser, driven through Runtime.evaluate.
type tab struct {
	c       *cdp
	session string
	Browser string
}

func (t *tab) close() { t.c.ws.CloseNow() }

type targetInfo struct {
	TargetID string `json:"targetId"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	URL      string `json:"url"`
}

func (c *cdp) pages(ctx context.Context) ([]targetInfo, error) {
	var targets struct {
		TargetInfos []targetInfo `json:"targetInfos"`
	}
	if err := c.call(ctx, "", "Target.getTargets", map[string]any{}, &targets); err != nil {
		return nil, err
	}
	var pages []targetInfo
	for _, t := range targets.TargetInfos {
		if t.Type == "page" {
			pages = append(pages, t)
		}
	}
	return pages, nil
}

// purchaseHost reports whether a page belongs to a Renfe purchase: Renfe's sale
// site or the Redsys payment pages it hands over to.
func purchaseHost(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return u.Host == saleHost || u.Host == "redsys.es" || strings.HasSuffix(u.Host, ".redsys.es")
}

func (c *cdp) attach(ctx context.Context, targetID string) (string, error) {
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := c.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": targetID, "flatten": true}, &attached); err != nil {
		return "", err
	}
	return attached.SessionID, nil
}

// openBookingTab copies the purchase session's cookies into the CLI's browser and opens
// the passenger-details step in a new tab.
func openBookingTab(ctx context.Context, dir string, jar *cookieJar, target string, stderr io.Writer) (*tab, error) {
	v, name, err := ensureBrowser(ctx, dir, stderr)
	if err != nil {
		return nil, err
	}
	c, err := dialBrowser(ctx, v)
	if err != nil {
		return nil, err
	}
	t := &tab{c: c, Browser: name}
	ok := false
	defer func() {
		if !ok {
			t.close()
		}
	}()

	// The new cookies replace the session of any earlier booking, whose tabs would then
	// show one purchase while acting on another. Close them, including payment pages.
	pages, err := c.pages(ctx)
	if err != nil {
		return nil, err
	}
	closed := 0
	for _, p := range pages {
		if purchaseHost(p.URL) {
			if err := c.call(ctx, "", "Target.closeTarget", map[string]any{"targetId": p.TargetID}, nil); err != nil {
				return nil, err
			}
			closed++
		}
	}
	if closed > 0 {
		fmt.Fprintf(stderr, "Closed %d earlier Renfe tab(s); this booking replaces their session.\n", closed)
	}

	var cookies []map[string]any
	for _, jc := range jar.all() {
		p := map[string]any{"name": jc.Name, "value": jc.Value, "path": jc.Path, "secure": jc.Secure, "httpOnly": jc.HttpOnly}
		if jc.hostOnly {
			p["url"] = "https://" + jc.Domain + jc.Path
		} else {
			p["domain"] = "." + jc.Domain
		}
		if !jc.Expires.IsZero() {
			p["expires"] = jc.Expires.Unix()
		}
		cookies = append(cookies, p)
	}
	if err := c.call(ctx, "", "Storage.setCookies", map[string]any{"cookies": cookies}, nil); err != nil {
		return nil, err
	}
	var created struct {
		TargetID string `json:"targetId"`
	}
	if err := c.call(ctx, "", "Target.createTarget", map[string]any{"url": target}, &created); err != nil {
		return nil, err
	}
	if t.session, err = c.attach(ctx, created.TargetID); err != nil {
		return nil, err
	}
	if _, err := t.waitTitle(ctx, "Datos Viajeros"); err != nil {
		return nil, err
	}
	ok = true
	return t, nil
}

// findTab attaches to the running CLI browser's tab whose title contains title.
func findTab(ctx context.Context, title string) (*tab, error) {
	var v browserVersion
	if err := debugJSON(ctx, "/json/version", &v); err != nil {
		return nil, errors.New("the renfe browser is not running")
	}
	c, err := dialBrowser(ctx, v)
	if err != nil {
		return nil, err
	}
	pages, err := c.pages(ctx)
	if err != nil {
		c.ws.CloseNow()
		return nil, err
	}
	var match []targetInfo
	for _, p := range pages {
		if purchaseHost(p.URL) && strings.Contains(p.Title, title) {
			match = append(match, p)
		}
	}
	if len(match) != 1 {
		c.ws.CloseNow()
		return nil, fmt.Errorf("expected one Renfe %q tab in the renfe browser, found %d", title, len(match))
	}
	t := &tab{c: c, Browser: v.Browser}
	if t.session, err = c.attach(ctx, match[0].TargetID); err != nil {
		t.close()
		return nil, err
	}
	return t, nil
}

// eval runs a page script and decodes its JSON-serialisable result into out.
func (t *tab) eval(ctx context.Context, script string, out any) error {
	var r struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := t.c.call(ctx, t.session, "Runtime.evaluate", map[string]any{"expression": script, "returnByValue": true, "awaitPromise": true}, &r); err != nil {
		return err
	}
	if r.ExceptionDetails != nil {
		msg := firstNonEmpty(r.ExceptionDetails.Exception.Description, r.ExceptionDetails.Text)
		return fmt.Errorf("page script failed: %s", strings.SplitN(msg, "\n", 2)[0])
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(r.Result.Value, out)
}

type pageState struct {
	Title  string   `json:"title"`
	Host   string   `json:"host"`
	Ready  bool     `json:"ready"`
	Errors []string `json:"errors"`
}

// pageStateJS reports the page title and any error Renfe or Redsys is showing:
// field errors (#error<field>), the error modal, and Redsys error boxes.
const pageStateJS = `(() => {
  const vis = e => !!(e.offsetWidth || e.offsetHeight || e.getClientRects().length) && getComputedStyle(e).visibility !== 'hidden';
  const errors = [];
  for (const e of document.querySelectorAll('[id^=error], #modalGeneric .modal-body, .alert-danger, .error, .msg-error, .errorMsg')) {
    const text = (e.innerText || '').trim().replace(/\s+/g, ' ');
    if (vis(e) && text && !errors.includes(text)) errors.push(text.slice(0, 300));
  }
  return {title: document.title, host: location.host, ready: document.readyState === 'complete', errors};
})()`

func (t *tab) state(ctx context.Context) (pageState, error) {
	var s pageState
	err := t.eval(ctx, pageStateJS, &s)
	return s, err
}

// waitTitle waits until the page title contains one of titles. Evaluation errors are
// expected while the page navigates and are retried until the context expires.
func (t *tab) waitTitle(ctx context.Context, titles ...string) (pageState, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var last pageState
	for {
		if s, err := t.state(ctx); err == nil {
			last = s
			for _, title := range titles {
				if s.Ready && strings.Contains(s.Title, title) {
					return s, nil
				}
			}
		}
		select {
		case <-ctx.Done():
			msg := fmt.Sprintf("timed out waiting for Renfe's %q page (now on %q)", strings.Join(titles, `" or "`), last.Title)
			if len(last.Errors) > 0 {
				msg += ": " + strings.Join(last.Errors, "; ")
			}
			return last, errors.New(msg)
		case <-ticker.C:
		}
	}
}

// clickAndWait clicks a button and waits for the next step's page. If the page stays put
// and shows errors (e.g. a rejected document number), it fails with Renfe's messages.
func (t *tab) clickAndWait(ctx context.Context, buttonID string, timeout time.Duration, titles ...string) (pageState, error) {
	before, err := t.state(ctx)
	if err != nil {
		return before, err
	}
	script := fmt.Sprintf(`(() => { const b = document.getElementById(%q); if (!b) return false; b.click(); return true; })()`, buttonID)
	var clicked bool
	if err := t.eval(ctx, script, &clicked); err != nil {
		return before, err
	}
	if !clicked {
		return before, fmt.Errorf("Renfe's %q page has no %s button; the website may have changed", before.Title, buttonID)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	stuck := 0
	last := before
	for {
		select {
		case <-ctx.Done():
			msg := fmt.Sprintf("timed out waiting for Renfe's %q page after %s (now on %q)", strings.Join(titles, `" or "`), buttonID, last.Title)
			if len(last.Errors) > 0 {
				msg += ": " + strings.Join(last.Errors, "; ")
			}
			return last, errors.New(msg)
		case <-ticker.C:
		}
		s, err := t.state(ctx)
		if err != nil {
			continue // navigating
		}
		last = s
		for _, title := range titles {
			if s.Ready && strings.Contains(s.Title, title) {
				return s, nil
			}
		}
		if s.Title == before.Title && len(s.Errors) > 0 {
			if stuck++; stuck >= 4 { // errors persisted for two seconds without navigation
				return s, fmt.Errorf("Renfe rejected the %q step: %s", before.Title, strings.Join(s.Errors, "; "))
			}
		}
	}
}
