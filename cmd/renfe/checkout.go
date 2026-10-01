package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// Page scripts are functions called with JSON arguments, so passenger values are
// data, never code. They set values the way typing would, firing the events the
// website's jQuery validation listens to.
const setFieldJS = `const setField = (id, v) => {
  const e = document.getElementById(id);
  if (!e) return false;
  e.focus(); e.value = v;
  for (const t of ['input', 'keyup', 'change', 'blur']) e.dispatchEvent(new Event(t, {bubbles: true}));
  return true;
};
const setPhone = (selectID, inputID, phone) => {
  const sel = document.getElementById(selectID);
  if (!sel) return false;
  const prefix = [...sel.options].map(o => o.value).filter(v => v && phone.startsWith(v)).sort((a, b) => b.length - a.length)[0];
  if (!prefix) return false;
  setField(selectID, prefix); // the prefix's change handler clears the number, so set it first
  return setField(inputID, phone.slice(prefix.length));
};`

const fillPassengersJS = `(ps) => {` + setFieldJS + `
  const count = Number((document.getElementById('nviajeros') || {}).value);
  if (count !== ps.length) return {error: 'Renfe expects ' + count + ' passengers, the booking has ' + ps.length};
  const missing = [];
  const need = (ok, what) => { if (!ok) missing.push(what); };
  ps.forEach((p, i) => {
    need(setField('nombre' + i, p.name), 'name of passenger ' + (i + 1));
    need(setField('apellido1' + i, p.surname1), 'surname1 of passenger ' + (i + 1));
    setField('apellido2' + i, p.surname2);
    if (p.documentCode) {
      need(setField('tipoDocumento' + i, p.documentCode), 'document type of passenger ' + (i + 1));
      need(setField('documento' + i, p.document), 'document of passenger ' + (i + 1));
    }
    if (p.email) need(setField('email' + i, p.email) || i > 0, 'email of passenger ' + (i + 1));
    if (p.phone) need(setPhone('prefijo' + i, 'telefono' + i, p.phone) || i > 0, 'phone of passenger ' + (i + 1));
  });
  return {missing};
}`

// preparePaymentJS fills the buyer (when given), picks Bizum and accepts Renfe's purchase
// conditions, then reports the total. It never presses the pay button.
const preparePaymentJS = `async (buyer) => {` + setFieldJS + `
  const vis = e => !!(e && (e.offsetWidth || e.offsetHeight || e.getClientRects().length));
  if (buyer) {
    if (!setField('inputEmail', buyer.email) || !setPhone('prefijoComprador', 'telefonoComprador', buyer.phone))
      return {error: 'cannot fill the buyer email and phone on the payment page'};
  }
  const bizum = document.getElementById('datosPago_cdgoFormaPago_bizum');
  if (!vis(bizum)) return {error: 'Renfe does not offer Bizum for this purchase'};
  if (!bizum.checked) bizum.click();
  const terms = document.getElementById('aceptarCondiciones');
  if (!terms) return {error: 'cannot find the purchase conditions checkbox'};
  if (!terms.checked) terms.click();
  await new Promise(r => setTimeout(r, 2000)); // choosing a method recalculates the price
  const button = document.getElementById('butonPagar');
  return {
    cents: Number((document.getElementById('precioTotalInfo') || {}).value),
    method: (document.getElementById('datosPago_subCdgoFormaPago') || {}).value,
    enabled: !!button && !button.disabled,
    invalid: [...document.querySelectorAll('.invalid')].map(e => e.id),
  };
}`

// redsysAmountJS reads the amount on Redsys's Bizum page before anything is entered.
const redsysAmountJS = `() => {
  if (!document.getElementById('iPhBizInit') || !document.getElementById('bBizInit')) return {error: 'unexpected Redsys page: ' + document.title};
  const m = document.body.innerText.match(/Detalle del pago:\s*([0-9.,]+)\s*€/);
  return {amount: m ? m[1] : ''};
}`

const redsysPhoneJS = `(phone) => {` + setFieldJS + `
  if (!setField('iPhBizInit', phone)) return false;
  document.getElementById('bBizInit').click();
  return true;
}`

// confirmationJS reads the booking locator and the ticket PDF link (the hidden #rutaPDF
// behind "Descárgalos en PDF") from Renfe's confirmation page.
const confirmationJS = `() => {
  const text = document.body.innerText;
  // Locators are short upper-case codes; lower-case words between label and code are skipped.
  const m = text.match(/[Ll]ocalizador[^A-Z0-9]{0,60}?\b([A-Z0-9]{5,8})\b/);
  const pdf = document.getElementById('rutaPDF');
  return {confirmed: !!m, locator: m ? m[1] : '', ticketURL: pdf ? pdf.value : ''};
}`

type confirmation struct {
	Confirmed bool   `json:"confirmed"`
	Locator   string `json:"locator"`
	TicketURL string `json:"ticketURL"`
}

func jsCall(fn string, args ...any) string {
	parts := make([]string, len(args))
	for i, a := range args {
		data, err := json.Marshal(a)
		if err != nil {
			panic(err)
		}
		parts[i] = string(data)
	}
	return "(" + fn + ")(" + strings.Join(parts, ",") + ")"
}

type formPassenger struct {
	Name         string `json:"name"`
	Surname1     string `json:"surname1"`
	Surname2     string `json:"surname2"`
	DocumentCode string `json:"documentCode"`
	Document     string `json:"document"`
	Email        string `json:"email"`
	Phone        string `json:"phone"`
}

