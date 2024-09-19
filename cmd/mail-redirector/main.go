package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/quotedprintable"
	"net/smtp"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/DusanKasan/parsemail"
	"github.com/PuerkitoBio/goquery"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/dynamodb/dynamodbattribute"
	"github.com/aws/aws-sdk-go/service/s3"

	"github.com/zerobugdebug/aws-lambdas-go/pkg/cipher"
)

const (
	defaultFromEmail = "nobody@nobody.none"
	defaultToEmail   = "nobody@nobody.none"
	tableOrdersName  = "ORDERS"
)

type OrderData struct {
	OrderID     string    `json:"order_id"`
	OrderNumber string    `json:"order_number"`
	TotalAmount string    `json:"total_amount"`
	ItemName    string    `json:"item_name"`
	ItemID      string    `json:"item_id"`
	ItemPrice   string    `json:"item_price"`
	Quantity    string    `json:"quantity"`
	ClientName  string    `json:"client_name"`
	ClientEmail string    `json:"client_email"`
	LoginType   string    `json:"login_type"`
	Login       string    `json:"login"`
	Timestamp   time.Time `json:"timestamp"`
	Active      int       `json:"active"`
	UserHash    string    `json:"user_hash"`
}

func storeOrderInDynamoDB(orderData OrderData, dynamodbClient *dynamodb.DynamoDB) error {
	orderData.Timestamp = time.Now()
	orderData.Active = 1

	bytes := make([]byte, 18)
	_, err := rand.Read(bytes)
	if err != nil {
		return fmt.Errorf("failed to generate new order id: %w", err)
	}

	orderData.OrderID = base64.URLEncoding.EncodeToString(bytes)

	loginTypeMap := map[string]string{
		"Phone":  "sms",
		"E-mail": "email",
	}
	orderData.UserHash, err = cipher.GenerateIDHash(orderData.Login, loginTypeMap[orderData.LoginType])
	if err != nil {
		return fmt.Errorf("failed to generate user hash: %w", err)
	}

	av, err := dynamodbattribute.MarshalMap(orderData)
	if err != nil {
		return fmt.Errorf("failed to marshal order data: %w", err)
	}

	input := &dynamodb.PutItemInput{
		Item:      av,
		TableName: aws.String(tableOrdersName),
	}

	fmt.Printf("av: %+v\n", av)

	_, err = dynamodbClient.PutItem(input)
	if err != nil {
		return fmt.Errorf("failed to put item in DynamoDB: %w", err)
	}

	return nil
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

			err = storeOrderInDynamoDB(orderData, dynamodb.New(sess))
			if err != nil {
				fmt.Printf("failed to store order data in DynamoDB: %v", err)
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

	reader := quotedprintable.NewReader(strings.NewReader(emailContent))
	decodedBytes, err := io.ReadAll(reader)
	if err != nil {
		fmt.Println("Error decoding:", err)
		return orderData, err
	}
	decodedHTML := string(decodedBytes)
	//fmt.Printf("decodedHTML: %v\n", string(decodedHTML))

	// Load the HTML file
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(decodedHTML))
	if err != nil {
		return orderData, err
	}

	// Find the [Login type] field
	doc.Find("div").Each(func(i int, s *goquery.Selection) {
		text := strings.TrimSpace(s.Text())
		if strings.Contains(text, "[Login type]:") {
			// The next sibling contains the login type value
			orderData.LoginType = strings.TrimSpace(s.Next().Find("td").Text())
			fmt.Printf("orderData.LoginType: %v\n", orderData.LoginType)
		}
		if strings.Contains(text, "[Login]:") {
			// The next sibling contains the login value
			orderData.Login = strings.TrimSpace(s.Next().Find("td").Text())
			fmt.Printf("orderData.Login: %v\n", orderData.Login)
		}
	})

	var pattern string
	var re *regexp.Regexp
	var match []string

	// pattern = `(?s)<span[^>]*>.*(SQ\d+).*</span>`
	// //`<span[^>]*>\s*<br\s*/?>\s*(SQ\d+)\s*<br\s*/?>\s*</span>`
	// re = regexp.MustCompile(pattern)
	// regexp.Compile()
	// match = re.FindStringSubmatch(decodedHTML)
	// fmt.Printf("ItemID match: %v\n", match)
	// if len(match) > 1 {
	// 	orderData.ItemID = match[1]
	// }

	// pattern = `<div\s+style="padding-left:\s*10px;\s*padding-bottom:\s*10px;">\s*(\d*?)\s*</div>`
	// re = regexp.MustCompile(pattern)
	// match = re.FindStringSubmatch(decodedHTML)
	// fmt.Printf("Quantity match: %v\n", match)
	// if len(match) > 1 {
	// 	orderData.Quantity = match[1]
	// }

	// pattern = `Order #(\d+)`
	// re = regexp.MustCompile(pattern)
	// match = re.FindStringSubmatch(decodedHTML)
	// fmt.Printf("Order match: %v\n", match)
	// if len(match) > 1 {
	// 	orderData.OrderNumber = match[1]
	// }

	pattern = `(?s)<span[^>]*>.*(SQ\d+).*</span>`
	re = regexp.MustCompile(pattern)
	match = re.FindStringSubmatch(decodedHTML)
	if len(match) > 1 {
		orderData.ItemID = match[1]
	}

	pattern = `Order #(\d+)\.`
	re = regexp.MustCompile(pattern)
	match = re.FindStringSubmatch(decodedHTML)
	if len(match) > 1 {
		orderData.OrderNumber = match[1]
	}

	pattern = `(?s)BILLED TO:.*?<div[^>]*>\s*([^<]+)`
	re = regexp.MustCompile(pattern)
	match = re.FindStringSubmatch(decodedHTML)
	if len(match) > 1 {
		orderData.ClientName = match[1]
	}

	pattern = `(?s)BILLED TO:.*?<span[^>]*>\s*([^<@\s]+@[^<\s]+)\s*</span>`
	re = regexp.MustCompile(pattern)
	match = re.FindStringSubmatch(decodedHTML)
	if len(match) > 1 {
		orderData.ClientEmail = match[1]
	}

	pattern = `(?s)QTY.*?<div[^>]*>\s*(\d+)\s*</div>`
	re = regexp.MustCompile(pattern)
	match = re.FindStringSubmatch(decodedHTML)
	if len(match) > 1 {
		orderData.Quantity = match[1]
	}

	pattern = `(?s)UNIT PRICE.*?<div[^>]*>\s*(CA\$[\d.]+)\s*</div>`
	re = regexp.MustCompile(pattern)
	match = re.FindStringSubmatch(decodedHTML)
	if len(match) > 1 {
		orderData.ItemPrice = match[1]
	}

	//Cleanup HTML and parse it
	// Split the input into lines
	// lines := strings.Split(emailContent, "\n")

	// var builder strings.Builder

	// // Iterate over each line
	// for _, line := range lines {
	// 	// Trim the line to remove leading and trailing whitespace
	// 	line = strings.ReplaceAll(line, "=20", " ")
	// 	line = strings.TrimSpace(line)
	// 	line = strings.TrimSuffix(line, "=")

	// 	// Add the trimmed line to the builder
	// 	builder.WriteString(line)
	// }

	//orderData, err := parseHTML(builder.String())
	if err != nil {
		return orderData, err
	}

	return orderData, nil
}

