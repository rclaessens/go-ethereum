package miner

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/ethereum/go-ethereum/log"
)

// encodeEnvironmentToJson converts the Environment struct to a JSON string.
func encodeEnvironmentToJson(payload *ServerPayload) (string, error) {
	if payload == nil {
		return "", errors.New("server payload is nil")
	}
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	log.Info("Encoded environment to JSON", "json", string(jsonData))
	return string(jsonData), nil
}

// decodeJsonToEnvironment converts a JSON string back into an Environment struct.
func decodeJsonToEnvironment(jsonData string) (*ServerPayload, error) {
	var payload ServerPayload
	err := json.Unmarshal([]byte(jsonData), &payload)
	if err != nil {
		return nil, err
	}
	return &payload, nil
}

// tlsCallToServer makes a secure HTTP call to the server, sending the JSON-encoded Environment
// and returns the JSON response from the server.
func tlsCallToServer(envJson string) (string, error) {
	// URL of the server endpoint
	url := "http://localhost:8080"

	// Create a new HTTP client with default settings
	client := &http.Client{}

	// Create a new POST request with the JSON data
	req, err := http.NewRequest("POST", url, bytes.NewBufferString(envJson))
	if err != nil {
		return "", err
	}
	log.Info("Sending request to server", "url", url)

	// Set the appropriate HTTP headers for JSON content
	req.Header.Set("Content-Type", "application/json")

	// Execute the HTTP request
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	// Read the response body using io.ReadAll
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	log.Info("Received response from server", "status", resp.Status, "body", string(respBody))

	// Return the body as a string
	return string(respBody), nil
}