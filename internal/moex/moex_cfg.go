package moex

// ISS defaults and URL patterns.
const (
	defaultBaseURL = "https://iss.moex.com"

	// bondsSecuritiesPath is the path for fetching all bond securities with
	// both static (securities) and dynamic (marketdata) data.
	bondsSecuritiesPath = "/iss/engines/stock/markets/bonds/securities.json"

	// defaultLanguageHeader for ISS requests.
	defaultLanguageHeader = "en-US,en;q=0.9,ru-RU;q=0.8,ru;q=0.7"

	// moexBondURL is the template for a bond page link on moex.com.
	MoexBondURL = "https://www.moex.com/en/issue.aspx?code="
)

// Boards we fetch from. TQOB = government OFZ, TQCB = corporate.
var defaultBoards = []string{"TQOB", "TQCB"}

// securitiesColumns are the columns we request from the securities block.
var securitiesColumns = []string{
	"SECID", "BOARDID", "SHORTNAME", "SECNAME",
	"FACEVALUE", "COUPONVALUE", "COUPONPERCENT", "COUPONPERIOD",
	"NEXTCOUPON", "MATDATE", "ACCRUEDINT", "LISTLEVEL",
}

// marketdataColumns are the columns we request from the marketdata block.
var marketdataColumns = []string{
	"SECID", "BOARDID", "LAST", "BID", "OFFER", "YIELD",
}
