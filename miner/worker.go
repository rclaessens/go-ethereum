// Copyright 2015 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package miner

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/misc/eip1559"
	"github.com/ethereum/go-ethereum/consensus/misc/eip4844"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth/tracers"
	"github.com/ethereum/go-ethereum/eth/tracers/native"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

var (
	errBlockInterruptedByNewHead  = errors.New("new head arrived while building block")
	errBlockInterruptedByRecommit = errors.New("recommit interrupt while building block")
	errBlockInterruptedByTimeout  = errors.New("timeout while building block")
)

// Environment is the worker's current Environment and holds all
// information of the sealing block generation.
type Environment struct {
	Signer   types.Signer
	State    *state.StateDB // apply state changes here
	Tcount   int            // tx count in cycle
	GasPool  *core.GasPool  // available gas used to pack transactions
	Coinbase common.Address

	Header   *types.Header
	Txs      []*types.Transaction
	Receipts []*types.Receipt
	Sidecars []*types.BlobTxSidecar
	Blobs    int
}

const (
	commitInterruptNone int32 = iota
	commitInterruptNewHead
	commitInterruptResubmit
	commitInterruptTimeout
)

// newPayloadResult is the result of payload generation.
type newPayloadResult struct {
	err      error
	block    *types.Block
	fees     *big.Int               // total block fees
	sidecars []*types.BlobTxSidecar // collected blobs of blob transactions
	stateDB  *state.StateDB         // StateDB after executing the transactions
	receipts []*types.Receipt       // Receipts collected during construction
}

// generateParams wraps various settings for generating sealing task.
type generateParams struct {
	timestamp   uint64            // The timestamp for sealing task
	forceTime   bool              // Flag whether the given timestamp is immutable or not
	parentHash  common.Hash       // Parent block hash, empty means the latest chain head
	coinbase    common.Address    // The fee recipient address for including transaction
	random      common.Hash       // The randomness generated by beacon chain, empty before the merge
	withdrawals types.Withdrawals // List of withdrawals to include in block (shanghai field)
	beaconRoot  *common.Hash      // The beacon root (cancun field).
	noTxs       bool              // Flag whether an empty block without any transaction is expected
}

// generateWork generates a sealing block based on the given parameters.
func (miner *Miner) generateWork(params *generateParams) *newPayloadResult {
	work, err := miner.prepareWork(params)
	if err != nil {
		return &newPayloadResult{err: err}
	}
	if !params.noTxs {
		interrupt := new(atomic.Int32)
		timer := time.AfterFunc(miner.config.Recommit, func() {
			interrupt.Store(commitInterruptTimeout)
		})
		defer timer.Stop()

		err := miner.fillTransactions(interrupt, work)
		if errors.Is(err, errBlockInterruptedByTimeout) {
			log.Warn("Block building is interrupted", "allowance", common.PrettyDuration(miner.config.Recommit))
		}
	}
	body := types.Body{Transactions: work.Txs, Withdrawals: params.withdrawals}
	if(len(work.Txs) > 0){
		log.Info("Block Header Information",
    	"GasLimit", work.Header.GasLimit,
    	"GasUsed", work.Header.GasUsed,)
	}
	block, err := miner.engine.FinalizeAndAssemble(miner.chain, work.Header, work.State, &body, work.Receipts)
	if err != nil {
		return &newPayloadResult{err: err}
	}
	return &newPayloadResult{
		block:    block,
		fees:     totalFees(block, work.Receipts),
		sidecars: work.Sidecars,
		stateDB:  work.State,
		receipts: work.Receipts,
	}
}

