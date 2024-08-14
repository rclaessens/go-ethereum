package miner

import "github.com/ethereum/go-ethereum/metrics"

const (
	minerIngressMeterName = "miner/ingress"
	mineEgressMeterName = "miner/egress"
)

var (
	minerIngressMeter = metrics.NewRegisteredMeter(minerIngressMeterName, nil)
	minerEgressMeter = metrics.NewRegisteredMeter(mineEgressMeterName, nil)
)

func MarkMinerIngress(bytes int64) {
	if metrics.Enabled {
		minerIngressMeter.Mark(bytes)
	}
}

func MarkMinerEgress(bytes int64) {
	if metrics.Enabled {
		minerEgressMeter.Mark(bytes)
	}
}