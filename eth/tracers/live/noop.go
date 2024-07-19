package live

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"path/filepath"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/tracers"
	"gopkg.in/natefinch/lumberjack.v2"
)

func init() {
	tracers.LiveDirectory.Register("noop", newNoopTracer)
}

type BalanceChange struct {
	Address common.Address `json:"address"`
	Prev    *big.Int       `json:"prev"`
	New     *big.Int       `json:"new"`
	Reason  byte           `json:"reason"`
}

type NonceChange struct {
	Address common.Address `json:"address"`
	Prev    uint64         `json:"prev"`
	New     uint64         `json:"new"`
}

type CodeChange struct {
	Address      common.Address `json:"address"`
	PrevCodeHash common.Hash    `json:"prevCodeHash"`
	NewCodeHash  common.Hash    `json:"newCodeHash"`
}

type StateChange struct {
	BalanceChanges []BalanceChange `json:"balanceChanges"`
	NonceChanges   []NonceChange   `json:"nonceChanges"`
	CodeChanges    []CodeChange    `json:"codeChanges"`
}

type noop struct {
	mu          sync.Mutex
	stateChange StateChange
	logger      *log.Logger
}

type noopTracerConfig struct {
	Path    string `json:"path"`    // Path to the directory where the tracer logs will be stored
	MaxSize int    `json:"maxSize"` // MaxSize is the maximum size in megabytes of the tracer log file before it gets rotated. It defaults to 100 megabytes.
}

func newNoopTracer(cfg json.RawMessage) (*tracing.Hooks, error) {
	var config noopTracerConfig
	if cfg != nil {
		if err := json.Unmarshal(cfg, &config); err != nil {
			return nil, fmt.Errorf("failed to parse config: %v", err)
		}
	}

	if config.Path == "" {
		return nil, fmt.Errorf("output path is required")
	}

	// Store traces in a rotating file
	loggerOutput := &lumberjack.Logger{
		Filename: filepath.Join(config.Path, "noop.jsonl"),
	}

	if config.MaxSize > 0 {
		loggerOutput.MaxSize = config.MaxSize
	}

	logger := log.New(loggerOutput, "", 0)


	t := &noop{
		logger: logger,
	}
	return &tracing.Hooks{
		OnTxEnd:          t.OnTxEnd,
		OnBlockStart:     t.OnBlockStart,
		OnBlockEnd:       t.OnBlockEnd,
		OnBalanceChange:  t.OnBalanceChange,
		OnNonceChange:    t.OnNonceChange,
		OnCodeChange:     t.OnCodeChange,
	}, nil
}

func (t *noop) addChange(change interface{}) {
	t.mu.Lock()
	defer t.mu.Unlock()

	switch c := change.(type) {
	case BalanceChange:
		t.stateChange.BalanceChanges = append(t.stateChange.BalanceChanges, c)
	case NonceChange:
		t.stateChange.NonceChanges = append(t.stateChange.NonceChanges, c)
	case CodeChange:
		t.stateChange.CodeChanges = append(t.stateChange.CodeChanges, c)
	}
}

func (t *noop) OnTxEnd(receipt *types.Receipt, err error) {
	if err == nil && receipt != nil {
		fmt.Printf("Transaction %s has been validated successfully\n", receipt.TxHash.Hex())
	} else {
		fmt.Printf("Transaction failed with error: %v\n", err)
	}
}

func (t *noop) OnBlockStart(ev tracing.BlockEvent) {
	fmt.Printf("Block %d started processing\n", ev.Block.NumberU64())
}

func (t *noop) OnBlockEnd(err error) {
	if err == nil {
		t.sendStateChanges()
		t.resetStateChanges()
		fmt.Println("Changes successfully sended to the client")
	} else {
		fmt.Printf("Block processing failed with error: %v\n", err)
	}

	
}

func (t *noop) OnBalanceChange(a common.Address, prev, new *big.Int, reason tracing.BalanceChangeReason) {
	change := BalanceChange{
		Address: a,
		Prev:    prev,
		New:     new,
		Reason:  byte(reason),
	}
	t.addChange(change)
	fmt.Printf("Balance changed for address %s: from %s to %s\n", a.Hex(), prev.String(), new.String())
}

func (t *noop) OnNonceChange(a common.Address, prev, new uint64) {
	change := NonceChange{
		Address: a,
		Prev:    prev,
		New:     new,
	}
	t.addChange(change)
	fmt.Printf("Nonce changed for address %s: from %d to %d\n", a.Hex(), prev, new)
}

func (t *noop) OnCodeChange(a common.Address, prevCodeHash common.Hash, prev []byte, codeHash common.Hash, code []byte) {
	change := CodeChange{
		Address:      a,
		PrevCodeHash: prevCodeHash,
		NewCodeHash:  codeHash,
	}
	t.addChange(change)
	fmt.Printf("Code changed for address %s: previous code hash %s, new code hash %s\n", a.Hex(), prevCodeHash.Hex(), codeHash.Hex())
}

func (t *noop) dumpChangesToJSON() {
	t.mu.Lock()
	defer t.mu.Unlock()

	data, err := json.MarshalIndent(t.stateChange, "", "  ")
	if err != nil {
		fmt.Printf("Failed to marshal changes to JSON: %v\n", err)
		return
	}

	t.logger.Println(string(data))
}

func (t *noop) sendStateChanges() {
	data, err := json.MarshalIndent(t.stateChange, "", "  ")
	if err != nil {
		fmt.Printf("Failed to marshal changes to JSON: %v\n", err)
		return
	}

	// Send the JSON data to the client
	// Assuming a client endpoint is available at http://localhost:8080/update
	resp, err := http.Post("http://localhost:8080/update", "application/json", bytes.NewBuffer(data))
	if err != nil {
		fmt.Printf("Failed to send changes to client: %v\n", err)
		return
	}
	defer resp.Body.Close()

	fmt.Println("Successfully sent state changes to client")
}

func (t *noop) resetStateChanges() {
	t.stateChange = StateChange{}
}