// prepareWork constructs the sealing task according to the given parameters,
// either based on the last chain head or specified parent. In this function
// the pending transactions are not filled yet, only the empty task returned.
func (miner *Miner) prepareWork(genParams *generateParams) (*Environment, error) {
	miner.confMu.RLock()
	defer miner.confMu.RUnlock()

	// Find the parent block for sealing task
	parent := miner.chain.CurrentBlock()
	if genParams.parentHash != (common.Hash{}) {
		block := miner.chain.GetBlockByHash(genParams.parentHash)
		if block == nil {
			return nil, errors.New("missing parent")
		}
		parent = block.Header()
	}
	// Sanity check the timestamp correctness, recap the timestamp
	// to parent+1 if the mutation is allowed.
	timestamp := genParams.timestamp
	if parent.Time >= timestamp {
		if genParams.forceTime {
			return nil, fmt.Errorf("invalid timestamp, parent %d given %d", parent.Time, timestamp)
		}
		timestamp = parent.Time + 1
	}
	// Construct the sealing block header.
	header := &types.Header{
		ParentHash: parent.Hash(),
		Number:     new(big.Int).Add(parent.Number, common.Big1),
		GasLimit:   core.CalcGasLimit(parent.GasLimit, miner.config.GasCeil),
		Time:       timestamp,
		Coinbase:   genParams.coinbase,
	}
	// Set the extra field.
	if len(miner.config.ExtraData) != 0 {
		header.Extra = miner.config.ExtraData
	}
	// Set the randomness field from the beacon chain if it's available.
	if genParams.random != (common.Hash{}) {
		header.MixDigest = genParams.random
	}
	// Set baseFee and GasLimit if we are on an EIP-1559 chain
	if miner.chainConfig.IsLondon(header.Number) {
		header.BaseFee = eip1559.CalcBaseFee(miner.chainConfig, parent)
		if !miner.chainConfig.IsLondon(parent.Number) {
			parentGasLimit := parent.GasLimit * miner.chainConfig.ElasticityMultiplier()
			header.GasLimit = core.CalcGasLimit(parentGasLimit, miner.config.GasCeil)
		}
	}
	// Run the consensus preparation with the default or customized consensus engine.
	// Note that the `header.Time` may be changed.
	if err := miner.engine.Prepare(miner.chain, header); err != nil {
		log.Error("Failed to prepare header for sealing", "err", err)
		return nil, err
	}
	// Apply EIP-4844, EIP-4788.
	if miner.chainConfig.IsCancun(header.Number, header.Time) {
		var excessBlobGas uint64
		if miner.chainConfig.IsCancun(parent.Number, parent.Time) {
			excessBlobGas = eip4844.CalcExcessBlobGas(*parent.ExcessBlobGas, *parent.BlobGasUsed)
		} else {
			// For the first post-fork block, both parent.data_gas_used and parent.excess_data_gas are evaluated as 0
			excessBlobGas = eip4844.CalcExcessBlobGas(0, 0)
		}
		header.BlobGasUsed = new(uint64)
		header.ExcessBlobGas = &excessBlobGas
		header.ParentBeaconRoot = genParams.beaconRoot
	}
	// Could potentially happen if starting to mine in an odd state.
	// Note genParams.coinbase can be different with header.Coinbase
	// since clique algorithm can modify the coinbase field in header.
	env, err := miner.makeEnv(parent, header, genParams.coinbase)
	if err != nil {
		log.Error("Failed to create sealing context", "err", err)
		return nil, err
	}
	if header.ParentBeaconRoot != nil {
		context := core.NewEVMBlockContext(header, miner.chain, nil)
		vmenv := vm.NewEVM(context, vm.TxContext{}, env.State, miner.chainConfig, vm.Config{})
		core.ProcessBeaconBlockRoot(*header.ParentBeaconRoot, vmenv, env.State)
	}

	return env, nil
}

// makeEnv creates a new environment for the sealing block.
func (miner *Miner) makeEnv(parent *types.Header, header *types.Header, coinbase common.Address) (*Environment, error) {
	// Retrieve the parent state to execute on top and start a prefetcher for
	// the miner to speed block sealing up a bit.
	state, err := miner.chain.StateAt(parent.Root)
	if err != nil {
		return nil, err
	}
	log.Info("Making new environment", "parent", parent.Number, "header", header.Number)
	// Note the passed coinbase may be different with header.Coinbase.
	return &Environment{
		Signer:   types.MakeSigner(miner.chainConfig, header.Number, header.Time),
		State:    state,
		Coinbase: coinbase,
		Header:   header,
	}, nil
}

func (miner *Miner) commitTransaction(env *Environment, tx *types.Transaction) (json.RawMessage, error) {
	if tx.Type() == types.BlobTxType {
		return miner.commitBlobTransaction(env, tx)
	}
	receipt, result, err := miner.applyTransaction(env, tx)
	if err != nil {
		return nil, err
	}
	env.Txs = append(env.Txs, tx)
	env.Receipts = append(env.Receipts, receipt)
	env.Tcount++
	return result, nil
}

