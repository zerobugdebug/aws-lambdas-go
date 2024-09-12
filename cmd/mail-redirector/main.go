package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/smtp"
	"os"
	"strings"

	"github.com/DusanKasan/parsemail"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"golang.org/x/net/html"
)

const (
	defaultFromEmail = "nobody@nobody.none"
	defaultToEmail   = "nobody@nobody.none"
)

type OrderData struct {
	OrderNumber string `json:"orderNumber"`
	TotalAmount string `json:"totalAmount"`
	ItemName    string `json:"itemName"`
	ItemID      string `json:"itemID"`
	ItemPrice   string `json:"itemPrice"`
	Quantity    string `json:"quantity"`
	ClientName  string `json:"clientName"`
	ClientEmail string `json:"clientEmail"`
}

func getEmailValue(email string, emailMap map[string]string) string {
	// Iterate over the emails until match a key in the map
	value, exists := emailMap[email]
	if exists {
		return value
	}

	// Return empty string if no key was found
	return ""
}

func HandleRequest(event events.SimpleEmailEvent) error {
	//Init the e-mail key-value map
	emailMapJson := os.Getenv("MAILREDIR_EMAIL_MAP")
	// Define a map to hold the parsed JSON
	emailMap := make(map[string]string)

	// Unmarshal the JSON into the map
	err := json.Unmarshal([]byte(emailMapJson), &emailMap)
	if err != nil {
		return fmt.Errorf("error while parsing EMAIL_MAP: %w", err)
	}

	mailBucket := os.Getenv("MAILREDIR_S3_BUCKET")
	// Create AWS SDK configuration and clients
	cfg := aws.NewConfig()
	sess, err := session.NewSession(cfg)
	if err != nil {
		return fmt.Errorf("could not create session: %w", err)
	}

	s3Client := s3.New(sess)

	for _, record := range event.Records {
		fmt.Printf("record.SES.Mail.MessageID: %v\n", record.SES.Mail.MessageID)
		// Retrieve mail contents from S3
		obj, err := s3Client.GetObject(&s3.GetObjectInput{
			Bucket: aws.String(mailBucket),
			Key:    aws.String(record.SES.Mail.MessageID),
		})
		if err != nil {
			return fmt.Errorf("could not get object: %w", err)
		}

		rawEmail, err := io.ReadAll(obj.Body)
		if err != nil {
			log.Fatal(err)
		}

		fmt.Printf("---MAIL PARSER---\n")

		email, err := parsemail.Parse(bytes.NewReader(rawEmail)) // returns Email struct and error
		if err != nil {
			return fmt.Errorf("failed to parse email: %w", err)
		}

		fmt.Printf("email.From: %+v\n", email.From)
		fmt.Printf("email.Subject: %+v\n", email.Subject)
		fmt.Printf("email.To: %+v\n", email.To)

		if isOrderEmail(email) {
			// Extract order data
			orderData, err := extractOrderData(email.HTMLBody)
			if err != nil {
				fmt.Printf("failed to extract order data: %v", err)
			} else {
				fmt.Printf("orderData: %+v\n", orderData)
			}
		}

		toAddressSlice := []string{}
		for _, address := range email.To {
			fmt.Printf("address.Address: %v\n", address.Address)
			toAddress := getEmailValue(address.Address, emailMap)
			if toAddress != "" {
				fmt.Printf("Matched toAddress: %v\n", toAddress)
				toAddressSlice = append(toAddressSlice, toAddress)
			}
		}

		if len(toAddressSlice) == 0 {
			toAddress := os.Getenv("MAILREDIR_DEFAULT_TO")
			fmt.Printf("No matches, using environment variable MAILREDIR_DEFAULT_TO: %v\n", toAddress)
			if toAddress == "" {
				toAddress = defaultToEmail
				fmt.Printf("No environment variable, using default e-mail address: %v\n", toAddress)
			}
			toAddressSlice = []string{toAddress}
		}

		fmt.Printf("Final toAddressSlice: %v\n", toAddressSlice)
		fmt.Printf("---MAIL PARSER---\n")

		smtpServerHost := os.Getenv("MAILREDIR_SMTP_SERVER_HOST")
		smtpServerPort := os.Getenv("MAILREDIR_SMTP_SERVER_PORT")

		// Send the email via SMTP
		err = smtp.SendMail(smtpServerHost+":"+smtpServerPort, nil, email.From[0].Address, toAddressSlice, rawEmail)
		if err != nil {
			return fmt.Errorf("failed to send e-mail: %w", err)
		}

		/* 			// Delete from bucket if everything worked
		   			_, err = s3Client.DeleteObject(&s3.DeleteObjectInput{
		   				Bucket: aws.String(mailBucket),
		   				Key:    aws.String(record.SES.Mail.MessageID),
		   			})
		   			if err != nil {
		   				return nil, fmt.Errorf("could not delete email from s3: %w", err)
		   			}
		*/
	}

	return nil
}

func isOrderEmail(email parsemail.Email) bool {
	return email.From[0].Address == "no-reply@squarespace.com" && email.To[0].Address == "store.manager@evacrane.com" && strings.Contains(email.Subject, "A New Order has Arrived")
}

func extractOrderData(emailContent string) (OrderData, error) {
	var orderData OrderData

	//Cleanup HTML and parse it
	// Split the input into lines
	lines := strings.Split(emailContent, "\n")

	var builder strings.Builder

	// Iterate over each line
	for _, line := range lines {
		// Trim the line to remove leading and trailing whitespace
		line = strings.ReplaceAll(line, "=20", " ")
		line = strings.TrimSpace(line)
		line = strings.TrimSuffix(line, "=")

		// Add the trimmed line to the builder
		builder.WriteString(line)
	}

	orderData, err := parseHTML(builder.String())
	if err != nil {
		return orderData, err
	}

	return orderData, nil
}

func parseHTML(cleanHTML string) (OrderData, error) {
	var orderData OrderData
	tkn := html.NewTokenizer(strings.NewReader(cleanHTML))

	state := "seekBilledTo"
	for {
		tt := tkn.Next()
		if tt == html.ErrorToken {
			if tkn.Err() == io.EOF {
				break
			}
			return OrderData{}, tkn.Err()
		}

		if tt == html.TextToken {
			t := tkn.Token()
			text := strings.TrimSpace(t.Data)

			switch state {
			case "seekBilledTo":
				if text == "BILLED TO:" {
					state = "getClientName"
				}
			case "getClientName":
				if text != "" {
					orderData.ClientName = text
					state = "seekEmail"
				}
			case "seekEmail":
				if strings.Contains(text, "@") {
					orderData.ClientEmail = text
					state = "seekSubtotal"
				}
			case "seekSubtotal":
				if text == "SUBTOTAL" {
					state = "getItemName"
				}
			case "getItemName":
				if text != "" {
					orderData.ItemName = text
					state = "getItemID"
				}
			case "getItemID":
				if text != "" {
					orderData.ItemID = text
					state = "getQuantity"
				}
			case "getQuantity":
				if text != "" {
					orderData.Quantity = text
					state = "getItemPrice"
				}
			case "getItemPrice":
				if text != "" {
					orderData.ItemPrice = text
					state = "getTotalAmount"
				}
			case "getTotalAmount":
				if text != "" {
					orderData.TotalAmount = text
					return orderData, nil
				}
			}
		}
	}

	return orderData, errors.New("incomplete data")
}

func main() {
	lambda.Start(HandleRequest)
}
