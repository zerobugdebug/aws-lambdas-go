package cipher

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"

)

const (
	envZerobounceAPIKey = "ZEROBOUNCE_API_KEY"
	envZerobounceAPIURL = "ZEROBOUNCE_API_URL"
)

type ZerobounceValidateResponse struct {
	Address        string `json:"address"`
	Status         string `json:"status"`
	SubStatus      string `json:"sub_status"`
	FreeEmail      bool   `json:"free_email"`
	DidYouMean     string `json:"did_you_mean"`
	Account        string `json:"account"`
	Domain         string `json:"domain"`
	DomainAgeDays  string `json:"domain_age_days"`
	SMTPProvider   string `json:"smtp_provider"`
	MxRecord       string `json:"mx_record"`
	MxFound        string `json:"mx_found"`
	Firstname      string `json:"firstname"`
	Lastname       string `json:"lastname"`
	Gender         string `json:"gender"`
	Country        string `json:"country"`
	Region         string `json:"region"`
	City           string `json:"city"`
	Zipcode        string `json:"zipcode"`
	RawProcessedAt string `json:"processed_at"`
}

func GenerateAuthKey() (string, error) {
	bytes := make([]byte, 36) // 288 bits
	_, err := rand.Read(bytes)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(bytes), nil
}

func GenerateIDHash(identifier string, identifierType string) (string, error) {
	var normalizedIdentifier string
	switch identifierType {
	case "sms":
		normalizedIdentifier = normalizePhoneNumber(identifier)
	case "email":
		normalizedIdentifier = normalizeEmail(identifier)
	default:
		return "", fmt.Errorf("unknown identifier type: %s", identifierType)
	}
	if normalizedIdentifier == "" {
		return "", fmt.Errorf("incorrect identifier: %s", identifier)
	}

	hash := sha256.Sum256([]byte(normalizedIdentifier))
	return hex.EncodeToString(hash[:]), nil
}

func normalizePhoneNumber(phone string) string {
	// Remove all non-digit characters
	re := regexp.MustCompile(`\D`)
	digits := re.ReplaceAllString(phone, "")

	// Check if the number is too short or too long
	if len(digits) < 7 || len(digits) > 15 {
		return ""
	}

	// Check if the number starts with a country code
	if strings.HasPrefix(digits, "00") {
		digits = "+" + digits[2:]
	} else if !strings.HasPrefix(digits, "+") {
		// If no country code, assume it's a domestic number and add +1 (US/Canada)
		digits = "+1" + digits
	}

	// Validate the resulting number
	re = regexp.MustCompile(`^\+[1-9]\d{1,14}$`)
	if !re.MatchString(digits) {
		return ""
	}

	return digits
}

