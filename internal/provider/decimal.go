package provider

import (
	"errors"
	"strconv"
	"strings"
)

const (
	maxCanonicalDecimalDigits = 128
	maxDecimalInputBytes      = 4096
)

var (
	errDecimalSyntax      = errors.New("decimal must be a JSON number")
	errDecimalNonPositive = errors.New("decimal must be greater than zero")
	errDecimalTooLong     = errors.New("decimal exceeds 128 digits")
	errDecimalInputLimit  = errors.New("decimal input exceeds limit")
	errCentsNonPositive   = errors.New("cents must be greater than zero")
)

func CanonicalDecimal(raw string) (string, error) {
	if len(raw) == 0 {
		return "", errDecimalSyntax
	}
	if len(raw) > maxDecimalInputBytes {
		return "", errDecimalInputLimit
	}

	position := 0
	negative := false
	if raw[position] == '-' {
		negative = true
		position++
		if position == len(raw) {
			return "", errDecimalSyntax
		}
	}

	integerStart := position
	if raw[position] == '0' {
		position++
		if position < len(raw) && isASCIIDigit(raw[position]) {
			return "", errDecimalSyntax
		}
	} else if raw[position] >= '1' && raw[position] <= '9' {
		for position < len(raw) && isASCIIDigit(raw[position]) {
			position++
		}
	} else {
		return "", errDecimalSyntax
	}
	integerEnd := position

	fractionStart := position
	fractionEnd := position
	if position < len(raw) && raw[position] == '.' {
		position++
		fractionStart = position
		for position < len(raw) && isASCIIDigit(raw[position]) {
			position++
		}
		if position == fractionStart {
			return "", errDecimalSyntax
		}
		fractionEnd = position
	}

	exponent := 0
	if position < len(raw) && (raw[position] == 'e' || raw[position] == 'E') {
		position++
		exponentNegative := false
		if position < len(raw) && (raw[position] == '+' || raw[position] == '-') {
			exponentNegative = raw[position] == '-'
			position++
		}
		exponentStart := position
		for position < len(raw) && isASCIIDigit(raw[position]) {
			if exponent <= maxDecimalInputBytes {
				exponent = exponent*10 + int(raw[position]-'0')
			}
			position++
		}
		if position == exponentStart {
			return "", errDecimalSyntax
		}
		if exponent > maxDecimalInputBytes {
			return "", errDecimalTooLong
		}
		if exponentNegative {
			exponent = -exponent
		}
	}
	if position != len(raw) {
		return "", errDecimalSyntax
	}
	if negative {
		return "", errDecimalNonPositive
	}

	var significand strings.Builder
	significand.Grow(integerEnd - integerStart + fractionEnd - fractionStart)
	significand.WriteString(raw[integerStart:integerEnd])
	significand.WriteString(raw[fractionStart:fractionEnd])
	digits := significand.String()

	leadingZeros := 0
	for leadingZeros < len(digits) && digits[leadingZeros] == '0' {
		leadingZeros++
	}
	if leadingZeros == len(digits) {
		return "", errDecimalNonPositive
	}

	decimalPosition := integerEnd - integerStart + exponent - leadingZeros
	digits = digits[leadingZeros:]
	for len(digits) > decimalPosition && digits[len(digits)-1] == '0' {
		digits = digits[:len(digits)-1]
	}

	digitCount := len(digits)
	if decimalPosition <= 0 {
		digitCount = 1 - decimalPosition + len(digits)
	} else if decimalPosition >= len(digits) {
		digitCount = decimalPosition
	}
	if digitCount > maxCanonicalDecimalDigits {
		return "", errDecimalTooLong
	}

	var canonical strings.Builder
	canonical.Grow(digitCount + 1)
	switch {
	case decimalPosition <= 0:
		canonical.WriteString("0.")
		canonical.WriteString(strings.Repeat("0", -decimalPosition))
		canonical.WriteString(digits)
	case decimalPosition >= len(digits):
		canonical.WriteString(digits)
		canonical.WriteString(strings.Repeat("0", decimalPosition-len(digits)))
	default:
		canonical.WriteString(digits[:decimalPosition])
		canonical.WriteByte('.')
		canonical.WriteString(digits[decimalPosition:])
	}
	return canonical.String(), nil
}

func USDFromCents(cents int64) (string, error) {
	if cents <= 0 {
		return "", errCentsNonPositive
	}

	dollars := cents / 100
	remainder := cents % 100
	raw := strconv.FormatInt(dollars, 10) + "."
	if remainder < 10 {
		raw += "0"
	}
	raw += strconv.FormatInt(remainder, 10)
	return CanonicalDecimal(raw)
}

func isASCIIDigit(value byte) bool {
	return value >= '0' && value <= '9'
}
