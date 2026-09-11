package models

import (
	"encoding/json"
	"testing"

	"backend/internal/testutil"
)

func sampleDeposit() Deposit {
	return Deposit{
		ID:                          42,
		ProductURL:                  "/deposits/sample/",
		RateMin:                     18.0,
		RateMax:                     22.5,
		AmountFrom:                  10000,
		AmountTo:                    testutil.Ptr(5000000),
		PeriodFrom:                  30,
		PeriodTo:                    testutil.Ptr(365),
		ProductName:                 "Тест Продукт",
		BankName:                    "Тест Банк",
		DepositName:                 "Тест Вклад",
		IsSavingAccount:             false,
		IsChildrenDeposit:           false,
		IsPensionDeposit:            false,
		FeatureList:                 map[string]bool{"capitalization": true, "replenishment": false},
		SpecialRestrictions:         testutil.Ptr("Только для резидентов"),
		SpecialTypeListNames:        []string{"premium", "online"},
		EfficientRate:               23.1,
		CapitalizationPeriods:       map[string]string{"monthly": "22.5%", "quarterly": "21.0%"},
		IsPartialWithdrawalPossible: true,
		IsReplenishmentPossible:     true,
		IsProlongationPossible:      testutil.Ptr(true),
		ProlongationMax:             testutil.Ptr(3),
		ProlongationComment:         testutil.Ptr("До 3 раз"),
		ProlongationCommentHtml:     testutil.Ptr("<p>До 3 раз</p>"),
		IsInOfficeOpeningPossible:   true,
		RatesExtremum: RatesExtremum{
			MinRate:   18.0,
			MaxRate:   22.5,
			MinAmount: 10000,
			MaxAmount: testutil.Ptr(5000000),
			MinPeriod: 30,
			MaxPeriod: testutil.Ptr(365),
			RatesTable: []RatesTable{
				{Rate: 22.5, FromNotation: "от 100 000₽", ToNotation: "до 1 000 000₽"},
				{Rate: 20.0, FromNotation: "от 1 000 000₽", ToNotation: "до 5 000 000₽"},
			},
		},
		DetailedConditions:           testutil.Ptr("Подробные условия"),
		IsRateIncreasePossible:       true,
		RateIncreaseCommentHtml:      testutil.Ptr("<b>Повышение ставки</b>"),
		ReplenishmentCommentHtml:     testutil.Ptr("<b>Пополнение</b>"),
		EarlyTerminationCommentHtml:  testutil.Ptr("<b>Досрочное расторжение</b>"),
		PaymentCommentHtml:           testutil.Ptr("<b>Выплата</b>"),
		CapitalizationCommentHtml:    testutil.Ptr("<b>Капитализация</b>"),
		PartialWithdrawalCommentHtml: testutil.Ptr("<b>Частичное снятие</b>"),
		RateCommentHtml:              testutil.Ptr("<b>Ставка</b>"),
		PercentCalculation:           testutil.Ptr("365/365"),
		IsNewClient:                  true,
		NewClientComment:             testutil.Ptr("Бонус для новых"),
		IsNewMoney:                   false,
		NewMoneyComment:              nil,
		IsKeyRateLinked:              true,
		KeyRateLinkedComment:         testutil.Ptr("Привязка к ключевой ставке ЦБ"),
	}
}

