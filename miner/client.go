package miner

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/holiman/uint256"
)

type stateMap = map[common.Address]*account

// Problem with UnmarshalJSON for big.Int 
type BigInt struct {
	big.Int
}

func (b *BigInt) UnmarshalJSON(data []byte) error {
	str := strings.Trim(string(data), "\"")
	if str == "null" {
		return nil
	}

	// Handle hex and decimal formats
	_, ok := b.SetString(str, 0)
	if !ok {
		return errors.New("invalid big integer format")
	}
	return nil
}

type account struct {
	Balance *BigInt                    `json:"balance,omitempty"`
	Code    []byte                      `json:"code,omitempty"`
	Nonce   uint64                      `json:"nonce,omitempty"`
	Storage map[common.Hash]common.Hash `json:"storage,omitempty"`
}


type stateModification struct {
	Pre 	stateMap `json:"pre"`
	Post    stateMap `json:"post"`
	Tx 	    *types.Transaction `json:"tx"`
	Receipt *types.Receipt `json:"receipt"`
}

type serverResponse struct {
	Results []json.RawMessage `json:"results"`
	Env     *Environment      `json:"env"`
}

func encodeEnvironmentToJson(plainTxs, blobTxs *transactionsByPriceAndNonce, env *Environment) ([]byte, error) {
    if len(plainTxs.txs) == 0 && len(blobTxs.txs) == 0 {
        log.Info("No transactions to encode")
        return nil, nil
    }

    clientEnv := &Environment{
        Coinbase: env.Coinbase,
        Header:   env.Header,
    }

    res, err := json.Marshal(struct {
        PlainTxs *transactionsByPriceAndNonce `json:"plainTxs"`
        BlobTxs  *transactionsByPriceAndNonce `json:"blobTxs"`
        Env      *Environment                 `json:"env"`
    }{
        PlainTxs: plainTxs,
        BlobTxs:  blobTxs,
        Env:      clientEnv,
    })
    if err != nil {
        log.Error("Failed to encode environment to JSON", "err", err)
        return nil, err
    }
    return res, nil
}

// tlsCallToServer makes a secure HTTP call to the server, sending the JSON-encoded Environment
// and returns the JSON response from the server.
func (miner *Miner) tlsCallToServer(plainTxs, blobTxs *transactionsByPriceAndNonce, env *Environment) ([]byte, *Environment, error) {
	// URL of the server endpoint
	url := "http://localhost:8080"

	// Create a new HTTP client with default settings
	client := &http.Client{}

	res, err := encodeEnvironmentToJson(plainTxs, blobTxs, env)
	if err != nil {
		return nil, nil, err
	}
	if res == nil {
		return nil, nil, nil
	}
	// Create a new POST request with the JSON data
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(res))
	log.Info("Sending request to server", "url", url, "body", string(res))
	if err != nil {
		return nil, nil, err
	}
	

	// Set the appropriate HTTP headers for JSON content
	req.Header.Set("Content-Type", "application/json")

	// Execute the HTTP request
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	// Read the response body using io.ReadAll
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	log.Info("Received response from server", "status", resp.Status, "body", string(respBody))
	var respMessage serverResponse
	if err := json.Unmarshal(respBody, &respMessage); err != nil {
		log.Error("Failed to decode JSON response: %v", err)
	}

	var stateModifications []stateModification
	for _, stateModif := range respMessage.Results {
		var sm stateModification
		if err := json.Unmarshal(stateModif, &sm); err != nil {
			log.Error("Failed to decode state modification: %v", err)
		}
		stateModifications = append(stateModifications, sm)
	}
	newState, err := miner.chain.State()
	if err != nil {
		log.Error("Failed to get state", "err", err)
		return nil, nil, err
	}
	for _, sm := range stateModifications {
		pre := sm.Pre
		post := sm.Post
		updates := comparePrePostStates(pre, post)
		newState = miner.updateState(updates, newState)
		// for addr, acc := range updates {
		// 	log.Info("Address", "address", addr.Hex())
		// 	if acc.Balance != nil {
		// 		log.Info("  Balance", "balance", acc.Balance.String())
		// 	}
		// 	if acc.Nonce != 0 {
		// 		log.Info("  Nonce", "nonce", acc.Nonce)
		// 	}
		// 	if acc.Code != nil {
		// 		log.Info("  Code", "code", acc.Code)
		// 	}
		// 	if acc.Storage != nil {
		// 		log.Info("  Storage", "storage", acc.Storage)
		// 	}
		// }
	}

	respMessage.Env.Signer = types.MakeSigner(miner.chainConfig, respMessage.Env.Header.Number, respMessage.Env.Header.Time)
	respMessage.Env.State = newState

	log.Info("Updated state successfully")	
	return respBody, respMessage.Env, nil
}

func comparePrePostStates(pre, post stateMap) map[common.Address]account {
	updates := make(map[common.Address]account)

	for addr, postAccount := range post {
		preAccount, exists := pre[addr]

		// Initialize the account update entry if not already initialized
		if _, exists := updates[addr]; !exists {
			updates[addr] = account{
				Balance: &BigInt{*new(big.Int)},
				Storage: make(map[common.Hash]common.Hash),
			}
		}
		updateAccount := updates[addr]

		// If the account does not exist in the pre state, it has been created
		if !exists {
			updates[addr] = *postAccount
			continue
		}

		// Check for balance changes
		if postAccount.Balance.Cmp(&preAccount.Balance.Int) != 0 {
			updateAccount.Balance = postAccount.Balance
		}

		// Check for nonce changes
		if postAccount.Nonce != preAccount.Nonce {
			updateAccount.Nonce = postAccount.Nonce
		}

		// Check for code changes
		if !bytes.Equal(postAccount.Code, preAccount.Code) {
			updateAccount.Code = postAccount.Code
		}

		// Check for storage changes
		for key, postValue := range postAccount.Storage {
			if preValue, exists := preAccount.Storage[key]; !exists || postValue != preValue {
				updateAccount.Storage[key] = postValue
			}
		}

		// Reassign the modified account back to the map
		updates[addr] = updateAccount
	}

	// Check for account deletions
	for addr := range pre {
		if _, exists := post[addr]; !exists {
			updates[addr] = account{
				Balance: &BigInt{*new(big.Int)},
				Nonce:   0,
				Code:    nil,
				Storage: map[common.Hash]common.Hash{},
			}
		}
	}
	return updates
}

func (miner *Miner)updateState(updates map[common.Address]account, state *state.StateDB)(*state.StateDB) {
	for addr, acc := range updates {
		if acc.Balance != nil {
			amount, _ := uint256.FromBig(&acc.Balance.Int)
			state.SetBalance(addr, amount, tracing.BalanceChangeUnspecified)
		}
		if acc.Nonce != 0 {
			state.SetNonce(addr, acc.Nonce)
		}
		if acc.Code != nil {
			state.SetCode(addr, acc.Code)
		}
		for key, val := range acc.Storage {
			state.SetState(addr, key, val)
		}
	}
	return state
}
