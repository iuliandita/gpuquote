package provider

import (
	"strings"
	"testing"
)

func TestCanonicalDecimal(t *testing.T) {
	oneE127 := "1" + strings.Repeat("0", 127)
	oneEMinus127 := "0." + strings.Repeat("0", 126) + "1"

	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "fraction", raw: "0.35", want: "0.35"},
		{name: "trailing fractional zeros", raw: "15.9200", want: "15.92"},
		{name: "negative exponent", raw: "7.43999984115362e-3", want: "0.00743999984115362"},
		{name: "positive exponent", raw: "1e2", want: "100"},
		{name: "zero", raw: "0", wantErr: true},
		{name: "negative", raw: "-1", wantErr: true},
		{name: "NaN", raw: "NaN", wantErr: true},
		{name: "infinity", raw: "Inf", wantErr: true},
		{name: "fraction expression", raw: "1/2", wantErr: true},
		{name: "maximum positive exponent", raw: "1e127", want: oneE127},
		{name: "positive exponent too large", raw: "1e128", wantErr: true},
		{name: "maximum negative exponent", raw: "1e-127", want: oneEMinus127},
		{name: "negative exponent too large", raw: "1e-128", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := CanonicalDecimal(test.raw)
			if test.wantErr {
				if err == nil {
					t.Fatalf("CanonicalDecimal() error = nil, want non-nil")
				}
				if got != "" {
					t.Errorf("CanonicalDecimal() = %q on error, want empty", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("CanonicalDecimal() error = %v, want nil", err)
			}
			if got != test.want {
				t.Errorf("CanonicalDecimal() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCanonicalDecimalRejectsInvalidJSONNumberGrammar(t *testing.T) {
	invalid := []string{
		"", "+1", ".5", "1.", "01", "00.1", "1e", "1e+", " 1", "1 ",
	}

	for _, raw := range invalid {
		if got, err := CanonicalDecimal(raw); err == nil || got != "" {
			t.Errorf("CanonicalDecimal(%q) = %q, %v; want empty, non-nil", raw, got, err)
		}
	}
}

func TestCanonicalDecimalErrorsAreStableAndDoNotEchoInput(t *testing.T) {
	const first = "private-invalid-value"
	const second = "another-invalid-value"

	_, firstErr := CanonicalDecimal(first)
	_, secondErr := CanonicalDecimal(second)
	if firstErr == nil || secondErr == nil {
		t.Fatal("CanonicalDecimal() error = nil, want non-nil")
	}
	if firstErr.Error() != secondErr.Error() {
		t.Errorf("errors differ: %q != %q", firstErr, secondErr)
	}
	if strings.Contains(firstErr.Error(), first) || strings.Contains(secondErr.Error(), second) {
		t.Fatal("CanonicalDecimal() error echoed caller input")
	}
}

func TestCanonicalDecimalBoundsInputBeforeExpansion(t *testing.T) {
	raw := "1e" + strings.Repeat("9", 1_000)
	if got, err := CanonicalDecimal(raw); err == nil || got != "" {
		t.Fatalf("CanonicalDecimal() = %q, %v; want empty, non-nil", got, err)
	}
}

func TestUSDFromCents(t *testing.T) {
	tests := []struct {
		name    string
		cents   int64
		want    string
		wantErr bool
	}{
		{name: "dollars and cents", cents: 1592, want: "15.92"},
		{name: "one cent", cents: 1, want: "0.01"},
		{name: "zero", cents: 0, wantErr: true},
		{name: "negative", cents: -1, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := USDFromCents(test.cents)
			if test.wantErr {
				if err == nil {
					t.Fatalf("USDFromCents() error = nil, want non-nil")
				}
				if got != "" {
					t.Errorf("USDFromCents() = %q on error, want empty", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("USDFromCents() error = %v, want nil", err)
			}
			if got != test.want {
				t.Errorf("USDFromCents() = %q, want %q", got, test.want)
			}
		})
	}
}