func TestDepositJSON_Roundtrip(t *testing.T) {
	original := sampleDeposit()

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded Deposit
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	// Spot-check scalar fields
	if decoded.ID != original.ID {
		t.Errorf("ID: got %d, want %d", decoded.ID, original.ID)
	}
	if decoded.ProductURL != original.ProductURL {
		t.Errorf("ProductURL: got %q, want %q", decoded.ProductURL, original.ProductURL)
	}
	if decoded.RateMin != original.RateMin {
		t.Errorf("RateMin: got %f, want %f", decoded.RateMin, original.RateMin)
	}
	if decoded.RateMax != original.RateMax {
		t.Errorf("RateMax: got %f, want %f", decoded.RateMax, original.RateMax)
	}
	if decoded.BankName != original.BankName {
		t.Errorf("BankName: got %q, want %q", decoded.BankName, original.BankName)
	}
	if decoded.EfficientRate != original.EfficientRate {
		t.Errorf("EfficientRate: got %f, want %f", decoded.EfficientRate, original.EfficientRate)
	}
	if decoded.IsNewClient != original.IsNewClient {
		t.Errorf("IsNewClient: got %v, want %v", decoded.IsNewClient, original.IsNewClient)
	}
	if decoded.IsKeyRateLinked != original.IsKeyRateLinked {
		t.Errorf("IsKeyRateLinked: got %v, want %v", decoded.IsKeyRateLinked, original.IsKeyRateLinked)
	}

	// Pointer fields
	if decoded.AmountTo == nil || *decoded.AmountTo != *original.AmountTo {
		t.Errorf("AmountTo: got %v, want %v", decoded.AmountTo, original.AmountTo)
	}
	if decoded.PeriodTo == nil || *decoded.PeriodTo != *original.PeriodTo {
		t.Errorf("PeriodTo: got %v, want %v", decoded.PeriodTo, original.PeriodTo)
	}
	if decoded.IsProlongationPossible == nil || *decoded.IsProlongationPossible != *original.IsProlongationPossible {
		t.Errorf("IsProlongationPossible: got %v, want %v", decoded.IsProlongationPossible, original.IsProlongationPossible)
	}

	// Map fields
	if len(decoded.FeatureList) != len(original.FeatureList) {
		t.Errorf("FeatureList length: got %d, want %d", len(decoded.FeatureList), len(original.FeatureList))
	}
	if len(decoded.CapitalizationPeriods) != len(original.CapitalizationPeriods) {
		t.Errorf("CapitalizationPeriods length: got %d, want %d", len(decoded.CapitalizationPeriods), len(original.CapitalizationPeriods))
	}

	// Slice fields
	if len(decoded.SpecialTypeListNames) != len(original.SpecialTypeListNames) {
		t.Errorf("SpecialTypeListNames length: got %d, want %d", len(decoded.SpecialTypeListNames), len(original.SpecialTypeListNames))
	}

	// Nested struct
	if len(decoded.RatesExtremum.RatesTable) != len(original.RatesExtremum.RatesTable) {
		t.Errorf("RatesTable length: got %d, want %d", len(decoded.RatesExtremum.RatesTable), len(original.RatesExtremum.RatesTable))
	}
}

func TestDepositJSON_NullableFields(t *testing.T) {
	jsonWithNulls := `{
		"id": 1,
		"product_url": "/test/",
		"rate_min": 10.0,
		"rate_max": 15.0,
		"amount_from": 1000,
		"amount_to": null,
		"period_from": 30,
		"period_to": null,
		"product_name": "Test",
		"bank_name": "TestBank",
		"deposit_name": "TestDeposit",
		"is_saving_account": false,
		"is_children_deposit": false,
		"is_pension_deposit": false,
		"special_restrictions": null,
		"efficient_rate": 15.5,
		"is_partial_withdrawal_possible": false,
		"is_replenishment_possible": false,
		"is_prolongation_possible": null,
		"prolongation_max": null,
		"prolongation_comment": null,
		"prolongation_comment_html": null,
		"is_in_office_opening_possible": false,
		"detailed_conditions": null,
		"is_rate_increase_possible": false,
		"is_new_client": false,
		"new_client_comment": null,
		"is_new_money": false,
		"new_money_comment": null,
		"is_key_rate_linked": false,
		"key_rate_linked_comment": null,
		"percent_calculation": null
	}`

	var d Deposit
	if err := json.Unmarshal([]byte(jsonWithNulls), &d); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if d.AmountTo != nil {
		t.Errorf("AmountTo should be nil, got %v", *d.AmountTo)
	}
	if d.PeriodTo != nil {
		t.Errorf("PeriodTo should be nil, got %v", *d.PeriodTo)
	}
	if d.IsProlongationPossible != nil {
		t.Errorf("IsProlongationPossible should be nil, got %v", *d.IsProlongationPossible)
	}
	if d.SpecialRestrictions != nil {
		t.Errorf("SpecialRestrictions should be nil, got %q", *d.SpecialRestrictions)
	}
	if d.DetailedConditions != nil {
		t.Errorf("DetailedConditions should be nil, got %q", *d.DetailedConditions)
	}
	if d.NewClientComment != nil {
		t.Errorf("NewClientComment should be nil, got %q", *d.NewClientComment)
	}
	if d.PercentCalculation != nil {
		t.Errorf("PercentCalculation should be nil, got %q", *d.PercentCalculation)
	}

	// Verify non-null scalars are correct
	if d.ID != 1 {
		t.Errorf("ID: got %d, want 1", d.ID)
	}
	if d.RateMax != 15.0 {
		t.Errorf("RateMax: got %f, want 15.0", d.RateMax)
	}
}

