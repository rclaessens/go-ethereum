package miner

import (
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
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

	err = miner.processPayload(payload)
	if err != nil {
		http.Error(w, "Failed to process payload", http.StatusInternalServerError)
		return
	}
	
	log.Info("Processed payload successfully")

	/*responseJSON, err := encodeToJSON(payload)
	if err != nil {
		http.Error(w, "Error encoding response JSON", http.StatusInternalServerError)
		return
	}*/

	// Send back the updated payload
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, "Decoded payload", payload) // TO DO : change to responseJSON
}

func (miner *Miner) processPayload (tx []*types.Transaction) error {
	parent := miner.chain.CurrentBlock()
	timestamp  := uint64(time.Now().Unix())
	withdrawal := types.Withdrawals{}
	genParams := &generateParams{
		timestamp:   timestamp,
		forceTime:   false,
		parentHash:  parent.Hash(),
		coinbase:	 miner.config.PendingFeeRecipient,
		random:      common.Hash{},
		withdrawals: withdrawal,
		beaconRoot:  parent.ParentBeaconRoot,
		noTxs:       false,
	}
	header := &types.Header{
		ParentHash: parent.Hash(),
		Number:     new(big.Int).Add(parent.Number, common.Big1),
		GasLimit:   core.CalcGasLimit(parent.GasLimit, miner.config.GasCeil),
		Time:       genParams.timestamp,
		Coinbase:   genParams.coinbase,
		Difficulty: parent.Difficulty,
		BaseFee:    parent.BaseFee,
	}
	env, err := miner.makeEnv(parent,header,genParams.coinbase)
	if err != nil {
		return err
	}
	if env.GasPool == nil {
		env.GasPool = new(core.GasPool).AddGas(header.GasLimit)
	}
	// func ApplyTransaction(config *params.ChainConfig, bc ChainContext, author *common.Address, gp *GasPool, statedb *state.StateDB, header *types.Header, tx *types.Transaction, usedGas *uint64, cfg vm.Config) (*types.Receipt, error)
	for _, tx := range tx {
		_, err := miner.applyTransaction(env, tx)
		if err != nil {
			return err
		}
	}
	return nil
}