// func parseHTML(cleanHTML string) (OrderData, error) {
// 	var orderData OrderData
// 	tkn := html.NewTokenizer(strings.NewReader(cleanHTML))

// 	state := "seekBilledTo"
// 	for {
// 		tt := tkn.Next()
// 		if tt == html.ErrorToken {
// 			if tkn.Err() == io.EOF {
// 				break
// 			}
// 			return OrderData{}, tkn.Err()
// 		}

// 		if tt == html.TextToken {
// 			t := tkn.Token()
// 			text := strings.TrimSpace(t.Data)

// 			switch state {
// 			case "seekBilledTo":
// 				if text == "BILLED TO:" {
// 					state = "getClientName"
// 				}
// 			case "getClientName":
// 				if text != "" {
// 					orderData.ClientName = text
// 					state = "seekEmail"
// 				}
// 			case "seekEmail":
// 				if strings.Contains(text, "@") {
// 					orderData.ClientEmail = text
// 					state = "seekSubtotal"
// 				}
// 			case "seekSubtotal":
// 				if text == "SUBTOTAL" {
// 					state = "getItemName"
// 				}
// 			case "getItemName":
// 				if text != "" {
// 					orderData.ItemName = text
// 					state = "getItemID"
// 				}
// 			case "getItemID":
// 				if text != "" {
// 					orderData.ItemID = text
// 					state = "getQuantity"
// 				}
// 			case "getQuantity":
// 				if text != "" {
// 					orderData.Quantity = text
// 					state = "getItemPrice"
// 				}
// 			case "getItemPrice":
// 				if text != "" {
// 					orderData.ItemPrice = text
// 					state = "getTotalAmount"
// 				}
// 			case "getTotalAmount":
// 				if text != "" {
// 					orderData.TotalAmount = text
// 					return orderData, nil
// 				}
// 			}
// 		}
// 	}

// 	return orderData, errors.New("incomplete data")
// }

func main() {
	lambda.Start(HandleRequest)
}
