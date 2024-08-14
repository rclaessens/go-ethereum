package miner

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/holiman/uint256"
	"github.com/edgelesssys/ego/attestation"
	"github.com/edgelesssys/ego/attestation/tcbstatus"
	"github.com/edgelesssys/ego/eclient"
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

func verifyReport(reportBytes, certBytes, signer []byte) error {
	report, err := eclient.VerifyRemoteReport(reportBytes)
	if err == attestation.ErrTCBLevelInvalid {
		log.Warn("Warning: TCB level is invalid", "status", report.TCBStatus, "explanation", tcbstatus.Explain(report.TCBStatus))
		log.Info("Ignoring TCB level issue, because in development mode")
	} else if err != nil {
		return err
	}

	hash := sha256.Sum256(certBytes)
	if !bytes.Equal(report.Data[:len(hash)], hash[:]) {
		return errors.New("report data does not match the certificate's hash")
	}

	// You can either verify the UniqueID or the tuple (SignerID, ProductID, SecurityVersion, Debug).

	if report.SecurityVersion < 2 {
		return errors.New("invalid security version")
	}
	if binary.LittleEndian.Uint16(report.ProductID) != 1234 {
		return errors.New("invalid product")
	}
	if !bytes.Equal(report.SignerID, signer) {
		return errors.New("invalid signer")
	}

	// For production, you must also verify that report.Debug == false

	return nil
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

	// Retrieve the server's certificate from the /cert endpoint
	certURL := "https://localhost:8080/cert"
	// Create an HTTP client with a transport that ignores certificate verification for the initial request
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	resp, err := client.Get(certURL)
	if err != nil {
		log.Error("Failed to fetch certificate", "err", err)
		return nil, err
	}
	defer resp.Body.Close()

	// Read the certificate into memory
	certBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Error("Failed to read certificate", "err", err)
		return nil, err
	}

	log.Info("Received certificate from server", "cert", string(certBytes))

	// Parse the certificate from the bytes
	cert, err := x509.ParseCertificate(certBytes)
	if err != nil {
		log.Error("Failed to parse certificate", "err", err)
		return nil, err
	}

	// Configure TLS settings to use the server's certificate and skip verification
	tlsConfig := &tls.Config{
		RootCAs:            x509.NewCertPool(),
		InsecureSkipVerify: true, // Skip verification because the certificate is self-signed
	}
	tlsConfig.RootCAs.AddCert(cert)

	// Create an HTTPS client with the configured TLS settings
	client = &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}

	// URL of the server endpoint
	url := "https://localhost:8080"

	// Create a new POST request with the JSON data
	log.Info("Test time", "ID", 2, "Block id", nil, "timestamp", time.Now().Format("2006-01-02T15:04:05.000000000"))
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(envJson))
	log.Info("Sending request to server", "url", url, "body", string(envJson))
	if err != nil {
		return nil, err
	}
	

	// Set the appropriate HTTP headers for JSON content
	req.Header.Set("Content-Type", "application/json")

	log.Info("Len JSON", "len", len(envJson))	
	MarkMinerEgress(int64(len(envJson)))

	// Execute the HTTP request
	resp, err = client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Read the response body using io.ReadAll
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	MarkMinerIngress(int64(len(respBody)))
	log.Info("Test time", "ID", 5, "Block id", nil, "timestamp", time.Now().Format("2006-01-02T15:04:05.000000000"))
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

	miner.pendingMu.Lock()
    defer miner.pendingMu.Unlock()

	// var receipts []*types.Receipt
	for _, sm := range stateModifications {
		if sm.Receipt == nil {
			log.Error("Receipt is nil for transaction", "tx", sm.Tx)	
		} else {
			env.Header.GasUsed += sm.Receipt.GasUsed
			if env.Header.GasUsed > env.Header.GasLimit {
				log.Warn("Gas limit exceeded; excluding transaction", "tx", sm.Tx, "gasUsed", env.Header.GasUsed, "gasLimit", env.Header.GasLimit)
				// Remove gas used
				env.Header.GasUsed -= sm.Receipt.GasUsed
				continue
			}
			env.Txs = append(env.Txs, sm.Tx)
			env.Tcount++
			env.Receipts = append(env.Receipts, sm.Receipt)
			
			pre := sm.Pre
			post := sm.Post
			updates := comparePrePostStates(pre, post)
			env.State = miner.updateState(updates, env.State)
		}
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
