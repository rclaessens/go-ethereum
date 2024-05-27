package miner

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

// encodeEnvironmentToJson converts the Environment struct to a JSON string.
func encodeEnvironmentToJson(transactions []*types.Transaction) ([]byte, error) {
	if len(transactions) == 0 {
		log.Info("No transactions to encode")
		return nil, nil
	}
	var marshaledTxs []json.RawMessage
	for _, tx := range transactions {
		marshaledTx, err := tx.MarshalJSON()
		log.Info("Marshaled transaction to JSON", "json", string(marshaledTx))
		if err != nil {
			return nil, err
		}
		marshaledTxs = append(marshaledTxs, marshaledTx)
	}
	marshaledTxsJson, err := json.Marshal(marshaledTxs)
	if err != nil {
		return nil, err
	}
	log.Info("Encoded transactions to JSON", "json", string(marshaledTxsJson))
	return marshaledTxsJson, nil
}

// decodeJsonToEnvironment converts a JSON string back into an Environment struct.
func decodeJsonToEnvironment(jsonData []byte) ([]*types.Transaction, error) {
	var MarshaledTxs [][]byte
	err := json.Unmarshal(jsonData, &MarshaledTxs)
	if err != nil {
		return nil, err
	}
	var transactions []*types.Transaction
	for _, marshaledTx := range MarshaledTxs {
		var tx types.Transaction
		err := tx.UnmarshalJSON(marshaledTx)
		if err != nil {
			return nil, err
		}
		transactions = append(transactions, &tx)
	}
	return transactions, nil
}

// tlsCallToServer makes a secure HTTP call to the server, sending the JSON-encoded Environment
// and returns the JSON response from the server.
func tlsCallToServer(envJson []byte) ([]byte, error) {
	// URL of the server endpoint
	url := "http://localhost:8080"

	// Create a new HTTP client with default settings
	client := &http.Client{}

	// Create a new POST request with the JSON data
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(envJson))
	log.Info("Sending request to server", "url", url, "body", string(envJson))
	if err != nil {
		return nil, err
	}
	

	// Set the appropriate HTTP headers for JSON content
	req.Header.Set("Content-Type", "application/json")

	// Execute the HTTP request
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Read the response body using io.ReadAll
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	log.Info("Received response from server", "status", resp.Status, "body", string(respBody))

	// Return the body as a string
	return respBody, nil
}