func normalizeEmail(email string) string {
	fmt.Printf("normalizeEmail email: %v\n", email)

	// Convert to lowercase
	email = strings.ToLower(email)

	// Remove leading/trailing whitespace
	email = strings.TrimSpace(email)

	// Split the email into local part and domain
	parts := strings.Split(email, "@")
	if len(parts) != 2 {
		return ""
	}

	localPart, domain := parts[0], parts[1]
	if idx := strings.Index(domain, "."); idx == -1 {
		return ""
	}

	// Remove dots from local part (for Gmail-style addresses)
	localPart = strings.ReplaceAll(localPart, ".", "")

	// Remove everything after '+' in local part
	if idx := strings.Index(localPart, "+"); idx != -1 {
		localPart = localPart[:idx]
	}

	// Remove subdomains for common email providers
	commonDomains := map[string]string{
		"googlemail.com": "gmail.com",
		"live.com":       "hotmail.com",
	}

	if normalizedDomain, ok := commonDomains[domain]; ok {
		domain = normalizedDomain
	}

	/// Check for well known domain
	wellKnownDomains := []string{
		"gmail.com",
		"yahoo.com",
		"hotmail.com",
		"aol.com",
		"outlook.com",
		"comcast.net",
		"icloud.com",
		"msn.com",
		"hotmail.co.uk",
		"sbcglobal.net",
		"live.com",
		"yahoo.co.in",
		"me.com",
		"att.net",
		"mail.ru",
		"bellsouth.net",
		"rediffmail.com",
		"cox.net",
		"yahoo.co.uk",
		"verizon.net",
		"ymail.com",
		"hotmail.it",
		"kw.com",
		"yahoo.com.tw",
		"mac.com",
		"live.se",
		"live.nl",
		"yahoo.com.br",
		"googlemail.com",
		"libero.it",
		"web.de",
		"allstate.com",
		"btinternet.com",
		"online.no",
		"yahoo.com.au",
		"live.dk",
		"earthlink.net",
		"yahoo.fr",
		"yahoo.it",
		"gmx.de",
		"hotmail.fr",
		"shawinc.com",
		"yahoo.de",
		"moe.edu.sg",
		"163.com",
		"naver.com",
		"bigpond.com",
		"statefarm.com",
		"remax.net",
		"rocketmail.com",
		"live.no",
		"yahoo.ca",
		"bigpond.net.au",
		"hotmail.se",
		"gmx.at",
		"live.co.uk",
		"mail.com",
		"yahoo.in",
		"yandex.ru",
		"qq.com",
		"charter.net",
		"indeedemail.com",
		"alice.it",
		"hotmail.de",
		"bluewin.ch",
		"optonline.net",
		"wp.pl",
		"yahoo.es",
		"hotmail.no",
		"pindotmedia.com",
		"orange.fr",
		"live.it",
		"yahoo.co.id",
		"yahoo.no",
		"hotmail.es",
		"morganstanley.com",
		"wellsfargo.com",
		"juno.com",
		"wanadoo.fr",
		"facebook.com",
		"edwardjones.com",
		"yahoo.se",
		"fema.dhs.gov",
		"rogers.com",
		"yahoo.com.hk",
		"live.com.au",
		"nic.in",
		"nab.com.au",
		"ubs.com",
		"uol.com.br",
		"shaw.ca",
		"t-online.de",
		"umich.edu",
		"westpac.com.au",
		"yahoo.com.mx",
		"yahoo.com.sg",
		"farmersagent.com",
		"anz.com",
		"yahoo.dk",
		"dhs.gov",
		"intexid.com",
		"go.intexid.com",
		"evacrane.com",
		"evacrane.ca",
		"zerobugdebug.ca",
	}
	if slices.Contains(wellKnownDomains, domain) {
		fmt.Printf("Well known domain: %s\n", domain)
	} else {
		// Check for the fake or disposable e-mails
		isDisposableEmail, _ := checkDisposableEmail(localPart + "@" + domain)
		if isDisposableEmail {
			return ""
		}
	}
	// Reconstruct the email
	return localPart + "@" + domain
}

func checkDisposableEmail(email string) (bool, error) {

	apiKey := os.Getenv(envZerobounceAPIKey)
	apiUrl := os.Getenv(envZerobounceAPIURL)
	if apiKey == "" || apiUrl == "" {
		fmt.Println("Zerobounce API configuration or URL not found")
		return false, fmt.Errorf("zerobounce API configuration not found")
	}

	url := fmt.Sprintf("%s?api_key=%s&email=%s&ip_address=", apiUrl, apiKey, email)
	fmt.Printf("checkDisposableEmail url: %v\n", url)

	resp, err := http.Get(url)
	if err != nil {
		fmt.Printf("Error getting Zerobounce URL: %s\n", err)
		return false, err
	}
	defer resp.Body.Close()

	var result ZerobounceValidateResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		fmt.Printf("Error parsing Zerobounce response: %s", err)
		return false, err
	}
	fmt.Printf("checkDisposableEmail result: %v\n", result)
	switch result.Status {
	case "valid", "catch-all", "unknown":
		return false, nil
	default:
		fmt.Printf("Email is not valid. Email status: %s\n", result.Status)
		return true, fmt.Errorf("fake email address")
	}

}
