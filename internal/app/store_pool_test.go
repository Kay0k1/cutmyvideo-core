package app

import "testing"

func TestStorePoolDefaultIsBoundedAndExplicitSettingsArePreserved(t *testing.T) {
	for _, tt := range []struct {
		name, connection string
		max, min, idle   int32
	}{
		{"url default", "postgres://localhost/test", 4, 0, 0},
		{"keyword default", "host=localhost dbname=test", 4, 0, 0},
		{"url maximum", "postgres://localhost/test?pool_max_conns=9", 9, 0, 0},
		{"keyword maximum", "host=localhost pool_max_conns='2'", 2, 0, 0},
		{"larger minimum", "postgres://localhost/test?pool_min_conns=8", 8, 8, 0},
		{"larger idle minimum", "host=localhost pool_min_idle_conns=12", 12, 0, 12},
		{"both minimums", "host=localhost pool_min_conns=9 pool_min_idle_conns=6", 9, 9, 6},
		{"explicit limits", "postgres://localhost/test?pool_max_conns=16&pool_min_conns=8&pool_min_idle_conns=6", 16, 8, 6},
	} {
		t.Run(tt.name, func(t *testing.T) {
			config, err := storePoolConfig(tt.connection)
			if err != nil {
				t.Fatal(err)
			}
			if config.MaxConns != tt.max || config.MinConns != tt.min || config.MinIdleConns != tt.idle {
				t.Fatalf("pool settings: max=%d min=%d idle=%d; want %d/%d/%d", config.MaxConns, config.MinConns, config.MinIdleConns, tt.max, tt.min, tt.idle)
			}
		})
	}
	for _, raw := range []string{"postgres://localhost/test?pool_max_conns=0", "host=localhost pool_max_conns=invalid", "host=localhost pool_min_conns=invalid"} {
		if _, err := storePoolConfig(raw); err == nil {
			t.Fatal("invalid explicit pool setting was accepted")
		}
	}
}
