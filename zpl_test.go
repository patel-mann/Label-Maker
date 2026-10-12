package main

import (
	"strings"
	"testing"
)

func TestNormalizeTemplateRoundsFOCoordinates(t *testing.T) {
	got := normalizeTemplate("^FO10.4,20.6^FDx^FS")
	if want := "^FO10,21^FDx^FS"; got != want {
		t.Errorf("normalizeTemplate() = %q, want %q", got, want)
	}
}

func TestNormalizeTemplateKeepsEAN13(t *testing.T) {
	in := "^BE0,100,Y,N"
	if got := normalizeTemplate(in); got != in {
		t.Errorf("normalizeTemplate() = %q, want %q (^BE must not be rewritten)", got, in)
	}
}

func TestUpcData(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"empty is allowed", "", "", false},
		{"eleven digits", "03600029145", "03600029145", false},
		{"twelve with valid check digit", "036000291452", "03600029145", false},
		{"twelve with bad check digit", "036000291453", "", true},
		{"too short", "12345", "", true},
		{"strips non-digits", "0360-0029-145", "03600029145", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := upcData(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("upcData(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("upcData(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

const barcodeTemplate = "^XA\n^FO10.6,20.2^BUN,80,Y,N,Y^FD${BARCODE}^FS\n^FO5,5^FD${NAME}^FS\n^XZ"

func TestRenderZPLOmitsBarcodeFieldWhenEmpty(t *testing.T) {
	zpl, err := renderZPL(barcodeTemplate, Label{Name: "Widget", Qty: 1})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(zpl, "BARCODE") || strings.Contains(zpl, "^BU") {
		t.Errorf("empty barcode should omit the field, got:\n%s", zpl)
	}
	if !strings.Contains(zpl, "^FDWidget^FS") {
		t.Errorf("name field should survive, got:\n%s", zpl)
	}
}

func TestRenderZPLKeepsBarcodeFieldWhenGiven(t *testing.T) {
	zpl, err := renderZPL(barcodeTemplate, Label{Barcode: "03600029145", Qty: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(zpl, "^BU") || !strings.Contains(zpl, "03600029145") {
		t.Errorf("barcode should be present, got:\n%s", zpl)
	}
}

func TestRenderZPLKeepsFixedBarcodeField(t *testing.T) {
	tpl := "^XA\n^FO1,1^BQN,2,3^FDMA,hello^FS\n^XZ"
	zpl, err := renderZPL(tpl, Label{Qty: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(zpl, "^BQN,2,3^FDMA,hello^FS") {
		t.Errorf("a fixed barcode field must not be removed, got:\n%s", zpl)
	}
}

func TestRenderZPLDoesNotRewriteEAN13(t *testing.T) {
	tpl := "^XA\n^FO1,1^BE0,100,Y,N^FD${BARCODE}^FS\n^XZ"
	zpl, err := renderZPL(tpl, Label{Barcode: "03600029145", Qty: 1})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(zpl, "^BU") {
		t.Errorf("^BE must not be converted to ^BU, got:\n%s", zpl)
	}
	if !strings.Contains(zpl, "^BE0,100,Y,N") {
		t.Errorf("^BE should remain, got:\n%s", zpl)
	}
}
