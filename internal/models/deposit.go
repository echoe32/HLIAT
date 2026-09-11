// Package models defines the data structures for bank deposit products.
package models

import (
	"strconv"
	"strings"
)

// Deposit represents a single bank deposit product as returned by the API.
// Pointer fields (*int, *string, *bool) indicate values that may be absent (null)
// in the API response. When serialized to JSON, nil pointers produce "null".
type Deposit struct {
	// --- Core identification ---
	ID         int    `json:"id"`
	ProductURL string `json:"product_url"` // relative URL, also used as DB primary key

	// --- Rate & amount range ---
	RateMin    float64 `json:"rate_min"`
	RateMax    float64 `json:"rate_max"`
	AmountFrom int     `json:"amount_from"` // minimum deposit amount
	AmountTo   *int    `json:"amount_to"`   // nil means "no upper limit"

	// --- Period range (in days) ---
	PeriodFrom int  `json:"period_from"`
	PeriodTo   *int `json:"period_to"` // nil means "no upper limit"

	// --- Display names ---
	ProductName string `json:"product_name"`
	BankName    string `json:"bank_name"`
	DepositName string `json:"deposit_name"`

	// --- Deposit category flags ---
	IsSavingAccount   bool `json:"is_saving_account"`
	IsChildrenDeposit bool `json:"is_children_deposit"`
	IsPensionDeposit  bool `json:"is_pension_deposit"`

	// --- Features & restrictions ---
	FeatureList          map[string]bool `json:"feature_list"`            // e.g. {"capitalization": true}
	SpecialRestrictions  *string         `json:"special_restrictions"`    // free-text restrictions, nil if none
	SpecialTypeListNames []string        `json:"special_type_list_names"` // e.g. ["premium", "online"]

	// --- Effective rate & capitalization ---
	EfficientRate         float64           `json:"efficient_rate"`
	CapitalizationPeriods map[string]string `json:"capitalization_periods"` // period label → rate string

	// --- Deposit options ---
	IsPartialWithdrawalPossible bool    `json:"is_partial_withdrawal_possible"`
	IsReplenishmentPossible     bool    `json:"is_replenishment_possible"`
	IsProlongationPossible      *bool   `json:"is_prolongation_possible"` // nil = unknown
	ProlongationMax             *int    `json:"prolongation_max"`         // max number of prolongations
	ProlongationComment         *string `json:"prolongation_comment"`
	ProlongationCommentHtml     *string `json:"prolongation_comment_html"`
	IsInOfficeOpeningPossible   bool    `json:"is_in_office_opening_possible"`

	// --- Rate details ---
	RatesExtremum          RatesExtremum `json:"rates_extremum"`      // min/max rate breakdown by amount/period
	DetailedConditions     *string       `json:"detailed_conditions"` // free-text conditions
	IsRateIncreasePossible bool          `json:"is_rate_increase_possible"`

	// --- HTML comment fields (rendered on the frontend) ---
	RateIncreaseCommentHtml      *string `json:"rate_increase_comment_html"`
	ReplenishmentCommentHtml     *string `json:"replenishment_comment_html"`
	EarlyTerminationCommentHtml  *string `json:"early_termination_comment_html"`
	PaymentCommentHtml           *string `json:"payment_comment_html"`
	CapitalizationCommentHtml    *string `json:"capitalization_comment_html"`
	PartialWithdrawalCommentHtml *string `json:"partial_withdrawal_comment_html"`
	RateCommentHtml              *string `json:"rate_comment_html"`

	// --- Interest calculation & special flags ---
	PercentCalculation   *string `json:"percent_calculation"` // e.g. "365/365"
	IsNewClient          bool    `json:"is_new_client"`
	NewClientComment     *string `json:"new_client_comment"`
	IsNewMoney           bool    `json:"is_new_money"`
	NewMoneyComment      *string `json:"new_money_comment"`
	IsKeyRateLinked      bool    `json:"is_key_rate_linked"`
	KeyRateLinkedComment *string `json:"key_rate_linked_comment"`
}

// String returns a concise, human-readable representation of the deposit
// using strings.Builder and strconv for zero-allocation formatting.
func (d Deposit) String() string {
	var b strings.Builder
	b.Grow(128)
	b.WriteString("Deposit{ID:")
	b.WriteString(strconv.Itoa(d.ID))
	b.WriteString(" Bank:")
	b.WriteString(d.BankName)
	b.WriteString(" URL:")
	b.WriteString(d.ProductURL)
	b.WriteString(" Rate:")
	b.WriteString(strconv.FormatFloat(d.RateMin, 'f', -1, 64))
	b.WriteByte('-')
	b.WriteString(strconv.FormatFloat(d.RateMax, 'f', -1, 64))
	b.WriteByte('}')
	return b.String()
}

// RatesExtremum contains the overall min/max rate boundaries for a deposit
// product, along with a detailed breakdown by amount/period ranges.
type RatesExtremum struct {
	MinRate    float64      `json:"min_rate"`
	MaxRate    float64      `json:"max_rate"`
	MinAmount  int          `json:"min_amount"`
	MaxAmount  *int         `json:"max_amount"` // nil means "no upper limit"
	MinPeriod  int          `json:"min_period"`
	MaxPeriod  *int         `json:"max_period"`  // nil means "no upper limit"
	RatesTable []RatesTable `json:"rates_table"` // rate tiers broken down by amount range
}

// RatesTable represents a single rate tier showing the interest rate
// for a specific amount range, displayed as human-readable notation.
type RatesTable struct {
	Rate         float64 `json:"rate"`          // interest rate for this tier
	FromNotation string  `json:"from_notation"` // e.g. "от 100 000"
	ToNotation   string  `json:"to_notation"`   // e.g. "до 1 000 000"
}
