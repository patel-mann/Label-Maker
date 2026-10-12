package main

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	reFO  = regexp.MustCompile(`\^FO([\d.]+),([\d.]+)`)
	reVar = regexp.MustCompile(`\$\{(\w+)\}`)
	rePQ  = regexp.MustCompile(`\^PQ[^\^]*`)
	ctl   = strings.NewReplacer("^", "", "~", "")

	// reBarcodeField matches a ZPL barcode field whose data is the ${BARCODE} variable:
	// optional ^FO/^FT origin, optional ^BY defaults, the ^B<type> command, then ^FD...^FS.
	reBarcodeField = regexp.MustCompile(`(?:\^F[OT][^\^]*)?(?:\^BY[^\^]*)?\^B[A-Z0-9][^\^]*\^FD[^\^]*\$\{BARCODE\}[^\^]*\^FS`)
)

// Label is everything needed to fill one template.
type Label struct {
	Name, Origin, Ingredients, Barcode string
	Weight, WeightUnit, Price          string
	Prep, Exp, Batch                   string // Prep/Exp are YYYY-MM-DD
	Qty                                int
}

// normalizeTemplate: LF line endings and integer ^FO coordinates. Templates are authored
// as UPC-A (^BU); EAN-13 (^BE) is no longer rewritten.
func normalizeTemplate(t string) string {
	t = strings.ReplaceAll(t, "\r\n", "\n")
	return reFO.ReplaceAllStringFunc(t, func(m string) string {
		s := reFO.FindStringSubmatch(m)
		x, _ := strconv.ParseFloat(s[1], 64)
		y, _ := strconv.ParseFloat(s[2], 64)
		return fmt.Sprintf("^FO%d,%d", int(math.Round(x)), int(math.Round(y)))
	})
}

func oneLine(s string) string { return strings.Join(strings.Fields(ctl.Replace(s)), " ") }
func multiLine(s string) string { // \& is a line break inside ^FB
	s = strings.ReplaceAll(strings.TrimSpace(ctl.Replace(s)), "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", `\&`)
}

func mdy(iso string) string {
	t, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return ""
	}
	return t.Format("01/02/2006")
}

func digits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func upcCheck(d11 string) int {
	sum := 0
	for i := 0; i < 11; i++ {
		n := int(d11[i] - '0')
		if i%2 == 0 {
			n *= 3
		}
		sum += n
	}
	return (10 - sum%10) % 10
}

// upcData returns the 11 digits ^BU wants (it adds the check digit itself).
func upcData(raw string) (string, error) {
	d := digits(raw)
	switch len(d) {
	case 0:
		return "", nil
	case 11:
		return d, nil
	case 12:
		if upcCheck(d[:11]) != int(d[11]-'0') {
			return "", fmt.Errorf("UPC check digit looks wrong: expected %d for %s", upcCheck(d[:11]), d[:11])
		}
		return d[:11], nil
	}
	return "", errors.New("UPC-A needs 11 digits (or 12 including the check digit)")
}

func renderZPL(tpl string, l Label) (string, error) {
	if l.Qty < 1 || l.Qty > 999 {
		return "", errors.New("quantity must be 1-999")
	}
	bc, err := upcData(l.Barcode)
	if err != nil {
		return "", err
	}
	vals := map[string]string{
		"NAME": oneLine(l.Name), "ORIGIN": oneLine(l.Origin), "INGREDIENTS": multiLine(l.Ingredients),
		"BARCODE": bc, "WEIGHT": oneLine(l.Weight), "WEIGHT_UNIT": oneLine(l.WeightUnit),
		"PRICE": oneLine(l.Price), "PREP_MDY": mdy(l.Prep), "EXP_MDY": mdy(l.Exp), "BATCH_NO": oneLine(l.Batch),
	}
	z := normalizeTemplate(tpl)
	if bc == "" { // no UPC-A supplied: drop the barcode field instead of printing an empty symbol
		z = reBarcodeField.ReplaceAllString(z, "")
	}
	z = reVar.ReplaceAllStringFunc(z, func(m string) string {
		k := reVar.FindStringSubmatch(m)[1]
		if v, ok := vals[k]; ok {
			return v
		}
		return m
	})
	z = rePQ.ReplaceAllString(z, "")
	end := strings.LastIndex(z, "^XZ")
	if end < 0 {
		return "", errors.New("template has no ^XZ")
	}
	return z[:end] + fmt.Sprintf("^PQ%d,0,1,N\n", l.Qty) + z[end:], nil
}
