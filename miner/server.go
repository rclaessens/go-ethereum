package miner

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

func decodeFromJSON (jsonData []byte) ([]*types.Transaction, error){
	log.Info("Received JSON data", "data", string(jsonData))
	var marshaledTxs []json.RawMessage
	err := json.Unmarshal(jsonData, &marshaledTxs)
	if err != nil {
		return nil, err
	}

	var transactions []*types.Transaction
	for _, marshaledTx := range marshaledTxs {
		var tx types.Transaction
		err := tx.UnmarshalJSON([]byte(marshaledTx))
		if err != nil {
			return nil, err
		}
		transactions = append(transactions, &tx)
	}

	return transactions, nil
}

func encodeToJSON(payload *ServerPayload) (string, error){
	jsonData, err := json.Marshal(payload)
	if err != nil  {
		return "", err
	}

	return string(jsonData), nil
}

func (miner *Miner) Handler (w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	log.Info("Received request from client")

	body, err := io.ReadAll(r.Body)
	log.Info("Received request body", "body", string(body))
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusInternalServerError)
		return
	}

	payload, err := decodeFromJSON(body)
	if err != nil {
		log.Error("Failed to decode JSON", "err", err)
		http.Error(w, "Failed to decode JSON", http.StatusBadRequest) // TO DO : because it fails
		return
	}

	for _, tx := range payload {
		log.Info("Decoded transaction", "tx", tx.Nonce())
	}

	/*err = miner.processPayload(payload)
	if err != nil {
		http.Error(w, "Failed to process payload", http.StatusInternalServerError)
		return
	}
	
	log.Info("Processed payload successfully")

	responseJSON, err := encodeToJSON(payload)
	if err != nil {
		http.Error(w, "Error encoding response JSON", http.StatusInternalServerError)
		return
	}*/

	// Send back the updated payload
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, "Decoded payload", payload) // TO DO : change to responseJSON
}

func (miner *Miner) processPayload (payload *ServerPayload) error {
	// Fill the block with all available pending transactions.
	if len(payload.LocalPlainTxs) > 0 || len(payload.LocalBlobTxs) > 0 {
		plainTxs := newTransactionsByPriceAndNonce(payload.Env.Signer, payload.LocalPlainTxs, payload.Env.Header.BaseFee)
		blobTxs := newTransactionsByPriceAndNonce(payload.Env.Signer, payload.LocalBlobTxs, payload.Env.Header.BaseFee)

		if err := miner.commitTransactions(payload.Env, plainTxs, blobTxs, payload.Interrupt); err != nil {
			return err
		}
	}
	if len(payload.RemotePlainTxs) > 0 || len(payload.RemoteBlobTxs) > 0 {
		plainTxs := newTransactionsByPriceAndNonce(payload.Env.Signer, payload.RemotePlainTxs, payload.Env.Header.BaseFee)
		blobTxs := newTransactionsByPriceAndNonce(payload.Env.Signer, payload.RemoteBlobTxs, payload.Env.Header.BaseFee)

		if err := miner.commitTransactions(payload.Env, plainTxs, blobTxs, payload.Interrupt); err != nil {
			return err
		}
	}
	return nil
}