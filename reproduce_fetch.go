//go:build ignore

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

type LokiCurrency struct {
	Name   string `json:"name"`
	Symbol string `json:"symbol"`
}

func main() {
	lokihubServicesURL := os.Getenv("LOKI_HUB_SERVICES_URL")
	if lokihubServicesURL == "" {
		fmt.Println("LOKI_HUB_SERVICES_URL environment variable must be set")
		return
	}

	client := &http.Client{Timeout: 10 * time.Second}
	url := fmt.Sprintf("%s/currencies.json", lokihubServicesURL)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		fmt.Printf("Error creating request: %v\n", err)
		return
	}
	req.Header.Set("User-Agent", "Lokihub")
	// raw github content returns text/plain mostly but json content
	// req.Header.Set("Content-Type", "application/json")

	fmt.Printf("Fetching %s...\n", url)
	res, err := client.Do(req)
	if err != nil {
		fmt.Printf("Failed to fetch rates: %v\n", err)
		return
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		fmt.Printf("Failed to read body: %v\n", err)
		return
	}

	fmt.Printf("Status Code: %d\n", res.StatusCode)

	if res.StatusCode >= 300 {
		fmt.Printf("Error Response Body: %s\n", string(body))
		return
	}

	var currencies map[string]LokiCurrency
	err = json.Unmarshal(body, &currencies)
	if err != nil {
		fmt.Printf("Failed to decode JSON: %v\n", err)
		fmt.Printf("Body snippet: %s\n", string(body)[:100])
		return
	}

	fmt.Printf("Successfully fetched %d currencies.\n", len(currencies))
	for k, v := range currencies {
		fmt.Printf("- %s: %s (%s)\n", k, v.Name, v.Symbol)
		break // just show one
	}
}