// fillPassengers completes "Datos Viajeros" and continues to the payment page,
// skipping the optional extras step.
func fillPassengers(ctx context.Context, t *tab, ps []passenger) error {
	form := make([]formPassenger, len(ps))
	for i, p := range ps {
		form[i] = formPassenger{Name: p.Name, Surname1: p.Surname1, Surname2: p.Surname2, DocumentCode: documentCodes[strings.ToLower(p.DocumentType)], Document: p.Document, Email: p.Email, Phone: p.Phone}
	}
	var r struct {
		Error   string   `json:"error"`
		Missing []string `json:"missing"`
	}
	if err := t.eval(ctx, jsCall(fillPassengersJS, form), &r); err != nil {
		return err
	}
	if r.Error != "" {
		return errors.New(r.Error)
	}
	if len(r.Missing) > 0 {
		return fmt.Errorf("Renfe's passenger form has no field for: %s", strings.Join(r.Missing, ", "))
	}
	s, err := t.clickAndWait(ctx, "submitpersonaliza", 45*time.Second, "PERSONALIZA", "Método de pago")
	if err != nil {
		return err
	}
	if strings.Contains(s.Title, "PERSONALIZA") {
		if _, err := t.clickAndWait(ctx, "submitFormaPago", 45*time.Second, "Método de pago"); err != nil {
			return err
		}
	}
	return nil
}

// preparePayment selects Bizum on the payment page and returns the total in cents.
// buyer may be nil when the page was already filled.
func preparePayment(ctx context.Context, t *tab, buyer *passenger) (int, error) {
	var arg any
	if buyer != nil {
		arg = map[string]string{"email": buyer.Email, "phone": buyer.Phone}
	}
	var r struct {
		Error   string   `json:"error"`
		Cents   int      `json:"cents"`
		Method  string   `json:"method"`
		Enabled bool     `json:"enabled"`
		Invalid []string `json:"invalid"`
	}
	if err := t.eval(ctx, jsCall(preparePaymentJS, arg), &r); err != nil {
		return 0, err
	}
	switch {
	case r.Error != "":
		return 0, errors.New(r.Error)
	case len(r.Invalid) > 0:
		return 0, fmt.Errorf("Renfe marked these payment fields invalid: %s", strings.Join(r.Invalid, ", "))
	case r.Method != "05": // Redsys sub-method code of the Bizum radio button
		return 0, fmt.Errorf("Bizum is not selected (payment method %q)", r.Method)
	case !r.Enabled:
		s, _ := t.state(ctx)
		return 0, fmt.Errorf("Renfe's pay button is disabled: %s", strings.Join(s.Errors, "; "))
	case r.Cents <= 0:
		return 0, errors.New("cannot read the total from Renfe's payment page")
	}
	return r.Cents, nil
}

type payment struct {
	Status    string // confirmed or payment_pending
	Locator   string
	TicketURL string
}

// payBizum submits the payment page and starts a Bizum request on Redsys, then waits for
// the buyer to approve it in their bank app. Both Renfe's and Redsys's totals must be at
// most maxCents before anything is submitted.
func payBizum(ctx context.Context, t *tab, cents, maxCents int, bizumPhone string, wait time.Duration) (payment, error) {
	if cents > maxCents {
		return payment{}, fmt.Errorf("total %s EUR is above --max-total %s; nothing was paid", euros(cents), euros(maxCents))
	}
	national, ok := strings.CutPrefix(bizumPhone, "+34")
	if !ok || len(national) != 9 {
		return payment{}, errors.New("bizum_phone must be a Spanish number such as +34600000000")
	}
	if _, err := t.clickAndWait(ctx, "butonPagar", 60*time.Second, "Pago Bizum"); err != nil {
		return payment{}, err
	}
	var page struct {
		Error  string `json:"error"`
		Amount string `json:"amount"`
	}
	if err := t.eval(ctx, jsCall(redsysAmountJS), &page); err != nil {
		return payment{}, err
	}
	if page.Error != "" {
		return payment{}, errors.New(page.Error)
	}
	redsysEUR, ok := parseEuro(page.Amount)
	if !ok || int(math.Round(redsysEUR*100)) != cents {
		return payment{}, fmt.Errorf("Redsys shows %q instead of %s EUR; nothing was paid", page.Amount, euros(cents))
	}
	var submitted bool
	if err := t.eval(ctx, jsCall(redsysPhoneJS, national), &submitted); err != nil {
		return payment{}, err
	}
	if !submitted {
		return payment{}, errors.New("cannot enter the Bizum phone on Redsys's page")
	}

	deadline := time.Now().Add(wait)
	stuck := 0
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		s, err := t.state(ctx)
		if err != nil {
			continue // navigating
		}
		if s.Host == saleHost && s.Ready {
			var c confirmation
			if err := t.eval(ctx, jsCall(confirmationJS), &c); err == nil && c.Confirmed {
				return payment{Status: "confirmed", Locator: c.Locator, TicketURL: c.TicketURL}, nil
			}
			if len(s.Errors) > 0 {
				return payment{}, fmt.Errorf("Renfe reported after payment: %s", strings.Join(s.Errors, "; "))
			}
		}
		if s.Host != saleHost && len(s.Errors) > 0 {
			if stuck++; stuck >= 3 {
				return payment{}, fmt.Errorf("Redsys reported: %s", strings.Join(s.Errors, "; "))
			}
		}
	}
	return payment{Status: "payment_pending"}, nil
}

func euros(cents int) string { return fmt.Sprintf("%d.%02d", cents/100, cents%100) }