func (miner *Miner) commitBlobTransaction(env *Environment, tx *types.Transaction) (json.RawMessage, error) {
	sc := tx.BlobTxSidecar()
	if sc == nil {
		panic("blob transaction without blobs in miner")
	}
	// Checking against blob gas limit: It's kind of ugly to perform this check here, but there
	// isn't really a better place right now. The blob gas limit is checked at block validation time
	// and not during execution. This means core.ApplyTransaction will not return an error if the
	// tx has too many blobs. So we have to explicitly check it here.
	if (env.Blobs+len(sc.Blobs))*params.BlobTxBlobGasPerBlob > params.MaxBlobGasPerBlock {
		return nil, errors.New("max data blobs reached")
	}
	receipt, result, err := miner.applyTransaction(env, tx)
	if err != nil {
		return nil, err
	}
	env.Txs = append(env.Txs, tx.WithoutBlobTxSidecar())
	env.Receipts = append(env.Receipts, receipt)
	env.Sidecars = append(env.Sidecars, sc)
	env.Blobs += len(sc.Blobs)
	*env.Header.BlobGasUsed += receipt.BlobGasUsed
	env.Tcount++
	return result, nil
}

// applyTransaction runs the transaction. If execution fails, state and gas pool are reverted.
func (miner *Miner) applyTransaction(env *Environment, tx *types.Transaction) (*types.Receipt, json.RawMessage, error) {
	var (
		snap = env.State.Snapshot()
		gp   = env.GasPool.Gas()
		receipt *types.Receipt
		err error
	)

	if(miner.serverMode){
		// Initialize the prestate tracer
		tracer, err := initializePrestateTracer()
		if err != nil {
			log.Error("Failed to initialize prestate tracer", "err", err)
			return nil, nil, err
		}
	
		// Attach the tracer to the VM context
		vmConfig := vm.Config{
			Tracer: tracer.Hooks,
		}
	
		receipt, err = core.ApplyTransaction(miner.chainConfig, miner.chain, &env.Coinbase, env.GasPool, env.State, env.Header, tx, &env.Header.GasUsed, vmConfig)
		if err != nil {
			env.State.RevertToSnapshot(snap)
			env.GasPool.SetGas(gp)
		}
	
		// Get the tracer result
		result, tracerErr := tracer.GetResult()
		if tracerErr != nil {
			log.Error("Failed to get tracer result", "err", tracerErr)
		}

		return receipt, result, err

	} else {
		receipt, err = core.ApplyTransaction(miner.chainConfig, miner.chain, &env.Coinbase, env.GasPool, env.State, env.Header, tx, &env.Header.GasUsed, vm.Config{})
		if err != nil {
			env.State.RevertToSnapshot(snap)
			env.GasPool.SetGas(gp)
		}
	}

	return receipt, nil, err
}

