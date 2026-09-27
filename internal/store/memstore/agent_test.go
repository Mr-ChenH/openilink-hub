package memstore_test

import (
	"testing"

	"github.com/openilink/openilink-hub/internal/store/memstore"
	"github.com/openilink/openilink-hub/internal/store/storetest"
)

func TestAgentStore(t *testing.T) {
	storetest.TestAgentStore(t, memstore.New())
}
