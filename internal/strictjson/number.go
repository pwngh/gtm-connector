package strictjson

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// literal is a JSON number literal read as sign, coefficient and exponent: the value is
// coefficient × 10^exponent. The coefficient has no leading zeros and is "0" for zero.
type literal struct {
	negative    bool
	coefficient string
	exponent    *big.Int
}

// readLiteral splits a well-formed JSON number literal. The exponent is exact however many digits it has.
func readLiteral(text string) literal {
	var l literal
	if strings.HasPrefix(text, "-") {
		l.negative, text = true, text[1:]
	}
	mantissa, exponent, _ := strings.Cut(strings.ReplaceAll(text, "E", "e"), "e")
	whole, fraction, _ := strings.Cut(mantissa, ".")
	l.coefficient = strings.TrimLeft(whole+fraction, "0")
	if l.coefficient == "" {
		l.coefficient = "0"
	}
	l.exponent = new(big.Int)
	if exponent != "" {
		l.exponent.SetString(strings.TrimPrefix(exponent, "+"), 10)
	}
	l.exponent.Sub(l.exponent, big.NewInt(int64(len(fraction))))
	return l
}

// parseFloat is float(text) for a well-formed JSON number literal, correctly rounded like CPython's.
// strconv.ParseFloat alone stops counting an exponent past 10,000, so a literal whose digits and exponent
// cancel (thousands of leading zeros under a large exponent) is first rewritten as 0.digits × 10^point.
func parseFloat(text string) float64 {
	l := readLiteral(text)
	zero := 0.0
	if l.negative {
		zero = math.Copysign(0, -1)
	}
	if l.coefficient == "0" {
		return zero
	}
	// The value lies in [10^(point-1), 10^point).
	point := new(big.Int).Add(l.exponent, big.NewInt(int64(len(l.coefficient))))
	switch {
	case point.Cmp(big.NewInt(310)) > 0:
		return math.Copysign(math.Inf(1), zero)
	case point.Cmp(big.NewInt(-330)) < 0:
		return zero
	}
	sign := ""
	if l.negative {
		sign = "-"
	}
	value, _ := strconv.ParseFloat(sign+"0."+l.coefficient+"e"+point.String(), 64)
	return value
}

// Decimal is a number kept exactly, as Python's decimal.Decimal holds it: coefficient × 10^Exponent, with
// the scale the text gave ("1.50" keeps its zero).
type Decimal struct {
	Negative    bool
	Coefficient string // decimal digits with no leading zeros; "0" for zero
	Exponent    int64
}

// The exponents decimal.Decimal accepts from text: the adjusted exponent at most Emax, the exponent at
// least Etiny.
var (
	decimalEmax  = big.NewInt(999_999_999_999_999_999)
	decimalEtiny = big.NewInt(-1_999_999_999_999_999_997)
)

// ParseDecimal is decimal.Decimal(text) for a JSON number literal. An exponent out of Decimal's range is
// refused with InvalidOperation, as Python refuses it.
func ParseDecimal(text string) (Decimal, error) {
	l := readLiteral(text)
	adjusted := new(big.Int).Add(l.exponent, big.NewInt(int64(len(l.coefficient)-1)))
	if adjusted.Cmp(decimalEmax) > 0 || l.exponent.Cmp(decimalEtiny) < 0 {
		return Decimal{}, &Error{"InvalidOperation", "[<class 'decimal.InvalidOperation'>]"}
	}
	return Decimal{Negative: l.negative, Coefficient: l.coefficient, Exponent: l.exponent.Int64()}, nil
}

func (d Decimal) sign() string {
	if d.Negative {
		return "-"
	}
	return ""
}

// digits places the decimal point dot digits into the coefficient.
func (d Decimal) digits(dot int64) string {
	n := int64(len(d.Coefficient))
	switch {
	case dot <= 0:
		return "0." + strings.Repeat("0", int(-dot)) + d.Coefficient
	case dot >= n:
		return d.Coefficient + strings.Repeat("0", int(dot-n))
	default:
		return d.Coefficient[:dot] + "." + d.Coefficient[dot:]
	}
}

// String is str(Decimal): plain notation when the exponent is at most 0 and the number is not too small,
// scientific otherwise.
func (d Decimal) String() string {
	left := d.Exponent + int64(len(d.Coefficient))
	if d.Exponent <= 0 && left > -6 {
		return d.sign() + d.digits(left)
	}
	text := d.sign() + d.digits(1)
	if left != 1 {
		text += fmt.Sprintf("E%+d", left-1)
	}
	return text
}

// Fixed is format(Decimal, "f"): every digit in fixed point, with the scale kept. Its length grows with the
// exponent, so a caller bounds the exponent first, as the Salesforce adapter does.
func (d Decimal) Fixed() string {
	if d.Coefficient == "0" && d.Exponent > 0 {
		return d.sign() + "0"
	}
	return d.sign() + d.digits(d.Exponent+int64(len(d.Coefficient)))
}