func (miner *Miner) commitTransactions(env *Environment, plainTxs, blobTxs *transactionsByPriceAndNonce, interrupt *atomic.Int32) ([]json.RawMessage, error) {
	gasLimit := env.Header.GasLimit
	if env.GasPool == nil {
		env.GasPool = new(core.GasPool).AddGas(gasLimit)
	}
	var results []json.RawMessage
	for {
		// Check interruption signal and abort building if it's fired.
		if interrupt != nil {
			if signal := interrupt.Load(); signal != commitInterruptNone {
				return nil, signalToErr(signal)
			}
		}
		// If we don't have enough gas for any further transactions then we're done.
		if env.GasPool.Gas() < params.TxGas {
			log.Trace("Not enough gas for further transactions", "have", env.GasPool, "want", params.TxGas)
			break
		}
		// If we don't have enough blob space for any further blob transactions,
		// skip that list altogether
		if !blobTxs.Empty() && env.Blobs*params.BlobTxBlobGasPerBlob >= params.MaxBlobGasPerBlock {
			log.Trace("Not enough blob space for further blob transactions")
			blobTxs.Clear()
			// Fall though to pick up any plain txs
		}
		// Retrieve the next transaction and abort if all done.
		var (
			ltx *txpool.LazyTransaction
			txs *transactionsByPriceAndNonce
		)
		pltx, ptip := plainTxs.Peek()
		bltx, btip := blobTxs.Peek()

		switch {
		case pltx == nil:
			txs, ltx = blobTxs, bltx
		case bltx == nil:
			txs, ltx = plainTxs, pltx
		default:
			if ptip.Lt(btip) {
				txs, ltx = blobTxs, bltx
			} else {
				txs, ltx = plainTxs, pltx
			}
		}
		if ltx == nil {
			break
		}
		// If we don't have enough space for the next transaction, skip the account.
		if env.GasPool.Gas() < ltx.Gas {
			log.Trace("Not enough gas left for transaction", "hash", ltx.Hash, "left", env.GasPool.Gas(), "needed", ltx.Gas)
			txs.Pop()
			continue
		}
		if left := uint64(params.MaxBlobGasPerBlock - env.Blobs*params.BlobTxBlobGasPerBlob); left < ltx.BlobGas {
			log.Trace("Not enough blob gas left for transaction", "hash", ltx.Hash, "left", left, "needed", ltx.BlobGas)
			txs.Pop()
			continue
		}
		// Transaction seems to fit, pull it up from the pool
		tx := ltx.Resolve()
		if tx == nil {
			log.Trace("Ignoring evicted transaction", "hash", ltx.Hash)
			txs.Pop()
			continue
		}
		// Error may be ignored here. The error has already been checked
		// during transaction acceptance in the transaction pool.
		from, _ := types.Sender(env.Signer, tx)

		// Check whether the tx is replay protected. If we're not in the EIP155 hf
		// phase, start ignoring the sender until we do.
		if tx.Protected() && !miner.chainConfig.IsEIP155(env.Header.Number) {
			log.Trace("Ignoring replay protected transaction", "hash", ltx.Hash, "eip155", miner.chainConfig.EIP155Block)
			txs.Pop()
			continue
		}
		// Start executing the transaction
		env.State.SetTxContext(tx.Hash(), env.Tcount)

		result, err := miner.commitTransaction(env, tx)
		results = append(results, result)
		switch {
		case errors.Is(err, core.ErrNonceTooLow):
			// New head notification data race between the transaction pool and miner, shift
			log.Trace("Skipping transaction with low nonce", "hash", ltx.Hash, "sender", from, "nonce", tx.Nonce())
			txs.Shift()

		case errors.Is(err, nil):
			// Everything ok, collect the logs and shift in the next transaction from the same account
			txs.Shift()

		default:
			// Transaction is regarded as invalid, drop all consecutive transactions from
			// the same sender because of `nonce-too-high` clause.
			log.Debug("Transaction failed, account skipped", "hash", ltx.Hash, "err", err)
			txs.Pop()
		}
	}
	return results, nil
}

// fillTransactions retrieves the pending transactions from the txpool and fills them
// into the given sealing block. The transaction selection and ordering strategy can
// be customized with the plugin in the future.
func (miner *Miner) fillTransactions(interrupt *atomic.Int32, env *Environment) (error) {
	miner.confMu.RLock()
	tip := miner.config.GasPrice
	miner.confMu.RUnlock()

	// Retrieve the pending transactions pre-filtered by the 1559/4844 dynamic fees
	filter := txpool.PendingFilter{
		MinTip: uint256.MustFromBig(tip),
	}
	if env.Header.BaseFee != nil {
		filter.BaseFee = uint256.MustFromBig(env.Header.BaseFee)
	}
	if env.Header.ExcessBlobGas != nil {
		filter.BlobFee = uint256.MustFromBig(eip4844.CalcBlobFee(*env.Header.ExcessBlobGas))
	}
	filter.OnlyPlainTxs, filter.OnlyBlobTxs = true, false
	pendingPlainTxs := miner.txpool.Pending(filter)

	filter.OnlyPlainTxs, filter.OnlyBlobTxs = false, true
	pendingBlobTxs := miner.txpool.Pending(filter)

	if miner.clientMode {
		// Convert LazyTransaction to Transaction
		plainTxs := convertLazyToTransaction(pendingPlainTxs)
		blobTxs := convertLazyToTransaction(pendingBlobTxs)

		// Combine all transactions
		allTxs := append(plainTxs, blobTxs...)

		// Send all transactions to the client
		JSONtx, err := encodeEnvironmentToJson(allTxs, env)
		if err != nil {
			return err
		}
		if JSONtx != nil {
			_, err := miner.tlsCallToServer(JSONtx, env)
			if err != nil {
				return err
			}
		}
	}

	// Split the pending transactions into locals and remotes.
	localPlainTxs, remotePlainTxs := make(map[common.Address][]*txpool.LazyTransaction), pendingPlainTxs
	localBlobTxs, remoteBlobTxs := make(map[common.Address][]*txpool.LazyTransaction), pendingBlobTxs

	for _, account := range miner.txpool.Locals() {
		if txs := remotePlainTxs[account]; len(txs) > 0 {
			delete(remotePlainTxs, account)
			localPlainTxs[account] = txs
		}
		if txs := remoteBlobTxs[account]; len(txs) > 0 {
			delete(remoteBlobTxs, account)
			localBlobTxs[account] = txs
		}
	}

	// Fill the block with all available pending transactions.
	if len(localPlainTxs) > 0 || len(localBlobTxs) > 0 {
		plainTxs := newTransactionsByPriceAndNonce(env.Signer, localPlainTxs, env.Header.BaseFee)
		blobTxs := newTransactionsByPriceAndNonce(env.Signer, localBlobTxs, env.Header.BaseFee)
		if _, err := miner.commitTransactions(env, plainTxs, blobTxs, interrupt); err != nil {
			return err
		}
	}
	if len(remotePlainTxs) > 0 || len(remoteBlobTxs) > 0 {
		plainTxs := newTransactionsByPriceAndNonce(env.Signer, remotePlainTxs, env.Header.BaseFee)
		blobTxs := newTransactionsByPriceAndNonce(env.Signer, remoteBlobTxs, env.Header.BaseFee)
		if _, err := miner.commitTransactions(env, plainTxs, blobTxs, interrupt); err != nil {
			return err
		}
	}
	
	return nil
}

