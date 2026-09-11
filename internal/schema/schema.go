// Package schema provides shared SQL schema definitions.
package schema

import (
	"fmt"

	"github.com/lib/pq"
)

// CreateDepositsTableSQL returns the DDL for creating the deposits table with the given name.
func CreateDepositsTableSQL(tableName string) string {
	return fmt.Sprintf(`
	CREATE TABLE IF NOT EXISTS %s (`, pq.QuoteIdentifier(tableName)) + `
	    id INTEGER,
		product_url TEXT PRIMARY KEY,  -- unique deposit identifier; used for upsert dedup
		rate_min REAL,
		rate_max REAL,
		amount_from INTEGER,
		amount_to INTEGER,            -- NULL means no upper limit
		period_from INTEGER,
		period_to INTEGER,            -- NULL means no upper limit
		product_name TEXT,
		bank_name TEXT,
		deposit_name TEXT,
		is_saving_account BOOLEAN,
		is_children_deposit BOOLEAN,
		is_pension_deposit BOOLEAN,
		feature_list TEXT,             -- JSON map: {"capitalization": true, ...}
		special_restrictions TEXT,
		special_type_list_names TEXT,  -- JSON array: ["premium", "online"]
		efficient_rate REAL,
		capitalization_periods TEXT,   -- JSON map: {"monthly": "22.5%%", ...}
		is_partial_withdrawal_possible BOOLEAN,
		is_replenishment_possible BOOLEAN,
		is_prolongation_possible BOOLEAN,
		prolongation_max INTEGER,
		prolongation_comment TEXT,
		prolongation_comment_html TEXT,
		is_in_office_opening_possible BOOLEAN,
		rates_extremum TEXT,           -- JSON object: see models.RatesExtremum
		detailed_conditions TEXT,
		is_rate_increase_possible BOOLEAN,
		rate_increase_comment_html TEXT,
		replenishment_comment_html TEXT,
		early_termination_comment_html TEXT,
		payment_comment_html TEXT,
		capitalization_comment_html TEXT,
		partial_withdrawal_comment_html TEXT,
		rate_comment_html TEXT,
		percent_calculation TEXT,
		is_new_client BOOLEAN,
		new_client_comment TEXT,
		is_new_money BOOLEAN,
		new_money_comment TEXT,
		is_key_rate_linked BOOLEAN,
		key_rate_linked_comment TEXT
	)`
}

// InsertDepositSQL returns the SQL for inserting a deposit into the given table.
func InsertDepositSQL(tableName string) string {
	return fmt.Sprintf(`
		INSERT INTO %s (
			id, product_url, rate_min, rate_max, amount_from, amount_to,
			period_from, period_to, product_name, bank_name, deposit_name,
			is_saving_account, is_children_deposit, is_pension_deposit,
			feature_list, special_restrictions, special_type_list_names,
			efficient_rate, capitalization_periods, is_partial_withdrawal_possible,
			is_replenishment_possible, is_prolongation_possible, prolongation_max,
			prolongation_comment, prolongation_comment_html, is_in_office_opening_possible,
			rates_extremum, detailed_conditions, is_rate_increase_possible,
			rate_increase_comment_html, replenishment_comment_html, early_termination_comment_html,
			payment_comment_html, capitalization_comment_html, partial_withdrawal_comment_html,
			rate_comment_html, percent_calculation, is_new_client, new_client_comment,
			is_new_money, new_money_comment, is_key_rate_linked, key_rate_linked_comment
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29, $30, $31, $32, $33, $34, $35, $36, $37, $38, $39, $40, $41, $42, $43)`, pq.QuoteIdentifier(tableName))
}
