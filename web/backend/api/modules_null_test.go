package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xibodev/compa/internal/module"
	"github.com/xibodev/compa/internal/moduletools"
	"github.com/xibodev/compa/pkg/modproto"
)

func TestModuleViewOptionalListsAreArrays(t *testing.T) {
	v := moduleView(moduletools.Installed{Descriptor: &modproto.Descriptor{Module: "test"}, Runner: &module.Runner{Binary: "test.exe"}})
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"requirements", "filesystem_read", "filesystem_write", "network", "credentials", "paid_providers", "subprocess"} {
		if !strings.Contains(string(b), `"`+field+`":[]`) {
			t.Fatalf("%s must be an array: %s", field, b)
		}
	}
}