func TestDepositJSON_WithActualValues(t *testing.T) {
	jsonWithValues := `{
		"id": 5,
		"product_url": "/deposits/real/",
		"rate_min": 12.0,
		"rate_max": 18.0,
		"amount_from": 50000,
		"amount_to": 3000000,
		"period_from": 90,
		"period_to": 730,
		"product_name": "Реальный Продукт",
		"bank_name": "Реальный Банк",
		"deposit_name": "Реальный Вклад",
		"is_saving_account": true,
		"is_children_deposit": false,
		"is_pension_deposit": true,
		"efficient_rate": 19.2,
		"is_partial_withdrawal_possible": true,
		"is_replenishment_possible": true,
		"is_prolongation_possible": true,
		"prolongation_max": 5,
		"prolongation_comment": "До 5 раз",
		"is_in_office_opening_possible": true,
		"is_rate_increase_possible": false,
		"is_new_client": true,
		"new_client_comment": "Только новые",
		"is_new_money": true,
		"new_money_comment": "Только новые деньги",
		"is_key_rate_linked": true,
		"key_rate_linked_comment": "Привязка к КС",
		"percent_calculation": "365/366",
		"detailed_conditions": "Условия подробно",
		"special_restrictions": "Резиденты РФ"
	}`

	var d Deposit
	if err := json.Unmarshal([]byte(jsonWithValues), &d); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if d.AmountTo == nil || *d.AmountTo != 3000000 {
		t.Errorf("AmountTo: got %v, want 3000000", d.AmountTo)
	}
	if d.PeriodTo == nil || *d.PeriodTo != 730 {
		t.Errorf("PeriodTo: got %v, want 730", d.PeriodTo)
	}
	if d.IsProlongationPossible == nil || *d.IsProlongationPossible != true {
		t.Errorf("IsProlongationPossible: got %v, want true", d.IsProlongationPossible)
	}
	if d.ProlongationMax == nil || *d.ProlongationMax != 5 {
		t.Errorf("ProlongationMax: got %v, want 5", d.ProlongationMax)
	}
	if d.SpecialRestrictions == nil || *d.SpecialRestrictions != "Резиденты РФ" {
		t.Errorf("SpecialRestrictions: got %v, want 'Резиденты РФ'", d.SpecialRestrictions)
	}
	if d.NewClientComment == nil || *d.NewClientComment != "Только новые" {
		t.Errorf("NewClientComment: got %v, want 'Только новые'", d.NewClientComment)
	}
	if d.IsKeyRateLinked != true {
		t.Errorf("IsKeyRateLinked: got %v, want true", d.IsKeyRateLinked)
	}
}

