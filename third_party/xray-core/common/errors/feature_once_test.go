package errors

import (
	"context"
	"sync"
	"testing"

	"github.com/xtls/xray-core/common/log"
)

type featureWarningSink struct {
	mu       sync.Mutex
	messages []log.Message
}

func (s *featureWarningSink) Handle(message log.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, message)
}

func TestRepeatedDeprecationsKeepFirstWarningAndAllRuntimeErrors(t *testing.T) {
	sink := &featureWarningSink{}
	log.RegisterHandler(sink)
	var work sync.WaitGroup
	for i := 0; i < 64; i++ {
		work.Add(1)
		go func() {
			defer work.Done()
			PrintNonRemovalDeprecatedFeatureWarning("fixture-grpc", "fixture-xhttp")
			PrintDeprecatedFeatureWarning("fixture-old-config", "fixture-new-config")
		}()
	}
	work.Wait()
	if len(sink.messages) != 2 {
		t.Fatal("deprecation spam", len(sink.messages))
	}
	PrintNonRemovalDeprecatedFeatureWarning("fixture-other-transport", "fixture-xhttp")
	for i := 0; i < 2; i++ {
		LogWarning(context.Background(), "fixture connection timeout")
		LogError(context.Background(), "fixture invalid config")
		if PrintRemovedFeatureError("fixture-removed", "fixture-new") == nil {
			t.Fatal("removed feature error suppressed")
		}
	}
	if len(sink.messages) != 7 {
		t.Fatal("distinct warning or repeated runtime errors lost", len(sink.messages))
	}
}
