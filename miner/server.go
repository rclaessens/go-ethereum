package miner

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/holiman/uint256"
)

type clientData struct {
	Transactions []*types.Transaction `json:"transactions"`
	Env 		*Environment          `json:"env"`
}

type clientResponse struct{
	Results []json.RawMessage `json:"results"`
	Env    *Environment      `json:"env"`
}

func decodeFromJSON (jsonData []byte) ([]*types.Transaction, *Environment, error){
	log.Info("Received JSON data", "data", string(jsonData))
	var clientData clientData
	err := json.Unmarshal(jsonData, &clientData)
	if err != nil {
		return nil, nil, err
	}

	log.Info("Received Transactions", "number", len(clientData.Transactions))

	return clientData.Transactions, clientData.Env, nil
}

func (miner *Miner) Handler (w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	log.Info("Test time", "ID", 3, "Block id", nil, "timestamp", time.Now().Format("2006-01-02T15:04:05.000000000"))
	log.Info("Received request from client")

	body, err := io.ReadAll(r.Body)
	log.Info("Received request body", "body", string(body))
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusInternalServerError)
		return
	}
	MarkMinerIngress(int64(len(body)))
	transactions, env, err := decodeFromJSON(body)
	if err != nil {
		log.Error("Failed to decode JSON", "err", err)
		http.Error(w, "Failed to decode JSON", http.StatusBadRequest) // TO DO : because it fails
		return
	}
	env.Signer = types.MakeSigner(miner.chainConfig, env.Header.Number, env.Header.Time)
	env.State, err = miner.chain.State()
	if err != nil {
		log.Error("Failed to get state", "err", err)
		http.Error(w, "Failed to get state", http.StatusInternalServerError)
		return
	}
	stateModifications, env, err := miner.processTransactions(transactions, env)
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
	log.Info("Test time", "ID", 4, "Block id", nil, "timestamp", time.Now().Format("2006-01-02T15:04:05.000000000"))
	var responseBuffer bytes.Buffer
	if err := json.NewEncoder(&responseBuffer).Encode(clientResponse); err != nil {
		http.Error(w, "Error encoding response JSON", http.StatusInternalServerError)
		return
	}

	// Write the response to the client
	n, err := w.Write(responseBuffer.Bytes())
	if err != nil {
		http.Error(w, "Error sending response", http.StatusInternalServerError)
		return
	}

	// Mark the egress meter with the number of bytes sent
	MarkMinerEgress(int64(n))
}

func (miner *Miner) processTransactions (tx []*types.Transaction, env *Environment) ([]json.RawMessage, *Environment, error) {
	interrupt := new(atomic.Int32)
	timer := time.AfterFunc(miner.config.Recommit, func() {
		interrupt.Store(commitInterruptTimeout)
	})
	defer timer.Stop()
	
	txs := convertTransactionsToLazy(tx)
	clientplainTxs := convertToAddressMap(txs, env.Signer)
	clientblobTxs := map[common.Address][]*txpool.LazyTransaction{}

	plainTxs := newTransactionsByPriceAndNonce(env.Signer, clientplainTxs, env.Header.BaseFee)
	blobTxs := newTransactionsByPriceAndNonce(env.Signer, clientblobTxs, env.Header.BaseFee)

	result, err := miner.commitTransactions(env, plainTxs, blobTxs, interrupt)
	if err != nil {
		return nil, nil, err
	}
	return result, env, nil
}

func convertTransactionToLazy(tx *types.Transaction) *txpool.LazyTransaction {
    // Create a new LazyTransaction
    lazyTx := &txpool.LazyTransaction{
        Tx:        tx,
        Hash:      tx.Hash(),
        Time:      time.Now(), // Set the time as now or fetch from a relevant field if available
        GasFeeCap: new(uint256.Int).SetUint64(tx.GasFeeCap().Uint64()),
        GasTipCap: new(uint256.Int).SetUint64(tx.GasTipCap().Uint64()),
        Gas:       tx.Gas(),
        BlobGas:   0, // Set to 0 or a relevant value if available
    }
	if(tx.Type() == types.BlobTxType){
		lazyTx.BlobGas = tx.BlobGas()
	}

    return lazyTx
}

func convertTransactionsToLazy(txs []*types.Transaction) []*txpool.LazyTransaction {
    var lazyTxs []*txpool.LazyTransaction
    for _, tx := range txs {
        lazyTx := convertTransactionToLazy(tx)
        lazyTxs = append(lazyTxs, lazyTx)
    }
    return lazyTxs
}

func convertToAddressMap(transactions []*txpool.LazyTransaction, signer types.Signer) map[common.Address][]*txpool.LazyTransaction {
    // Initialize the map to hold the transactions grouped by sender address
    addressMap := make(map[common.Address][]*txpool.LazyTransaction)

    // Iterate over each lazy transaction
    for _, lazyTx := range transactions {
		if lazyTx.Tx == nil {
            log.Error("LazyTransaction has nil Tx", "lazyTx", lazyTx)
            continue
        }

        // Retrieve the sender address from the transaction
        sender, err := types.Sender(signer, lazyTx.Tx)
        if err != nil {
            log.Error("Failed to retrieve sender address", "err", err)
            continue
        }

        // Append the transaction to the appropriate slice in the map
        addressMap[sender] = append(addressMap[sender], lazyTx)
    }

    return addressMap
}