func TestDepositJSON_PartialResponse(t *testing.T) {
	// Minimal JSON — only required fields
	minimal := `{
		"id": 99,
		"product_url": "/minimal/",
		"rate_min": 5.0,
		"rate_max": 10.0,
		"amount_from": 1000,
		"period_from": 30,
		"product_name": "Min",
		"bank_name": "MinBank",
		"deposit_name": "MinDeposit"
	}`

	var d Deposit
	if err := json.Unmarshal([]byte(minimal), &d); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if d.ID != 99 {
		t.Errorf("ID: got %d, want 99", d.ID)
	}
	if d.AmountTo != nil {
		t.Errorf("AmountTo should be nil for partial response")
	}
	if d.PeriodTo != nil {
		t.Errorf("PeriodTo should be nil for partial response")
	}
	if d.FeatureList != nil {
		t.Errorf("FeatureList should be nil for partial response")
	}
	if d.CapitalizationPeriods != nil {
		t.Errorf("CapitalizationPeriods should be nil for partial response")
	}
	if d.SpecialTypeListNames != nil {
		t.Errorf("SpecialTypeListNames should be nil for partial response")
	}
	// Booleans default to false
	if d.IsSavingAccount {
		t.Errorf("IsSavingAccount should default to false")
	}
	if d.IsNewClient {
		t.Errorf("IsNewClient should default to false")
	}
}

func TestRatesExtremum_Roundtrip(t *testing.T) {
	original := RatesExtremum{
		MinRate:   10.0,
		MaxRate:   25.0,
		MinAmount: 1000,
		MaxAmount: testutil.Ptr(10000000),
		MinPeriod: 30,
		MaxPeriod: testutil.Ptr(1095),
		RatesTable: []RatesTable{
			{Rate: 25.0, FromNotation: "от 100 000₽", ToNotation: "до 500 000₽"},
			{Rate: 22.0, FromNotation: "от 500 000₽", ToNotation: "до 1 000 000₽"},
			{Rate: 18.0, FromNotation: "от 1 000 000₽", ToNotation: "до 10 000 000₽"},
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded RatesExtremum
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.MinRate != original.MinRate {
		t.Errorf("MinRate: got %f, want %f", decoded.MinRate, original.MinRate)
	}
	if decoded.MaxRate != original.MaxRate {
		t.Errorf("MaxRate: got %f, want %f", decoded.MaxRate, original.MaxRate)
	}
	if decoded.MaxAmount == nil || *decoded.MaxAmount != *original.MaxAmount {
		t.Errorf("MaxAmount: got %v, want %v", decoded.MaxAmount, *original.MaxAmount)
	}
	if len(decoded.RatesTable) != 3 {
		t.Fatalf("RatesTable length: got %d, want 3", len(decoded.RatesTable))
	}
	for i, rt := range decoded.RatesTable {
		if rt.Rate != original.RatesTable[i].Rate {
			t.Errorf("RatesTable[%d].Rate: got %f, want %f", i, rt.Rate, original.RatesTable[i].Rate)
		}
		if rt.FromNotation != original.RatesTable[i].FromNotation {
			t.Errorf("RatesTable[%d].FromNotation: got %q, want %q", i, rt.FromNotation, original.RatesTable[i].FromNotation)
		}
	}
}

func TestRatesExtremum_NullMaxFields(t *testing.T) {
	jsonStr := `{
		"min_rate": 5.0,
		"max_rate": 10.0,
		"min_amount": 1000,
		"max_amount": null,
		"min_period": 30,
		"max_period": null,
		"rates_table": []
	}`

	var re RatesExtremum
	if err := json.Unmarshal([]byte(jsonStr), &re); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if re.MaxAmount != nil {
		t.Errorf("MaxAmount should be nil, got %v", *re.MaxAmount)
	}
	if re.MaxPeriod != nil {
		t.Errorf("MaxPeriod should be nil, got %v", *re.MaxPeriod)
	}
	if len(re.RatesTable) != 0 {
		t.Errorf("RatesTable should be empty, got %d items", len(re.RatesTable))
	}
}

func TestDeposit_String(t *testing.T) {
	d := Deposit{
		ID:         42,
		BankName:   "TestBank",
		ProductURL: "/test/",
		RateMin:    18,
		RateMax:    22.5,
	}

	s := d.String()
	expected := "Deposit{ID:42 Bank:TestBank URL:/test/ Rate:18-22.5}"
	if s != expected {
		t.Errorf("String(): got %q, want %q", s, expected)
	}
}
