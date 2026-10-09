package proxy

import (
	"bytes"
	"context"
	"testing"

	"github.com/xtls/xray-core/common/buf"
)

func TestXtlsPaddingPreservesFullBuffer(t *testing.T) {
	payload := bytes.Repeat([]byte{0x5a}, buf.Size)
	tests := []struct {
		name string
		uuid []byte
	}{
		{name: "uuid already sent"},
		{name: "first frame", uuid: bytes.Repeat([]byte{0x7b}, 16)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := buf.New()
			if written, err := input.Write(payload); err != nil || written != len(payload) {
				t.Fatalf("write full input: written=%d err=%v", written, err)
			}
			uuid := append([]byte(nil), test.uuid...)
			result := XtlsPadding(
				input,
				CommandPaddingContinue,
				&uuid,
				false,
				context.Background(),
				[]uint32{900, 500, 900, 1},
			)
			defer result.Release()

			headerLen := len(test.uuid) + 5
			if got, want := int(result.Len()), headerLen+len(payload); got != want {
				t.Fatalf("padded length=%d, want %d", got, want)
			}
			if !bytes.Equal(result.BytesRange(int32(headerLen), result.Len()), payload) {
				t.Fatal("full input payload was not preserved")
			}
			if uuid != nil {
				t.Fatal("UUID was not consumed")
			}
		})
	}
}
