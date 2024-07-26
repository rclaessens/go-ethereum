package miner

import (
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

type clientTxs struct {
	PlainTxs *transactionsByPriceAndNonce `json:"plainTxs"`
	BlobTxs  *transactionsByPriceAndNonce `json:"blobTxs"`
	Env 	 *Environment 				  `json:"env"`
}

type clientResponse struct{
	Results []json.RawMessage `json:"results"`
	Env    *Environment      `json:"env"`
}

func decodeFromJSON (jsonData []byte) (*transactionsByPriceAndNonce, *transactionsByPriceAndNonce, *Environment, error){
	log.Info("Received JSON data", "data", string(jsonData))
	var clientTxs clientTxs
	err := json.Unmarshal(jsonData, &clientTxs)
	if err != nil {
		return nil, nil, nil, err
	}

	return clientTxs.PlainTxs, clientTxs.BlobTxs, clientTxs.Env, nil
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

	plainTxs, blobTxs, env, err := decodeFromJSON(body)
	if err != nil {
		log.Error("Failed to decode JSON", "err", err)
		http.Error(w, "Failed to decode JSON", http.StatusBadRequest) // TO DO : because it fails
		return
	}
	log.Info("Block number", "number", env.Header.Number)
	env.Signer = types.MakeSigner(miner.chainConfig, env.Header.Number, env.Header.Time)
	env.State, err = miner.chain.State()
	if err != nil {
		log.Error("Failed to get state", "err", err)
		http.Error(w, "Failed to get state", http.StatusInternalServerError)
		return
	}
	stateModifications, env, err := miner.processTransactions(plainTxs, blobTxs, env)
	if err != nil {
		log.Error("Failed to process transactions", "err", err)
		http.Error(w, "Failed to process transactions", http.StatusInternalServerError)
		return
	}
	
	log.Info("Processed transactions successfully")
	clientEnv := &Environment{
		Coinbase: env.Coinbase,
		Header:   env.Header,
	}
	clientResponse := clientResponse{
		Results: stateModifications,
		Env:     clientEnv,
	}

	// Send back the updated payload
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(clientResponse); err != nil {
		http.Error(w, "Error encoding response JSON", http.StatusInternalServerError)
		return
	}
}

func (miner *Miner) processTransactions (plainTxs, blobTxs *transactionsByPriceAndNonce, env *Environment) ([]json.RawMessage, *Environment, error) {
	interrupt := new(atomic.Int32)
	timer := time.AfterFunc(miner.config.Recommit, func() {
		interrupt.Store(commitInterruptTimeout)
	})
	defer timer.Stop()
	// stateModifications := []json.RawMessage{}
	result, err := miner.commitTransactions(env, plainTxs, blobTxs, interrupt)
	if err != nil {
		return nil, nil, err
	}
	// stateModifications = append(stateModifications, result)
	return result, env, nil
}