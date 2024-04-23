package miner

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/txpool"
)

type ServerPayload struct {
	Env             *Environment
	LocalPlainTxs   map[common.Address][]*txpool.LazyTransaction
	LocalBlobTxs    map[common.Address][]*txpool.LazyTransaction
	RemotePlainTxs  map[common.Address][]*txpool.LazyTransaction
	RemoteBlobTxs   map[common.Address][]*txpool.LazyTransaction
	Interrupt 	    *atomic.Int32
}

// encodeEnvironmentToJson converts the Environment struct to a JSON string.
func encodeEnvironmentToJson(payload *ServerPayload) (string, error) {
	if payload == nil {
		return "", errors.New("server payload is nil")
	}
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
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
	url := "https://localhost:8080"

	// Create a new HTTP client with default settings
	client := &http.Client{}

	// Create a new POST request with the JSON data
	req, err := http.NewRequest("POST", url, bytes.NewBufferString(envJson))
	if err != nil {
		return "", err
	}

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

	// Return the body as a string
	return string(respBody), nil
}