package miner

import (
	"encoding/json"
	"fmt"
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

func decodeFromJSON (jsonData string) (*ServerPayload, error){
	var payload ServerPayload
	err := json.Unmarshal([]byte(jsonData), &payload)
	if err != nil {
		return nil, err
	}
	return &payload, nil
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

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusInternalServerError)
		return
	}

	payload, err := decodeFromJSON(string(body))
	if err != nil {
		http.Error(w, "Failed to decode JSON", http.StatusBadRequest)
		return
	}

	err = miner.processPayload(payload)
	if err != nil {
		http.Error(w, "Failed to process payload", http.StatusInternalServerError)
		return
	}

	responseJSON, err := encodeToJSON(payload)
	if err != nil {
		http.Error(w, "Error encoding response JSON", http.StatusInternalServerError)
		return
	}

	// Send back the updated payload
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, responseJSON)
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