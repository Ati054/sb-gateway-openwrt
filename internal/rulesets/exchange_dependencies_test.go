package rulesets

import (
	"slices"
	"testing"
)

func TestExchangeDependencySeedsWorkWithoutDownloadedSnapshot(t *testing.T) {
	for _, row := range []struct{ id, domain string }{{"bybit", "api.manepa.jp"}, {"bybit", "stream.spark-fintech.com"}, {"gateio", "fx-api-testnet.gateio.ws"}, {"binance", "binance.com"}} {
		got, err := Expressions([]string{row.id}, Snapshot{})
		if err != nil || !slices.Contains(got, "domain:"+row.domain) {
			t.Fatalf("%s dependency absent: %v %v", row.id, got, err)
		}
	}
	got, err := Expressions([]string{"bybit-api"}, Snapshot{})
	if err != nil || slices.Contains(got, "domain:bybit.com") {
		t.Fatal("API-only selection widened to all sites", got, err)
	}
}