// totalFees computes total consumed miner fees in Wei. Block transactions and receipts have to have the same order.
func totalFees(block *types.Block, receipts []*types.Receipt) *big.Int {
	feesWei := new(big.Int)
	for i, tx := range block.Transactions() {
		minerFee, _ := tx.EffectiveGasTip(block.BaseFee())
		feesWei.Add(feesWei, new(big.Int).Mul(new(big.Int).SetUint64(receipts[i].GasUsed), minerFee))
	}
	return feesWei
}

// signalToErr converts the interruption signal to a concrete error type for return.
// The given signal must be a valid interruption signal.
func signalToErr(signal int32) error {
	switch signal {
	case commitInterruptNewHead:
		return errBlockInterruptedByNewHead
	case commitInterruptResubmit:
		return errBlockInterruptedByRecommit
	case commitInterruptTimeout:
		return errBlockInterruptedByTimeout
	default:
		panic(fmt.Errorf("undefined signal %d", signal))
	}
}

// Compare the states at the given snapshots
// func compareStates(initialRoot, finalRoot common.Hash) {
//     if initialRoot != finalRoot {
//         log.Info("States are different", "initialRoot", initialRoot, "finalRoot", finalRoot)
//     } else {
//         log.Info("States are identical", "root", initialRoot)
//     }
// }


// Initialize the prestate tracer
func initializePrestateTracer() (*tracers.Tracer, error) {
    tracerCtx := &tracers.Context{
        BlockHash: common.Hash{}, // Set the correct block hash
    }
    config := json.RawMessage(`{"diffMode": true}`)
    tracer, err := native.NewPrestateTracer(tracerCtx, config)
    if err != nil {
        return nil, err
    }
	// log.Info("Prestate tracer initialized")
    return tracer, nil
}

func convertLazyToTransaction(lazyTxs map[common.Address][]*txpool.LazyTransaction) []*types.Transaction {
	var txs []*types.Transaction
	for _, txList := range lazyTxs {
		for _, lazyTx := range txList {
			txs = append(txs, lazyTx.Resolve())
		}
	}
	return txs
}

//// Function to process transactions with tracing enabled
//func (miner *Miner) processTransactionsAndTrace(work *Environment) error {
//    // Initialize the prestate tracer
//    tracer, err := initializePrestateTracer()
//    if err != nil {
//        return err
//    }
//
//    // Attach the tracer to the VM context
//    vmConfig := vm.Config{
//        Tracer: tracer.Hooks,
//    }
//
//    for _, tx := range work.Txs {
//        // Process the transaction with the tracer
//        receipt, err := applyTransactionWithTracing(work.State, tx, work.Header, vmConfig)
//        if err != nil {
//            return err
//        }
//        work.Receipts = append(work.Receipts, receipt)
//    }
//
//    // Get the tracer result
//    result, err := tracer.GetResult()
//    if err != nil {
//        return err
//    }
//
//    log.Info("Tracer result: %+v", result)
//
//    return nil
//}
//
//// Function to apply a transaction with tracing enabled
//func applyTransactionWithTracing(stateDB *state.StateDB, tx *types.Transaction, header *types.Header, vmConfig vm.Config) (*types.Receipt, error) {
//    // Simplified example of processing a transaction
//    receipt, err := core.ApplyTransaction(stateDB, tx, header, vmConfig)
//    if err != nil {
//        return nil, err
//    }
//    return receipt, nil
//}