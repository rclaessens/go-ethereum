package miner

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
)

// LazyTransactionWrapper wraps txpool.LazyTransaction
type LazyTransactionWrapper struct {
	lt txpool.LazyTransaction
}

// LazyTransactionJSON is a helper struct for JSON marshalling and unmarshalling.
type LazyTransactionJSON struct {
	Hash      common.Hash        `json:"hash"`
	Tx        *types.Transaction `json:"tx"`
	Time      time.Time          `json:"time"`
	GasFeeCap string             `json:"gasFeeCap"`
	GasTipCap string             `json:"gasTipCap"`
	Gas       uint64             `json:"gas"`
	BlobGas   uint64             `json:"blobGas"`
}

// MarshalJSON custom marshaller for LazyTransactionWrapper.
func (ltw *LazyTransactionWrapper) MarshalJSON() ([]byte, error) {
	return json.Marshal(&LazyTransactionJSON{
		Hash:      ltw.lt.Hash,
		Tx:        ltw.lt.Tx,
		Time:      ltw.lt.Time,
		GasFeeCap: ltw.lt.GasFeeCap.Hex(),
		GasTipCap: ltw.lt.GasTipCap.Hex(),
		Gas:       ltw.lt.Gas,
		BlobGas:   ltw.lt.BlobGas,
	})
}

// UnmarshalJSON custom unmarshaller for LazyTransactionWrapper.
func (ltw *LazyTransactionWrapper) UnmarshalJSON(data []byte) error {
	var temp LazyTransactionJSON
	if err := json.Unmarshal(data, &temp); err != nil {
		return err
	}

	gasFeeCap, err := uint256.FromHex(temp.GasFeeCap)
	if err != nil {
		return errors.New("invalid gasFeeCap value")
	}

	gasTipCap, err := uint256.FromHex(temp.GasTipCap)
	if err != nil {
		return errors.New("invalid gasTipCap value")
	}

	ltw.lt.Hash = temp.Hash
	ltw.lt.Tx = temp.Tx
	ltw.lt.Time = temp.Time
	ltw.lt.GasFeeCap = gasFeeCap
	ltw.lt.GasTipCap = gasTipCap
	ltw.lt.Gas = temp.Gas
	ltw.lt.BlobGas = temp.BlobGas

	return nil
}

// transactionsByPriceAndNonceWrapper wraps transactionsByPriceAndNonce for custom JSON marshalling/unmarshalling.
type transactionsByPriceAndNonceWrapper struct {
	Txs     map[common.Address][]*LazyTransactionWrapper `json:"txs"`
	Heads   txByPriceAndTime                             `json:"heads"`
	BaseFee string                                       `json:"baseFee"`
}

// MarshalJSON custom marshaller for transactionsByPriceAndNonce.
func (tpn *transactionsByPriceAndNonce) MarshalJSON() ([]byte, error) {
	wrapper := transactionsByPriceAndNonceWrapper{
		Txs:     make(map[common.Address][]*LazyTransactionWrapper),
		Heads:   tpn.heads,
		BaseFee: tpn.baseFee.Hex(),
	}

	for addr, txs := range tpn.txs {
		for _, tx := range txs {
			wrapper.Txs[addr] = append(wrapper.Txs[addr], &LazyTransactionWrapper{*tx})
		}
	}

	return json.Marshal(wrapper)
}

// UnmarshalJSON custom unmarshaller for transactionsByPriceAndNonce.
func (tpn *transactionsByPriceAndNonce) UnmarshalJSON(data []byte) error {
	var wrapper transactionsByPriceAndNonceWrapper
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return err
	}

	baseFee, err := uint256.FromHex(wrapper.BaseFee)
	if err != nil {
		return errors.New("invalid baseFee value")
	}

	tpn.txs = make(map[common.Address][]*txpool.LazyTransaction)
	tpn.heads = wrapper.Heads
	tpn.baseFee = baseFee

	for addr, txs := range wrapper.Txs {
		for _, tx := range txs {
			tpn.txs[addr] = append(tpn.txs[addr], &tx.lt)
		}
	}

	// Signer will be set later
	tpn.signer = nil

	return nil
}
