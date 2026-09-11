// Package models defines the data structures for financial products.
package models

import (
	"strconv"
	"strings"
)

// Bond represents a bond (obligation) security from the Moscow Exchange ISS.
// Fields are populated by merging the "securities" (static) and "marketdata"
// (dynamic) blocks of the ISS response.
type Bond struct {
	// --- Core identification ---
	SECID     string `json:"secid"`
	BoardID   string `json:"board_id"`
	ShortName string `json:"short_name"`
	SecName   string `json:"sec_name,omitempty"` // full security name

	// --- Face value & coupon ---
	FaceValue    float64 `json:"face_value"`    // par value (₽)
	CouponValue  float64 `json:"coupon_value"`  // current coupon payment (₽)
	CouponPercent float64 `json:"coupon_percent"` // coupon rate (% p.a.)
	CouponPeriod int     `json:"coupon_period"` // days between coupon payments
	NextCoupon   string  `json:"next_coupon"`   // next coupon date (YYYY-MM-DD)
	AccruedInt   float64 `json:"accrued_int"`   // accrued interest (₽)

	// --- Maturity ---
	MatDate string `json:"mat_date"` // maturity date (YYYY-MM-DD)

	// --- Market data (from marketdata block) ---
	Last            *float64 `json:"last"`              // last trade price (% of face)
	Bid             *float64 `json:"bid"`               // best bid (% of face)
	Offer           *float64 `json:"offer"`             // best offer (% of face)
	YieldToMaturity *float64 `json:"yield_to_maturity"` // yield to maturity (%)

	// --- Listing ---
	ListLevel int `json:"list_level"` // listing level: 1, 2, or 3
}

// String returns a concise, human-readable representation of the bond.
func (b Bond) String() string {
	var s strings.Builder
	s.Grow(128)
	s.WriteString("Bond{SECID:")
	s.WriteString(b.SECID)
	s.WriteString(" Name:")
	s.WriteString(b.ShortName)
	s.WriteString(" Coupon:")
	s.WriteString(strconv.FormatFloat(b.CouponPercent, 'f', 2, 64))
	s.WriteByte('%')
	if b.YieldToMaturity != nil {
		s.WriteString(" YTM:")
		s.WriteString(strconv.FormatFloat(*b.YieldToMaturity, 'f', 2, 64))
		s.WriteByte('%')
	}
	s.WriteString(" Mat:")
	s.WriteString(b.MatDate)
	s.WriteByte('}')
	return s.String()
}
