package schema

import "fmt"

func CreateStatsTableSQL(tableName string) string {
	return fmt.Sprintf(`
	CREATE TABLE IF NOT EXISTS %s (
		date DATE,
		category TEXT,
		average_apr REAL,
		average_income REAL,
		PRIMARY KEY (date, category)
	)`, tableName)
}

func InsertStatSQL(tableName string) string {
	return fmt.Sprintf(`
		INSERT INTO %s (date, category, average_apr, average_income)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (date, category) DO UPDATE 
		SET average_apr = EXCLUDED.average_apr, average_income = EXCLUDED.average_income
	`, tableName)
}

func GetLatestStatsSQL(tableName string) string {
	return fmt.Sprintf(`
		SELECT date, category, average_apr, average_income
		FROM %s
		WHERE date = (SELECT MAX(date) FROM %s)
	`, tableName, tableName)
}
