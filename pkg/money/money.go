// Package money provides precise decimal handling for financial operations.
// Uses shopspring/decimal internally to avoid floating point errors.
package money

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
)

// Amount represents a monetary value with precise decimal handling.
// It wraps shopspring/decimal for safe financial calculations.
type Amount struct {
	value    decimal.Decimal
	currency Currency
}

// Currency represents ISO 4217 currency codes
type Currency string

const (
	USD Currency = "USD"
	EUR Currency = "EUR"
	GBP Currency = "GBP"
	RUB Currency = "RUB"
	BTC Currency = "BTC"
	ETH Currency = "ETH"
)

var (
	ErrNegativeAmount    = errors.New("amount cannot be negative")
	ErrInsufficientFunds = errors.New("insufficient funds")
	ErrCurrencyMismatch  = errors.New("currency mismatch")
	ErrInvalidAmount     = errors.New("invalid amount format")
)

// Zero returns zero amount for given currency
func Zero(currency Currency) Amount {
	return Amount{
		value:    decimal.Zero,
		currency: currency,
	}
}

// New creates a new Amount from string representation
func New(value string, currency Currency) (Amount, error) {
	d, err := decimal.NewFromString(value)
	if err != nil {
		return Amount{}, fmt.Errorf("%w: %s", ErrInvalidAmount, value)
	}
	if d.IsNegative() {
		return Amount{}, ErrNegativeAmount
	}
	return Amount{value: d, currency: currency}, nil
}

// MustNew creates Amount or panics - use only in tests
func MustNew(value string, currency Currency) Amount {
	a, err := New(value, currency)
	if err != nil {
		panic(err)
	}
	return a
}

// FromDecimal creates Amount from decimal.Decimal
func FromDecimal(d decimal.Decimal, currency Currency) (Amount, error) {
	if d.IsNegative() {
		return Amount{}, ErrNegativeAmount
	}
	return Amount{value: d, currency: currency}, nil
}

// FromCents creates Amount from cents/smallest currency unit
func FromCents(cents int64, currency Currency) Amount {
	d := decimal.NewFromInt(cents).Div(decimal.NewFromInt(100))
	return Amount{value: d, currency: currency}
}

// Value returns the decimal value
func (a Amount) Value() decimal.Decimal {
	return a.value
}

// Currency returns the currency
func (a Amount) Currency() Currency {
	return a.currency
}

// String returns formatted string representation
func (a Amount) String() string {
	return fmt.Sprintf("%s %s", a.value.StringFixed(2), a.currency)
}

// StringValue returns just the numeric value as string
func (a Amount) StringValue() string {
	return a.value.StringFixed(2)
}

// IsZero returns true if amount is zero
func (a Amount) IsZero() bool {
	return a.value.IsZero()
}

// IsPositive returns true if amount > 0
func (a Amount) IsPositive() bool {
	return a.value.IsPositive()
}

// Cents returns amount in smallest currency unit
func (a Amount) Cents() int64 {
	return a.value.Mul(decimal.NewFromInt(100)).IntPart()
}

// Add returns sum of two amounts
func (a Amount) Add(other Amount) (Amount, error) {
	if a.currency != other.currency {
		return Amount{}, ErrCurrencyMismatch
	}
	return Amount{
		value:    a.value.Add(other.value),
		currency: a.currency,
	}, nil
}

// Sub returns difference of two amounts
func (a Amount) Sub(other Amount) (Amount, error) {
	if a.currency != other.currency {
		return Amount{}, ErrCurrencyMismatch
	}
	result := a.value.Sub(other.value)
	if result.IsNegative() {
		return Amount{}, ErrInsufficientFunds
	}
	return Amount{
		value:    result,
		currency: a.currency,
	}, nil
}

// Mul multiplies amount by a factor
func (a Amount) Mul(factor decimal.Decimal) Amount {
	return Amount{
		value:    a.value.Mul(factor),
		currency: a.currency,
	}
}

// MulInt multiplies amount by an integer
func (a Amount) MulInt(factor int64) Amount {
	return Amount{
		value:    a.value.Mul(decimal.NewFromInt(factor)),
		currency: a.currency,
	}
}

// Div divides amount by a factor
func (a Amount) Div(factor decimal.Decimal) Amount {
	return Amount{
		value:    a.value.Div(factor),
		currency: a.currency,
	}
}

// Percent calculates percentage of amount
func (a Amount) Percent(percent int) Amount {
	factor := decimal.NewFromInt(int64(percent)).Div(decimal.NewFromInt(100))
	return a.Mul(factor)
}

// GreaterThan compares two amounts
func (a Amount) GreaterThan(other Amount) bool {
	return a.value.GreaterThan(other.value)
}

// LessThan compares two amounts
func (a Amount) LessThan(other Amount) bool {
	return a.value.LessThan(other.value)
}

// Equal compares two amounts
func (a Amount) Equal(other Amount) bool {
	return a.currency == other.currency && a.value.Equal(other.value)
}

// Min returns minimum of two amounts
func Min(a, b Amount) Amount {
	if a.LessThan(b) {
		return a
	}
	return b
}

// Max returns maximum of two amounts
func Max(a, b Amount) Amount {
	if a.GreaterThan(b) {
		return a
	}
	return b
}

// MarshalJSON implements json.Marshaler
func (a Amount) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Value    string   `json:"value"`
		Currency Currency `json:"currency"`
	}{
		Value:    a.value.String(),
		Currency: a.currency,
	})
}

// UnmarshalJSON implements json.Unmarshaler
func (a *Amount) UnmarshalJSON(data []byte) error {
	var v struct {
		Value    string   `json:"value"`
		Currency Currency `json:"currency"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	amount, err := New(v.Value, v.Currency)
	if err != nil {
		return err
	}
	*a = amount
	return nil
}

// Scan implements sql.Scanner for database reads
func (a *Amount) Scan(src interface{}) error {
	switch v := src.(type) {
	case string:
		d, err := decimal.NewFromString(v)
		if err != nil {
			return err
		}
		a.value = d
	case []byte:
		d, err := decimal.NewFromString(string(v))
		if err != nil {
			return err
		}
		a.value = d
	case int64:
		a.value = decimal.NewFromInt(v)
	case float64:
		a.value = decimal.NewFromFloat(v)
	default:
		return fmt.Errorf("cannot scan %T into Amount", src)
	}
	return nil
}

// Value implements driver.Valuer for database writes
func (a Amount) DBValue() (driver.Value, error) {
	return a.value.String(), nil
}
