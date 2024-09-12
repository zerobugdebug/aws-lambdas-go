package cipher

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

)

func GenerateAuthKey() (string, error) {
	bytes := make([]byte, 36) // 128 bits
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
		// You may want to change this default or make it configurable
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

	// Check if the domain is disposable
	isDisposable, _ := checkDisposableDomain(domain)
	if isDisposable {
		return ""
	}

	// Reconstruct the email
	return localPart + "@" + domain
}

func checkDisposableDomain(domain string) (bool, error) {
	url := fmt.Sprintf("https://open.kickbox.com/v1/disposable/%s", domain)

	resp, err := http.Get(url)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	var result struct {
		Disposable bool `json:"disposable"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, err
	}

	return result.Disposable, nil
}
