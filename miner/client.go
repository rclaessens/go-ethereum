package miner

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/edgelesssys/ego/attestation"
	"github.com/edgelesssys/ego/eclient"
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

func createTLSConfig(signer []byte) (*tls.Config, error) {
	verifyReport := func(report attestation.Report) error {
		
		if report.SecurityVersion < 2 {
			return errors.New("invalid security version")
		}
		if binary.LittleEndian.Uint16(report.ProductID) != 1234 {
			return errors.New("invalid product")
		}
		if !bytes.Equal(report.SignerID, signer) {
			return errors.New("invalid signer")
		}
		// Add verifications
		return nil
	}

	// Create a TLS config that verifies a certificate with embedded report.
	tlsConfig := eclient.CreateAttestationClientTLSConfig(verifyReport)
	return tlsConfig, nil
}

// encodeEnvironmentToJson converts the Environment struct to a JSON string.
func encodeEnvironmentToJson(transactions []*types.Transaction, env *Environment) ([]byte, error) {
	if len(transactions) == 0 {
		log.Info("No transactions to encode")
		return nil, nil
	}

	clientEnv := &Environment{
        Coinbase: env.Coinbase,
        Header:   env.Header,
    }

	res, err := json.Marshal(struct {
        Transactions []*types.Transaction `json:"transactions"`
        Env          *Environment         `json:"env"`
    }{
        Transactions: transactions,
        Env:      clientEnv,
    })
    if err != nil {
        log.Error("Failed to encode environment to JSON", "err", err)
        return nil, err
    }
	log.Info("Encoded transactions to JSON", "json", string(res))

	return res, nil
}

// tlsCallToServer makes a secure HTTP call to the server, sending the JSON-encoded Environment
// and returns the JSON response from the server.
func (miner *Miner) tlsCallToServer(envJson []byte, env *Environment) ([]byte, error) {
	// URL of the server endpoint
	url := "http://localhost:8080"

	dummySignerID := "dummysignerid1234567890abcdef"
	signer, _ := hex.DecodeString(dummySignerID)


	// Create a TLS config for secure communication
	tlsConfig, err := createTLSConfig(signer)
	if err != nil {
		return nil, err
	}

	// Create a new HTTP client with the TLS configuration
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
		Timeout: 10 * time.Second, // Set an appropriate timeout
	}

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
	var respMessage clientResponse
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

	// var receipts []*types.Receipt
	for _, sm := range stateModifications {
		pre := sm.Pre
		post := sm.Post
		updates := comparePrePostStates(pre, post)
		env.State = miner.updateState(updates, env.State)
		env.Txs = append(env.Txs, sm.Tx)
		env.Tcount++
		env.Receipts = append(env.Receipts, sm.Receipt)
		env.Header.GasUsed += sm.Receipt.GasUsed
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

	log.Info("Updated state successfully")	
	return respBody, nil
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
