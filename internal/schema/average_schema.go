package schema

import "fmt"

func CreateAveragesTableSQL(tableName string) string {
	return fmt.Sprintf(`
	CREATE TABLE IF NOT EXISTS %s (
		category TEXT PRIMARY KEY,
		average_apr REAL,
		average_income REAL
	)`, tableName)
}

func InsertAverageSQL(tableName string) string {
	return fmt.Sprintf(`
		INSERT INTO %s (category, average_apr, average_income)
		VALUES ($1, $2, $3)
		ON CONFLICT (category) DO UPDATE 
		SET average_apr = EXCLUDED.average_apr, average_income = EXCLUDED.average_income
	`, tableName)
}
